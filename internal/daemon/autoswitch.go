package daemon

import (
	"errors"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

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

// OnSample is the watcher callback. Accumulates per-sample tokens into
// the local-estimate counters that drive SessionBarView.
//
// Historical note: this used to also fire probe-driven account switches
// on tripwire crossings via evaluateAndSwitch. That path consumed real
// tokens (each probe was a 1-token /v1/messages call) and looked
// machine-generated to Anthropic's abuse classifiers. The new
// AutoSwapper (autoswap.go) supersedes it — it makes the same kind of
// decision using OAuth /api/oauth/usage data, which is server-side
// truth and doesn't consume tokens.
//
// The tripwire-and-probe path lives on as dead code in evaluateAndSwitch
// + probeAccount + probe.go pending a full retirement commit; OnSample
// no longer calls into it.
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

	pct := float64(a.usageSinceReset) / float64(a.observedBudget) * 100.0
	a.lastPercent = pct
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

// evaluateAndSwitch and probeAccount were removed in 9b9da1b after the
// tripwire-driven swap path was retired (commit 65abc60). Account-
// rotation decisions now live in AutoSwapper (autoswap.go), which uses
// OAuth /api/oauth/usage data instead of real-API-call probes.
//
// probe.go's standalone Prober still exists for `aimonitor probe`
// manual debugging; a future cleanup commit will retire it along with
// the Provider interface's ProbeServerSide method.
