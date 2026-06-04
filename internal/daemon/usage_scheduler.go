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
//   - **Exponential backoff on errors, capped at 1 hour.** Any 4xx or 5xx
//     doubles the next interval. A buggy build cannot accidentally
//     hammer the API; the worst case is one fetch per hour.
//
// Token-auth outcomes are handled without ever permanently halting:
//   - **401 triggers a refresh, not a halt.** An expired access token is
//     refreshed in place via the account's refresh token (under the switch
//     lock, after a cache-bypassed re-read so we defer to whatever Claude
//     Code or another tool last wrote) and the fetch is retried. Only a
//     401 that survives a fresh access token, or a rejected *refresh*
//     token, backs the schedule off to the 1-hour cap — and even then it
//     keeps retrying hourly, so usage self-heals once a valid token
//     reappears (the user re-runs `claude`, or re-adds the account).
//   - **429 forces the backoff to its maximum immediately.**
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

	// RefreshActive, when set, ensures the live credential holds a valid
	// access token before each fetch — refreshing it under the switch lock
	// when expired (force=false) or after a 401 (force=true) — and returns
	// the credential now in the live slot, so the fetch uses exactly that
	// blob. Nil disables refresh (tests, or a provider with no token
	// endpoint); tickOnce then reads the live credential directly.
	RefreshActive func(ctx context.Context, acct store.Account, force bool) (provider.Credential, error)

	// rand is seeded per-scheduler so tests can substitute a deterministic
	// source. Default uses the package math/rand/v2 global source.
	rand *rand.Rand

	// inactiveRR is the round-robin cursor for background-polling inactive
	// accounts' usage — one per successful active tick (see fetchOneInactive).
	inactiveRR int
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

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			err := u.tickOnce(ctx)
			next := u.Baseline
			switch {
			case claude.IsRefreshExpired(err):
				// The refresh token was rejected. This is either a genuinely
				// dead token (user must re-login via `aimonitor add`) or a
				// transient cross-tool rotation race — Claude Code or a second
				// menu-bar app refreshed first, invalidating the token we held.
				// One failure can't tell them apart, so back off to the cap and
				// keep retrying hourly rather than permanently halting: usage
				// self-heals once a valid token reappears, and an hourly retry
				// never looks like abuse.
				currentBackoff = u.MaxBackoff
				next = u.MaxBackoff
				fmt.Fprintf(os.Stderr, "usage: token refresh rejected; retrying in %v (re-login with `aimonitor add` if usage stays blank): %v\n", next, err)
			case claude.IsAuthError(err):
				// A 401 that survived a forced token refresh — the account
				// looks deauthorized server-side. Back off hard and retry
				// hourly; do not latch.
				currentBackoff = u.MaxBackoff
				next = u.MaxBackoff
				fmt.Fprintf(os.Stderr, "usage: auth rejected after refresh; retrying in %v: %v\n", next, err)
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
				// Active fetch succeeded; opportunistically poll ONE inactive
				// account (round-robin) so the per-account usage bars stay
				// populated. Valid-token-only and best-effort — see
				// fetchOneInactive. Skipped during speedup so a near-limit
				// active account doesn't drag inactive polling to a fast cadence.
				if pct, ok := u.activePct(ctx); ok && pct >= u.SpeedupAtPct {
					next = u.SpeedupInterval
				} else {
					u.fetchOneInactive(ctx)
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

	cred, err := u.liveCredential(ctx, acct, false)
	if err != nil {
		return fmt.Errorf("active credential: %w", err)
	}
	// Closure form so the deferred zero covers whatever cred points at when
	// tickOnce returns, including a credential swapped in by the 401 retry.
	defer func() { cred.Zero() }()
	if len(cred.Bytes) == 0 {
		return nil
	}

	limits, err := u.Fetcher.FetchLimits(ctx, cred)
	if claude.IsAuthError(err) && u.RefreshActive != nil {
		// The live token was rejected even though we didn't refresh it (it
		// looked unexpired, or expiresAt was absent). Force one refresh and
		// retry — recovers a revoked token or a blob with a wrong expiry,
		// without the permanent halt the old code applied on any 401.
		fresh, rerr := u.RefreshActive(ctx, acct, true)
		if rerr != nil {
			return rerr
		}
		cred.Zero()
		cred = fresh
		if len(cred.Bytes) == 0 {
			return nil
		}
		limits, err = u.Fetcher.FetchLimits(ctx, cred)
	}
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

// liveCredential returns the live credential for acct with a valid access
// token. When RefreshActive is wired it refreshes-if-expired under the
// switch lock (force passed through); otherwise it falls back to a direct
// read of the live slot (tests / providers with no token endpoint).
func (u *UsageScheduler) liveCredential(ctx context.Context, acct store.Account, force bool) (provider.Credential, error) {
	if u.RefreshActive != nil {
		return u.RefreshActive(ctx, acct, force)
	}
	return u.Provider.ActiveCredential(ctx)
}

// fetchOneInactive polls usage for ONE inactive account per call,
// round-robin, so the per-account usage bars stay populated without
// fetching every account every tick.
//
// It uses ONLY the account's stashed access token, and only while that
// token is still valid. It deliberately never refreshes an inactive
// account's token: a refresh rotates the refresh token and would
// invalidate any other credential manager's copy of the same account
// (Claude Code, a second menu-bar app), triggering cross-tool token churn.
// An expired stash is left untouched — the account keeps its last-known
// usage and the UI marks it stale. The account's data refreshes naturally
// the next time aimonitor switches to it (the switch re-stashes a fresh
// blob) or the user re-imports.
//
// Entirely best-effort: any failure is swallowed (logged at most) so
// background inactive polling can never disrupt the active schedule.
func (u *UsageScheduler) fetchOneInactive(ctx context.Context) {
	resolve := u.ResolveActive
	if resolve == nil {
		resolve = u.activeAccount
	}
	activeAcct, activeFound, err := resolve(ctx)
	if err != nil {
		return
	}
	accounts, err := u.Store.ListAccounts(ctx)
	if err != nil || len(accounts) == 0 {
		return
	}
	var inactive []store.Account
	for _, a := range accounts {
		if activeFound && a.ID == activeAcct.ID {
			continue
		}
		inactive = append(inactive, a)
	}
	if len(inactive) == 0 {
		return
	}
	acct := inactive[u.inactiveRR%len(inactive)]
	u.inactiveRR++

	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return
	}
	defer stash.Zero()
	tokens, err := claude.ParseCredential(stash)
	if err != nil {
		return
	}
	if claude.IsExpired(tokens.ExpiresAt) {
		return // valid-token-only: never refresh an inactive (shared) token
	}
	limits, err := u.Fetcher.FetchLimits(ctx, stash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage: inactive %q fetch: %v\n", acct.Label, err)
		return
	}
	limits.AccountID = acct.ID
	if err := u.Store.PutLimits(ctx, acct.ID, limits); err != nil {
		fmt.Fprintf(os.Stderr, "usage: inactive %q put limits: %v\n", acct.Label, err)
	}
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

// activePct returns the active account's hotter utilization — the max of
// its 5-hour and 7-day percentages — used to decide whether to switch to
// the high-cadence interval. The 7-day window counts because a weekly-
// capped account arms an auto-swap just like a 5-hour-capped one does.
// Returns (0, false) when there is no persisted data or no active account.
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
	return max(limits.FiveHourPct, limits.SevenDayPct), true
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
