package daemon

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

// Threshold-notification settings (SQLite settings table, like auto_swap.*).
const (
	SettingsKeyNotifyEnabled = "notify.enabled"
	SettingsKeyNotifyWarnPct = "notify.warn_pct"
	SettingsKeyNotifyCritPct = "notify.crit_pct"
)

// Defaults applied when the corresponding notify.* setting is unset.
const (
	DefaultNotifyEnabled = true
	DefaultNotifyWarnPct = 80.0
	DefaultNotifyCritPct = 95.0
)

// notifyKey identifies one (account, window) pair for hysteresis tracking.
type notifyKey struct {
	accountID int64
	window    windowKind
}

// notifyState is the highest level already notified for a window in its
// current reset epoch. resetAt is that window's reset time; when it advances,
// the window has rolled over and the level clears.
type notifyState struct {
	level   int // 0 none, 1 warn, 2 crit
	resetAt time.Time
}

// ThresholdNotifier posts a macOS notification when the active account first
// crosses the warn/crit utilization thresholds on either window. It's the
// heads-up for users running with auto-swap OFF: when auto-swap is ON its own
// arm/swap notifications already cover the same moment, so this stays silent
// to avoid double banners.
//
// Hysteresis is load-bearing: the scheduler re-evaluates every ~5 min, so
// without it a near-limit account would notify on every tick. Each window
// notifies at most once per level per reset epoch — the tracked level only
// clears when the window itself rolls over.
type ThresholdNotifier struct {
	Store  *store.Store
	Notify func(title, body string) // nil → notifyMacOS
	Now    func() time.Time         // nil → time.Now (kept for symmetry/tests)

	mu   sync.Mutex
	seen map[notifyKey]notifyState
}

// Evaluate checks the active account's latest limits and notifies on a fresh
// upward threshold crossing. Best-effort: any store miss is silently skipped.
func (n *ThresholdNotifier) Evaluate(ctx context.Context, activeLabel string) {
	if activeLabel == "" {
		return
	}
	enabled, warn, crit, autoSwap := n.config(ctx)
	// Disabled, or auto-swap owns the notifications for this moment.
	if !enabled || autoSwap {
		return
	}
	acct, err := n.Store.GetAccountByLabel(ctx, activeLabel)
	if err != nil {
		return
	}
	lim, err := n.Store.GetLimits(ctx, acct.ID)
	if err != nil {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[notifyKey]notifyState{}
	}
	n.evalWindow(acct, window5h, lim.FiveHourPct, lim.FiveHourResetAt, warn, crit)
	n.evalWindow(acct, window7d, lim.SevenDayPct, lim.SevenDayResetAt, warn, crit)
}

// evalWindow updates one window's hysteresis state and notifies on an upward
// level change. Caller holds n.mu.
func (n *ThresholdNotifier) evalWindow(acct store.Account, w windowKind, pct float64, resetAt time.Time, warn, crit float64) {
	key := notifyKey{accountID: acct.ID, window: w}
	st := n.seen[key]
	// New reset epoch (window rolled over) → start this window fresh.
	if !st.resetAt.Equal(resetAt) {
		st = notifyState{resetAt: resetAt}
	}

	level := 0
	switch {
	case pct >= crit:
		level = 2
	case pct >= warn:
		level = 1
	}

	if level > st.level {
		switch level {
		case 2:
			n.notify(fmt.Sprintf("%s at %.0f%%", acct.Label, pct),
				fmt.Sprintf("%s usage is critical (%.0f%%). Consider switching accounts.", w, pct))
		case 1:
			n.notify(fmt.Sprintf("%s at %.0f%%", acct.Label, pct),
				fmt.Sprintf("%s usage reached %.0f%%.", w, pct))
		}
		st.level = level
	}
	n.seen[key] = st
}

func (n *ThresholdNotifier) notify(title, body string) {
	if n.Notify != nil {
		n.Notify(title, body)
		return
	}
	notifyMacOS(title, body)
}

// config reads the notify.* thresholds plus auto_swap.enabled (which gates
// whether the threshold notifier speaks at all), each falling back to its
// default when unset or unparseable.
func (n *ThresholdNotifier) config(ctx context.Context) (enabled bool, warn, crit float64, autoSwap bool) {
	enabled, warn, crit, autoSwap = DefaultNotifyEnabled, DefaultNotifyWarnPct, DefaultNotifyCritPct, DefaultAutoSwapEnabled
	if v, err := n.Store.GetSetting(ctx, SettingsKeyNotifyEnabled); err == nil {
		if b, e := strconv.ParseBool(v); e == nil {
			enabled = b
		}
	}
	if v, err := n.Store.GetSetting(ctx, SettingsKeyNotifyWarnPct); err == nil {
		if f, e := strconv.ParseFloat(v, 64); e == nil {
			warn = f
		}
	}
	if v, err := n.Store.GetSetting(ctx, SettingsKeyNotifyCritPct); err == nil {
		if f, e := strconv.ParseFloat(v, 64); e == nil {
			crit = f
		}
	}
	if v, err := n.Store.GetSetting(ctx, SettingsKeyAutoSwapEnabled); err == nil {
		if b, e := strconv.ParseBool(v); e == nil {
			autoSwap = b
		}
	}
	return
}
