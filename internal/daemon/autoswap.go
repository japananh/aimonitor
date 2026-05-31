package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// Settings keys for the auto-swap toggle and threshold. Both are
// strings in the settings table; AutoSwapper parses them.
const (
	SettingsKeyAutoSwapEnabled   = "auto_swap.enabled"
	SettingsKeyAutoSwapThreshold = "auto_swap.threshold_pct"
)

const (
	defaultAutoSwapEnabled   = true
	defaultAutoSwapThreshold = 80.0
)

// accountSwitcher is the narrow surface AutoSwapper needs from
// daemon.Switcher. Defining it here (the consumer) instead of on
// *Switcher itself keeps the production type concrete and gives
// tests a small interface to fake — strictly tighter than passing
// the full *Switcher around.
type accountSwitcher interface {
	Switch(ctx context.Context, label string) error
}

// AutoSwapper is the Limits-driven account-rotation engine. When the
// active account's 5-hour utilization rises to or above the configured
// threshold, it picks the least-utilized non-active account and
// triggers a silent swap via Switcher.
//
// Distinct from the legacy AutoSwitcher (autoswitch.go) which is
// tripwire-driven by JSONL samples and fires probe-based decisions.
// AutoSwapper is the replacement; AutoSwitcher will be deleted in
// a subsequent commit once probe.go is retired.
type AutoSwapper struct {
	Store    *store.Store
	Provider provider.Provider
	Switcher accountSwitcher

	// Stderr surfaces operational messages ("auto-swap: A -> B because
	// A hit 80%"). Nil → os.Stderr.
	Stderr io.Writer

	mu sync.Mutex
}

// MaybeSwap is invoked by the UsageScheduler after every successful
// fetch. Returns (true, nil) when a swap actually happened, (false, nil)
// when the active account is below threshold or no candidate has
// headroom, or (false, err) on a real failure.
//
// The decision happens under a mutex so two concurrent ticks (e.g.
// after a wake-from-sleep that delivers multiple ticks immediately)
// can't trigger two swaps in parallel.
func (a *AutoSwapper) MaybeSwap(ctx context.Context, activeLabel string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	enabled, threshold, err := a.config(ctx)
	if err != nil {
		return false, fmt.Errorf("auto-swap: read config: %w", err)
	}
	if !enabled {
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
			// No data on the active account yet — nothing to decide on.
			return false, nil
		}
		return false, err
	}
	if activeLim.FiveHourPct < threshold {
		return false, nil
	}

	candidate, found, err := a.pickCandidate(ctx, activeAcct.ID, threshold)
	if err != nil {
		return false, fmt.Errorf("auto-swap: pick candidate: %w", err)
	}
	if !found {
		fmt.Fprintf(a.stderr(), "auto-swap: no candidate below %.0f%% — all accounts near limit (active=%q at %.1f%%)\n",
			threshold, activeLabel, activeLim.FiveHourPct)
		return false, nil
	}

	fmt.Fprintf(a.stderr(), "auto-swap: %q hit %.1f%% (>= %.0f%%), switching to %q (5h %.1f%%)\n",
		activeLabel, activeLim.FiveHourPct, threshold, candidate.Label, candidate.FiveHourPct)
	if err := a.Switcher.Switch(ctx, candidate.Label); err != nil {
		return false, fmt.Errorf("auto-swap: switch to %q: %w", candidate.Label, err)
	}
	_ = a.Store.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts:        time.Now(),
		FromLabel: activeLabel,
		ToLabel:   candidate.Label,
		Trigger:   store.TriggerAutoswitch,
		Reason: fmt.Sprintf("active %q 5h%%=%.1f >= threshold %.0f; candidate %q 5h%%=%.1f",
			activeLabel, activeLim.FiveHourPct, threshold, candidate.Label, candidate.FiveHourPct),
	})
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

func (a *AutoSwapper) config(ctx context.Context) (enabled bool, threshold float64, err error) {
	enabled = defaultAutoSwapEnabled
	threshold = defaultAutoSwapThreshold

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapEnabled); getErr == nil {
		if b, perr := strconv.ParseBool(v); perr == nil {
			enabled = b
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapThreshold); getErr == nil {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil && f > 0 && f <= 100 {
			threshold = f
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold, getErr
	}
	return enabled, threshold, nil
}

func (a *AutoSwapper) stderr() io.Writer {
	if a.Stderr != nil {
		return a.Stderr
	}
	return os.Stderr
}
