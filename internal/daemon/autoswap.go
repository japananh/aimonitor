package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// Settings keys for the auto-swap engine. All are strings in the
// settings table; AutoSwapper parses them with sane fallbacks.
const (
	SettingsKeyAutoSwapEnabled   = "auto_swap.enabled"
	SettingsKeyAutoSwapThreshold = "auto_swap.threshold_pct"
	SettingsKeyAutoSwapGrace     = "auto_swap.grace_sec"
)

// Defaults applied when the corresponding auto_swap.* setting is unset.
// Exported so the `aimonitor config` CLI shows the same values the daemon
// falls back to.
const (
	DefaultAutoSwapEnabled   = true
	DefaultAutoSwapThreshold = 80.0
	DefaultAutoSwapGraceSec  = 60
)

const (
	// cooldownAfterSwap suppresses re-arming right after a swap so the
	// freshly-active account's limits can be re-fetched before we judge
	// it again. Mirrors claude-bar's 300s post-swap cooldown.
	cooldownAfterSwap = 5 * time.Minute
	// cooldownAfterExhausted backs off when every account is above
	// threshold — nothing to swap to, so don't recompute every tick.
	cooldownAfterExhausted = 10 * time.Minute
)

// accountSwitcher is the narrow surface AutoSwapper needs from
// daemon.Switcher. Defining it here (the consumer) instead of on
// *Switcher itself keeps the production type concrete and gives
// tests a small interface to fake — strictly tighter than passing
// the full *Switcher around.
type accountSwitcher interface {
	Switch(ctx context.Context, label string) error
}

// pendingSwap is an armed-but-not-yet-fired swap. The grace window
// between arming and firing gives the user a heads-up (notification)
// and a chance to wrap up a live `claude` session before the account
// binding flips.
type pendingSwap struct {
	target   string
	deadline time.Time
}

// AutoSwapper is the Limits-driven account-rotation engine. When the
// active account's 5-hour utilization rises to or above the configured
// threshold, it arms a swap to the least-utilized non-active account,
// notifies the user, and fires the swap once the grace window elapses.
//
// Distinct from the legacy AutoSwitcher (autoswitch.go) which is
// tripwire-driven by JSONL samples and fires probe-based decisions.
type AutoSwapper struct {
	Store    *store.Store
	Provider provider.Provider
	Switcher accountSwitcher

	// Stderr surfaces operational messages. Nil → os.Stderr.
	Stderr io.Writer

	// Now is the clock, injectable for tests. Nil → time.Now.
	Now func() time.Time

	// Notify posts a user-facing notification (title, body). Nil → a
	// best-effort macOS notification via osascript (no-op off darwin).
	// Injectable so tests can capture without touching the OS.
	Notify func(title, body string)

	mu            sync.Mutex
	pending       *pendingSwap
	cooldownUntil time.Time
}

// MaybeSwap is invoked by the UsageScheduler after every successful
// fetch. Returns (true, nil) only when a swap actually fired this call;
// (false, nil) when below threshold, still inside the grace window,
// cooling down, or no candidate has headroom; (false, err) on failure.
//
// The decision runs under a mutex so back-to-back ticks (e.g. after a
// wake-from-sleep burst) can't arm or fire two swaps in parallel.
func (a *AutoSwapper) MaybeSwap(ctx context.Context, activeLabel string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	enabled, threshold, graceSec, err := a.config(ctx)
	if err != nil {
		return false, fmt.Errorf("auto-swap: read config: %w", err)
	}
	if !enabled {
		a.pending = nil
		return false, nil
	}
	if a.now().Before(a.cooldownUntil) {
		return false, nil
	}
	if activeLabel == "" {
		return false, nil
	}

	activeAcct, err := a.Store.GetAccountByLabel(ctx, activeLabel)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			return false, nil
		}
		return false, err
	}
	activeLim, err := a.Store.GetLimits(ctx, activeAcct.ID)
	if err != nil {
		if errors.Is(err, store.ErrLimitsNotFound) {
			return false, nil // no data on the active account yet
		}
		return false, err
	}
	// Active dropped back below threshold (window reset, or the user swapped
	// manually): cancel any armed swap.
	if activeLim.FiveHourPct < threshold {
		a.pending = nil
		return false, nil
	}

	cand, found, err := a.pickCandidate(ctx, activeAcct.ID, threshold)
	if err != nil {
		return false, fmt.Errorf("auto-swap: pick candidate: %w", err)
	}
	if !found {
		fmt.Fprintf(a.stderr(), "auto-swap: no candidate below %.0f%% — all accounts near limit (active=%q at %.1f%%)\n",
			threshold, activeLabel, activeLim.FiveHourPct)
		a.pending = nil
		a.cooldownUntil = a.now().Add(cooldownAfterExhausted)
		a.notify("All accounts near limit", fmt.Sprintf("%q is at %.0f%% and no account has headroom to swap to.", activeLabel, activeLim.FiveHourPct))
		return false, nil
	}

	// Grace disabled (grace_sec=0): swap immediately.
	if graceSec <= 0 {
		return a.fireSwap(ctx, activeLabel, activeLim.FiveHourPct, cand, threshold)
	}

	// Arm on first detection (or when the best candidate changed), notifying
	// once. Subsequent ticks reuse the same deadline.
	if a.pending == nil || a.pending.target != cand.Label {
		a.pending = &pendingSwap{target: cand.Label, deadline: a.now().Add(time.Duration(graceSec) * time.Second)}
		fmt.Fprintf(a.stderr(), "auto-swap: armed — %q at %.1f%% (>= %.0f%%), will switch to %q in %ds\n",
			activeLabel, activeLim.FiveHourPct, threshold, cand.Label, graceSec)
		a.notify(
			fmt.Sprintf("Auto-swap in %ds", graceSec),
			fmt.Sprintf("%q hit %.0f%%. Switching to %q. Close `claude` now if you want it to finish cleanly.", activeLabel, activeLim.FiveHourPct, cand.Label),
		)
		return false, nil
	}

	// Still waiting out the grace window.
	if a.now().Before(a.pending.deadline) {
		return false, nil
	}

	// Deadline reached — fire.
	return a.fireSwap(ctx, activeLabel, activeLim.FiveHourPct, cand, threshold)
}

// fireSwap performs the switch, records the audit row, arms the
// post-swap cooldown, and clears the pending state. Caller holds a.mu.
func (a *AutoSwapper) fireSwap(ctx context.Context, activeLabel string, activePct float64, cand candidate, threshold float64) (bool, error) {
	fmt.Fprintf(a.stderr(), "auto-swap: %q at %.1f%% (>= %.0f%%), switching to %q (5h %.1f%%)\n",
		activeLabel, activePct, threshold, cand.Label, cand.FiveHourPct)
	if err := a.Switcher.Switch(ctx, cand.Label); err != nil {
		return false, fmt.Errorf("auto-swap: switch to %q: %w", cand.Label, err)
	}
	a.pending = nil
	a.cooldownUntil = a.now().Add(cooldownAfterSwap)
	_ = a.Store.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts:        a.now(),
		FromLabel: activeLabel,
		ToLabel:   cand.Label,
		Trigger:   store.TriggerAutoswitch,
		Reason: fmt.Sprintf("active %q 5h%%=%.1f >= threshold %.0f; candidate %q 5h%%=%.1f",
			activeLabel, activePct, threshold, cand.Label, cand.FiveHourPct),
	})
	a.notify(fmt.Sprintf("Switched to %s", cand.Label), "Restart `claude` to use the new account.")
	return true, nil
}

// candidate is a non-active account ranked for selection.
type candidate struct {
	Label       string
	FiveHourPct float64
	SevenDayPct float64
	LastUsedMs  int64
}

// pickCandidate returns the best non-active account whose 5-hour
// utilization is below threshold. Lowest 5h% wins; ties break by
// lowest 7d%; further ties break by least-recently-used so accounts
// rotate evenly.
//
// Returns (_, false, nil) when no eligible account exists.
func (a *AutoSwapper) pickCandidate(ctx context.Context, activeID int64, threshold float64) (candidate, bool, error) {
	accounts, err := a.Store.ListAccounts(ctx)
	if err != nil {
		return candidate{}, false, err
	}
	var pool []candidate
	for _, acct := range accounts {
		if acct.ID == activeID {
			continue
		}
		lim, err := a.Store.GetLimits(ctx, acct.ID)
		if err != nil {
			// Account with no fetched limits is treated as fully
			// available — the worst case is one fetch on the new
			// active account in the next tick.
			lim = provider.Limits{}
		}
		if lim.FiveHourPct >= threshold {
			continue
		}
		pool = append(pool, candidate{
			Label:       acct.Label,
			FiveHourPct: lim.FiveHourPct,
			SevenDayPct: lim.SevenDayPct,
			LastUsedMs:  acct.LastUsedAt.UnixMilli(),
		})
	}
	if len(pool) == 0 {
		return candidate{}, false, nil
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].FiveHourPct != pool[j].FiveHourPct {
			return pool[i].FiveHourPct < pool[j].FiveHourPct
		}
		if pool[i].SevenDayPct != pool[j].SevenDayPct {
			return pool[i].SevenDayPct < pool[j].SevenDayPct
		}
		return pool[i].LastUsedMs < pool[j].LastUsedMs
	})
	return pool[0], true, nil
}

func (a *AutoSwapper) config(ctx context.Context) (enabled bool, threshold float64, graceSec int, err error) {
	enabled = DefaultAutoSwapEnabled
	threshold = DefaultAutoSwapThreshold
	graceSec = DefaultAutoSwapGraceSec

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapEnabled); getErr == nil {
		if b, perr := strconv.ParseBool(v); perr == nil {
			enabled = b
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold, graceSec, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapThreshold); getErr == nil {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil && f > 0 && f <= 100 {
			threshold = f
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold, graceSec, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapGrace); getErr == nil {
		// >= 0; 0 means "swap immediately, no grace".
		if n, perr := strconv.Atoi(v); perr == nil && n >= 0 {
			graceSec = n
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold, graceSec, getErr
	}

	return enabled, threshold, graceSec, nil
}

func (a *AutoSwapper) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *AutoSwapper) notify(title, body string) {
	if a.Notify != nil {
		a.Notify(title, body)
		return
	}
	notifyMacOS(title, body)
}

func (a *AutoSwapper) stderr() io.Writer {
	if a.Stderr != nil {
		return a.Stderr
	}
	return os.Stderr
}

// notifyMacOS posts a Notification Center banner via osascript. Best
// effort: any failure (osascript missing, notifications disabled) is
// swallowed — a notification is a courtesy, never load-bearing. No-op
// off darwin.
func notifyMacOS(title, body string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// osascript args are passed as a single -e script. Both fields are
	// our own constant-ish strings (account labels / percentages), but
	// quote-escape defensively so a label with a double quote can't
	// break the script.
	script := fmt.Sprintf("display notification %q with title %q", body, title)
	_ = exec.Command("osascript", "-e", script).Run()
}
