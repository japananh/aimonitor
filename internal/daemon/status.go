package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Status is the periodic snapshot the daemon publishes to the settings
// table so the menu bar widget (and any other reader) can render live
// state without needing a Unix socket or JSON-RPC server.
//
// The widget polls this row every ~2s. Schema must stay backwards
// compatible across minor releases; widget renders gracefully on
// missing fields.
type Status struct {
	// PublishedAt is the wall-clock at which the daemon wrote this
	// snapshot. Widget shows "stale" if PublishedAt is more than ~15s
	// behind now (daemon probably exited).
	PublishedAt time.Time `json:"published_at"`

	// ActiveLabel is the label of the account currently in the
	// Claude Code-credentials slot (byte-matched). Empty when the
	// daemon could not identify an active account.
	ActiveLabel string `json:"active_label,omitempty"`

	// UsageSinceReset and ObservedBudget come straight from the
	// AutoSwitcher's accumulator. SessionPercent is the percentage
	// the widget renders in the bar.
	UsageSinceReset int64   `json:"usage_since_reset"`
	ObservedBudget  int64   `json:"observed_budget"`
	SessionPercent  float64 `json:"session_percent"`

	// AutoSwitchEnabled mirrors config.AutoSwitch at the time the
	// snapshot was taken. Helps the widget show the right toggle
	// state without re-loading the YAML config itself.
	AutoSwitchEnabled bool `json:"auto_switch_enabled"`

	// LastSwitchAt is when the auto-switcher last fired (zero if
	// never). Used to render the cool-down countdown.
	LastSwitchAt time.Time `json:"last_switch_at,omitempty"`

	// FiveHourPct / SevenDayPct are the active account's OAuth-introspected
	// utilization, fetched on the UsageScheduler's cadence (~300 s with
	// jitter). Zero values mean "no data yet" — the Swift widget hides the
	// bars in that case rather than rendering 0%.
	FiveHourPct float64 `json:"five_hour_pct,omitempty"`
	SevenDayPct float64 `json:"seven_day_pct,omitempty"`

	// FiveHourResetAt / SevenDayResetAt mirror the above for the reset
	// countdowns. Zero when unknown.
	FiveHourResetAt time.Time `json:"five_hour_reset_at,omitempty"`
	SevenDayResetAt time.Time `json:"seven_day_reset_at,omitempty"`

	// LimitsFetchedAt is when the UsageScheduler last persisted limits for
	// the active account. Widget shows a "~" stale indicator if older
	// than ~2× the baseline interval.
	LimitsFetchedAt time.Time `json:"limits_fetched_at,omitempty"`
}

// snapshot reads AutoSwitcher fields under its lock so we don't race
// the OnSample path. Pure accessor; no side effects.
func (a *AutoSwitcher) snapshot(activeLabel string) Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	pct := 0.0
	if a.observedBudget > 0 {
		pct = float64(a.usageSinceReset) / float64(a.observedBudget) * 100.0
	}
	return Status{
		PublishedAt:       a.clock(),
		ActiveLabel:       activeLabel,
		UsageSinceReset:   a.usageSinceReset,
		ObservedBudget:    a.observedBudget,
		SessionPercent:    pct,
		AutoSwitchEnabled: a.cfg.AutoSwitch,
		LastSwitchAt:      a.lastSwitchAt,
	}
}

// StatusPublisher writes a Status JSON blob to the settings table every
// Interval. Run() blocks until ctx is cancelled.
type StatusPublisher struct {
	Store    *store.Store
	Auto     *AutoSwitcher
	Interval time.Duration

	// ActiveLabel resolves the currently-active account label. It's a
	// callback because the byte-match logic lives in evaluateAndSwitch
	// (provider.ActiveCredential + stash byte-equal); we don't want to
	// duplicate it here. Empty result is fine.
	ActiveLabel func(ctx context.Context) string
}

// Run blocks until ctx is cancelled, publishing a fresh Status row on
// every Interval tick. The first publish happens immediately so a
// just-started widget has data without waiting a full interval.
func (p *StatusPublisher) Run(ctx context.Context) error {
	if p.Interval <= 0 {
		p.Interval = 2 * time.Second
	}
	p.publish(ctx)
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.publish(ctx)
		}
	}
}

func (p *StatusPublisher) publish(ctx context.Context) {
	label := ""
	if p.ActiveLabel != nil {
		label = p.ActiveLabel(ctx)
	}
	st := p.Auto.snapshot(label)
	if label != "" {
		// Look up the active account's persisted limits and attach
		// them to the snapshot. Best-effort: any failure (no account
		// row yet, no limits row yet) leaves the corresponding fields
		// zero — the widget hides bars without data, which is the
		// right "no data" state.
		if acct, err := p.Store.GetAccountByLabel(ctx, label); err == nil {
			if l, err := p.Store.GetLimits(ctx, acct.ID); err == nil {
				st.FiveHourPct = l.FiveHourPct
				st.SevenDayPct = l.SevenDayPct
				st.FiveHourResetAt = l.FiveHourResetAt
				st.SevenDayResetAt = l.SevenDayResetAt
				st.LimitsFetchedAt = l.FetchedAt
			} else if !errors.Is(err, store.ErrLimitsNotFound) {
				// Real I/O error (not just "no row yet"): surface
				// to stderr but keep publishing without limits.
				fmt.Fprintf(os.Stderr, "status: read limits for %q: %v\n", label, err)
			}
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = p.Store.PutSetting(ctx, store.SettingsKeyDaemonStatus, string(b))
}

// resolveActiveLabel is the default ActiveLabel resolver: byte-match the
// live keyring slot against known stashes. Mirrors the logic in
// AutoSwitcher.evaluateAndSwitch but tolerates errors silently — a
// blank label is acceptable for the widget; it just shows "—".
func resolveActiveLabel(s *Server) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		label, err := s.activeAccountLabel(ctx)
		if err != nil {
			return ""
		}
		return label
	}
}

// activeAccountLabel is the byte-match helper used by the StatusPublisher.
// Returns the empty string + nil when no stash matches (e.g. fresh install
// before `aimonitor add`).
func (s *Server) activeAccountLabel(ctx context.Context) (string, error) {
	live, err := s.provider.ActiveCredential(ctx)
	if err != nil {
		return "", fmt.Errorf("active credential: %w", err)
	}
	defer live.Zero()
	if len(live.Bytes) == 0 {
		return "", nil
	}
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("list accounts: %w", err)
	}
	for i := range accounts {
		stash, err := claude.RetrieveStash(ctx, accounts[i].KeyringRef)
		if err != nil {
			continue
		}
		match := bytes.Equal(stash.Bytes, live.Bytes)
		stash.Zero()
		if match {
			return accounts[i].Label, nil
		}
	}
	return "", nil
}
