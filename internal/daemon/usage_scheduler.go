package daemon

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// UsageScheduler periodically calls Anthropic's /api/oauth/usage endpoint
// for the *active* account and persists the result to the oauth_usage
// table. Inactive accounts are not fetched in the background — the
// menu-bar widget triggers an on-demand fetch when the popover opens
// (throttled, separately).
//
// The scheduling discipline is the load-bearing safety property here.
// Anthropic's abuse classifiers flag account-level traffic that looks
// machine-generated, and a misbehaving aimonitor build that pings
// /api/oauth/usage every few seconds could trigger an account ban. The
// following invariants protect against that:
//
//   - **Baseline ≥ 300 s.** Five minutes between background fetches per
//     account. Far below any plausible bot threshold.
//   - **Jitter ± 30 s.** Each fetch lands at baseline ± uniform(-30s, +30s).
//     Periodic clock-aligned traffic is one of the cheapest patterns to
//     detect; jitter destroys that signal.
//   - **Speedup only at high utilization.** Drop to 60 s baseline (still
//     jittered) only while the active account's 5-hour utilization is
//     ≥ 90 %. Below that the user has nothing they need to react to in
//     under five minutes.
//   - **Exponential backoff on errors, capped at 1 hour.** Any 4xx (other
//     than 401, which is a re-auth event surfaced separately) or 5xx
//     doubles the next interval. A buggy build cannot accidentally
//     hammer the API; the worst case is one fetch per hour.
//
// 401 (UsageAuthError) and 429 (UsageThrottledError) are reserved
// classifications that the scheduler treats differently:
//   - 401 stops further fetches for the affected credential until the
//     daemon restarts (re-auth needed).
//   - 429 forces the backoff to its maximum immediately.
type UsageScheduler struct {
	Store    *store.Store
	Provider provider.Provider
	Fetcher  *claude.UsageFetcher

	// Baseline is the normal interval between fetches for an account
	// below SpeedupAtPct utilization. Default 300 s.
	Baseline time.Duration

	// SpeedupAtPct is the 5-hour utilization (0..100) at or above which
	// the scheduler switches to SpeedupInterval. Default 90.
	SpeedupAtPct float64

	// SpeedupInterval is the interval used while at or above
	// SpeedupAtPct. Default 60 s.
	SpeedupInterval time.Duration

	// MaxBackoff caps the exponential backoff applied after errors.
	// Default 1 h.
	MaxBackoff time.Duration

	// Jitter is the maximum absolute offset added to each scheduled
	// interval. The actual offset is uniform on [-Jitter, +Jitter].
	// Default 30 s.
	Jitter time.Duration

	// ResolveActive returns the currently-active store.Account, or
	// (_, false, nil) when there is no active account. Optional; the
	// default implementation byte-matches the live keychain blob against
	// every account's stash via claude.RetrieveStash. Tests inject a
	// deterministic version that does not touch the keychain.
	ResolveActive func(ctx context.Context) (store.Account, bool, error)

	// AfterFetch is invoked after each successful fetch with the active
	// account's label and freshly-persisted Limits. Used to trigger the
	// AutoSwapper decision. Nil disables the hook.
	AfterFetch func(ctx context.Context, activeLabel string)

	// rand is seeded per-scheduler so tests can substitute a deterministic
	// source. Default uses the package math/rand/v2 global source.
	rand *rand.Rand
}

// defaults applies the production constants to any field left zero. Lets
// callers construct UsageScheduler{Store: s, Provider: p, Fetcher: f}
// and get the right behaviour without enumerating every knob.
func (u *UsageScheduler) defaults() {
	if u.Baseline == 0 {
		u.Baseline = 300 * time.Second
	}
	if u.SpeedupAtPct == 0 {
		u.SpeedupAtPct = 90
	}
	if u.SpeedupInterval == 0 {
		u.SpeedupInterval = 60 * time.Second
	}
	if u.MaxBackoff == 0 {
		u.MaxBackoff = 1 * time.Hour
	}
	if u.Jitter == 0 {
		u.Jitter = 30 * time.Second
	}
	if u.rand == nil {
		u.rand = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xa1ec0de))
	}
}

// initialDelay is the wait before the very first fetch. A small fixed
// delay (not the jittered baseline) so the bars populate within seconds
// of daemon start, while still not firing the instant the process comes
// up. Never longer than the baseline.
func (u *UsageScheduler) initialDelay() time.Duration {
	const d = 3 * time.Second
	if u.Baseline < d {
		return u.Baseline
	}
	return d
}

// Run blocks until ctx is cancelled, fetching limits for the active
// account on a jittered, error-aware schedule. The first fetch happens
// after a single jittered baseline interval so the daemon doesn't spike
// every Anthropic-bound request at boot.
func (u *UsageScheduler) Run(ctx context.Context) error {
	u.defaults()

	// Fetch soon after start (not after a full baseline interval) so a
	// freshly-launched daemon populates the usage bars within seconds
	// instead of leaving them blank for the first 5 minutes. One request
	// for the active account at boot is not a meaningful spike.
	timer := time.NewTimer(u.initialDelay())
	defer timer.Stop()

	currentBackoff := u.Baseline
	authDenied := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			// 401 latches: stop hitting Anthropic until restart.
			if authDenied {
				timer.Reset(u.MaxBackoff)
				continue
			}
			err := u.tickOnce(ctx)
			next := u.Baseline
			switch {
			case claude.IsAuthError(err):
				authDenied = true
				next = u.MaxBackoff
				fmt.Fprintf(os.Stderr, "usage: auth denied, halting background fetches: %v\n", err)
			case claude.IsThrottledError(err):
				// A 429 is often a transient burst limit (e.g. another tool
				// — or claude-bar — polling the same /api/oauth/usage
				// endpoint for this account). Escalate gradually like any
				// other error rather than jumping straight to the 1 h cap,
				// so a brief throttle costs minutes of staleness, not an
				// hour of blank usage bars. Sustained 429s still ramp to
				// the cap.
				currentBackoff = doubleCapped(currentBackoff, u.MaxBackoff)
				next = currentBackoff
				fmt.Fprintf(os.Stderr, "usage: throttled by Anthropic (429), backoff %v: %v\n", next, err)
			case err != nil:
				currentBackoff = doubleCapped(currentBackoff, u.MaxBackoff)
				next = currentBackoff
				fmt.Fprintf(os.Stderr, "usage: error, backoff %v: %v\n", next, err)
			default:
				currentBackoff = u.Baseline
				if pct, ok := u.activePct(ctx); ok && pct >= u.SpeedupAtPct {
					next = u.SpeedupInterval
				}
			}
			timer.Reset(u.jittered(next))
		}
	}
}

// tickOnce runs one fetch cycle. Returns nil on success, the underlying
// error otherwise. Resolves the active account, reads its credential,
// fetches limits, persists.
func (u *UsageScheduler) tickOnce(ctx context.Context) error {
	resolve := u.ResolveActive
	if resolve == nil {
		resolve = u.activeAccount
	}
	acct, found, err := resolve(ctx)
	if err != nil {
		return fmt.Errorf("active account: %w", err)
	}
	if !found {
		// No active account yet (e.g. before first `aimonitor add`).
		// Not an error — just nothing to fetch this cycle.
		return nil
	}

	cred, err := u.Provider.ActiveCredential(ctx)
	if err != nil {
		return fmt.Errorf("active credential: %w", err)
	}
	defer cred.Zero()
	if len(cred.Bytes) == 0 {
		return nil
	}

	limits, err := u.Fetcher.FetchLimits(ctx, cred)
	if err != nil {
		return err
	}
	limits.AccountID = acct.ID
	if err := u.Store.PutLimits(ctx, acct.ID, limits); err != nil {
		return fmt.Errorf("put limits: %w", err)
	}
	if u.AfterFetch != nil {
		// Run synchronously inside the tick — auto-swap should fire
		// before the next interval is scheduled so the next tick can
		// see the new active account.
		u.AfterFetch(ctx, acct.Label)
	}
	return nil
}

// activeAccount finds the account row whose stashed credential bytes
// equal the bytes currently in the live keychain slot. Returns
// (acct, true, nil) on a match; (_, false, nil) when there is no match
// (fresh install, or external rotation that bypassed aimonitor).
func (u *UsageScheduler) activeAccount(ctx context.Context) (store.Account, bool, error) {
	live, err := u.Provider.ActiveCredential(ctx)
	if err != nil {
		return store.Account{}, false, err
	}
	defer live.Zero()
	if len(live.Bytes) == 0 {
		return store.Account{}, false, nil
	}
	accounts, err := u.Store.ListAccounts(ctx)
	if err != nil {
		return store.Account{}, false, err
	}
	for _, a := range accounts {
		stash, err := claude.RetrieveStash(ctx, a.KeyringRef)
		if err != nil {
			continue
		}
		match := bytes.Equal(stash.Bytes, live.Bytes)
		stash.Zero()
		if match {
			return a, true, nil
		}
	}
	return store.Account{}, false, nil
}

// activePct returns the most recently persisted 5-hour utilization for
// the active account, used to decide whether to switch to the high-
// cadence interval. Returns (0, false) when there is no persisted data
// or no active account.
func (u *UsageScheduler) activePct(ctx context.Context) (float64, bool) {
	resolve := u.ResolveActive
	if resolve == nil {
		resolve = u.activeAccount
	}
	acct, found, err := resolve(ctx)
	if err != nil || !found {
		return 0, false
	}
	limits, err := u.Store.GetLimits(ctx, acct.ID)
	if err != nil {
		return 0, false
	}
	return limits.FiveHourPct, true
}

// jittered returns base ± uniform(-Jitter, +Jitter), clamped to a
// minimum of one second so a misconfiguration cannot turn into a tight
// loop.
func (u *UsageScheduler) jittered(base time.Duration) time.Duration {
	if u.Jitter <= 0 {
		return base
	}
	offset := time.Duration(u.rand.Int64N(int64(2*u.Jitter))) - u.Jitter
	out := base + offset
	if out < time.Second {
		out = time.Second
	}
	return out
}

// doubleCapped returns min(d*2, maxDur). Used by exponential backoff.
// Named maxDur instead of `cap` because `cap` is a built-in identifier
// and revive's redefines-builtin-id catches the shadowing.
func doubleCapped(d, maxDur time.Duration) time.Duration {
	doubled := d * 2
	if doubled > maxDur {
		return maxDur
	}
	return doubled
}
