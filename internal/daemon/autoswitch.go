package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// ProbeTopK caps the number of probes we fire on a single tripwire
// crossing. Each probe costs ~5-10 tokens of quota, so we don't want to
// fan out unboundedly.
const ProbeTopK = 3

// AutoSwitcher is the tripwire-driven account-switching engine. It
// subscribes to JSONL samples from the watcher and, when a configured
// threshold is crossed on the currently-active account, evaluates other
// accounts via server-side probes and (optionally) swaps the active
// credential.
//
// Design choices for v1:
//   - "Currently-active account" is detected per-sample by byte-matching
//     the live keyring slot against known stashes. Cheap (~1ms).
//   - Session budget is observed-max: highest accumulated_usage_since_reset
//     we've ever seen. Persisted in settings via SettingsKeyBudget.
//   - Cool-down is enforced in-process. Daemon restart resets it; this is
//     acceptable for v1.
type AutoSwitcher struct {
	store    *store.Store
	provider provider.Provider
	cfg      config.Config
	clock    func() time.Time

	mu                sync.Mutex
	usageSinceReset   int64
	observedBudget    int64
	lastPercent       float64
	lastSwitchAt      time.Time
	lastTripwireFired int
}

// AutoSwitcherConfig wires the engine. clock is optional (defaults to
// time.Now); it's the seam tests use to fast-forward cooldowns.
type AutoSwitcherConfig struct {
	Store    *store.Store
	Provider provider.Provider
	Config   config.Config
	Clock    func() time.Time
}

// NewAutoSwitcher constructs an AutoSwitcher.
func NewAutoSwitcher(cfg AutoSwitcherConfig) (*AutoSwitcher, error) {
	if cfg.Store == nil || cfg.Provider == nil {
		return nil, errors.New("AutoSwitcher: Store and Provider required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	a := &AutoSwitcher{
		store:    cfg.Store,
		provider: cfg.Provider,
		cfg:      cfg.Config,
		clock:    clock,
	}
	a.observedBudget = 1_000_000 // conservative starting estimate
	return a, nil
}

// SetConfig updates the runtime config (e.g. when the user changes the
// threshold list or toggles autoswitch via `aimonitor config set`).
// Safe to call from any goroutine.
func (a *AutoSwitcher) SetConfig(cfg config.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = cfg
}

// OnSample is the watcher callback. Idempotent on missing accounts table.
func (a *AutoSwitcher) OnSample(ev SampleEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tokens := ev.Sample.InputTokens + ev.Sample.OutputTokens +
		ev.Sample.CacheRead + ev.Sample.CacheWrite
	if tokens <= 0 {
		return
	}
	a.usageSinceReset += tokens
	if a.usageSinceReset > a.observedBudget {
		a.observedBudget = a.usageSinceReset
	}

	if !a.cfg.AutoSwitch {
		return
	}

	pct := float64(a.usageSinceReset) / float64(a.observedBudget) * 100.0
	tripwire := crossedTripwire(a.lastPercent, pct, a.cfg.Thresholds)
	a.lastPercent = pct
	if tripwire == 0 {
		return
	}
	if tripwire == a.lastTripwireFired {
		return
	}
	if a.clock().Sub(a.lastSwitchAt) < a.cfg.CooldownDuration() {
		return
	}

	// Release the lock during the probe + switch dance; those are slow
	// (HTTP + keychain) and we don't want to block sample delivery.
	a.mu.Unlock()
	err := a.evaluateAndSwitch(context.Background(), tripwire)
	a.mu.Lock()

	a.lastTripwireFired = tripwire
	if err == nil {
		a.lastSwitchAt = a.clock()
	}
}

// crossedTripwire returns the just-crossed threshold value, or 0 when no
// crossing occurred between prev and cur. Crossings only count when cur
// is on the higher side of a tripwire and prev was on the lower side.
func crossedTripwire(prev, cur float64, thresholds []int) int {
	for _, t := range thresholds {
		ft := float64(t)
		if prev < ft && cur >= ft {
			return t
		}
	}
	return 0
}

// evaluateAndSwitch is the heart of the auto-switch decision. Called when
// a tripwire crossing fires and cool-down has elapsed. Returns nil iff a
// switch happened (or we deliberately chose not to switch); a non-nil
// error indicates something unexpected (DB / keyring / network).
func (a *AutoSwitcher) evaluateAndSwitch(ctx context.Context, tripwire int) error {
	// 1. Identify the currently-active account via byte-match.
	live, err := a.provider.ActiveCredential(ctx)
	if err != nil {
		return fmt.Errorf("read active credential: %w", err)
	}
	if len(live.Bytes) == 0 {
		return errors.New("active credential is empty; nothing to switch from")
	}
	defer live.Zero()

	accounts, err := a.store.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	var current *store.Account
	for i := range accounts {
		stash, err := claude.RetrieveStash(ctx, accounts[i].KeyringRef)
		if err != nil {
			continue
		}
		if bytes.Equal(stash.Bytes, live.Bytes) {
			current = &accounts[i]
			break
		}
	}
	if current == nil {
		return errors.New("active credential doesn't match any stashed account")
	}

	// 2. Filter candidates: all OTHER accounts with local-% < tripwire.
	//    For v1 we don't track per-account local usage (we only have a
	//    counter for whichever account is active), so every other account
	//    qualifies. Phase 4 will refine this.
	var candidates []store.Account
	for _, acct := range accounts {
		if acct.ID == current.ID {
			continue
		}
		candidates = append(candidates, acct)
	}
	if len(candidates) == 0 {
		return nil // no candidates, nothing to do
	}

	// 3. Top-K=3 by most-recently-used (proxy for liveness). Sort and trim.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastUsedAt.After(candidates[j].LastUsedAt)
	})
	if len(candidates) > ProbeTopK {
		candidates = candidates[:ProbeTopK]
	}

	// 4. Probe each candidate + the current account. Best-by-remaining wins.
	currentProbe, _ := a.probeAccount(ctx, *current) // tolerate probe errors here
	var best *store.Account
	bestRemaining := currentProbe.TokensRemaining
	for i := range candidates {
		rl, err := a.probeAccount(ctx, candidates[i])
		if err != nil {
			continue
		}
		if rl.TokensRemaining > bestRemaining {
			bestRemaining = rl.TokensRemaining
			best = &candidates[i]
		}
	}

	// 5. Switch if we found something strictly better.
	if best == nil {
		_ = a.store.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
			Ts:                  a.clock(),
			FromLabel:           current.Label,
			ToLabel:             current.Label, // no change
			Trigger:             store.TriggerAutoswitch,
			Reason:              fmt.Sprintf("tripwire %d: no candidate had more remaining than current (%d)", tripwire, currentProbe.TokensRemaining),
			FromProbedRemaining: currentProbe.TokensRemaining,
		})
		return nil
	}

	cred, err := claude.RetrieveStash(ctx, best.KeyringRef)
	if err != nil {
		return fmt.Errorf("read stash for %q: %w", best.Label, err)
	}
	defer cred.Zero()

	if err := a.provider.SetActiveCredential(ctx, cred); err != nil {
		return fmt.Errorf("set active credential: %w", err)
	}
	_ = a.store.UpdateAccountLastUsed(ctx, best.ID, a.clock())
	_ = a.store.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts:                  a.clock(),
		FromLabel:           current.Label,
		ToLabel:             best.Label,
		Trigger:             store.TriggerAutoswitch,
		Reason:              fmt.Sprintf("tripwire %d crossed; %q probed remaining=%d vs current=%d", tripwire, best.Label, bestRemaining, currentProbe.TokensRemaining),
		FromProbedRemaining: currentProbe.TokensRemaining,
		ToProbedRemaining:   bestRemaining,
	})

	// Reset local-usage counter after a successful switch; new account
	// starts fresh from aimonitor's perspective.
	a.usageSinceReset = 0
	a.lastPercent = 0

	return nil
}

// probeAccount fires a fresh probe (no cache; tripwire decisions need
// up-to-the-second truth) and persists the result.
func (a *AutoSwitcher) probeAccount(ctx context.Context, acct store.Account) (provider.RateLimit, error) {
	rl, err := a.provider.ProbeServerSide(ctx, provider.Account{
		ID:         acct.ID,
		Provider:   acct.Provider,
		Label:      acct.Label,
		KeyringRef: acct.KeyringRef,
	})
	if rl.HTTPStatus != 0 || rl.TokensRemaining != 0 {
		_ = a.store.PutProbeResult(ctx, acct.ID, rl)
	}
	return rl, err
}
