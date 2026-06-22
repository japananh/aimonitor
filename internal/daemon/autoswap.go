package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// Settings keys for the auto-swap engine. All are strings in the
// settings table; AutoSwapper parses them with sane fallbacks.
const (
	SettingsKeyAutoSwapEnabled = "auto_swap.enabled"
	// SettingsKeyAutoSwapThreshold is the 5-hour-window threshold. The key
	// name predates the 7-day trigger; it keeps its historical value so
	// existing installs don't lose their setting.
	SettingsKeyAutoSwapThreshold = "auto_swap.threshold_pct"
	// SettingsKeyAutoSwapThreshold7d is the 7-day-window threshold.
	SettingsKeyAutoSwapThreshold7d = "auto_swap.threshold_7d_pct"
	SettingsKeyAutoSwapGrace       = "auto_swap.grace_sec"
	// SettingsKeyAutoSwapExcluded lists account IDs (comma-separated) that
	// auto-swap must NOT switch to. Empty/unset means no exclusions — every
	// account is an eligible target, and accounts added later stay eligible by
	// default (a blocklist, so a new account never silently drops out). The UI
	// guarantees at least one account remains eligible.
	SettingsKeyAutoSwapExcluded = "auto_swap.excluded_account_ids"
)

// Defaults applied when the corresponding auto_swap.* setting is unset.
// Exported so the `aimonitor config` CLI shows the same values the daemon
// falls back to.
const (
	DefaultAutoSwapEnabled     = true
	DefaultAutoSwapThreshold5h = 80.0
	DefaultAutoSwapThreshold7d = 80.0
	DefaultAutoSwapGraceSec    = 60
)

const (
	// cooldownAfterSwap suppresses re-arming right after a swap so the
	// freshly-active account's limits can be re-fetched before we judge
	// it again. Mirrors claude-bar's 300s post-swap cooldown.
	cooldownAfterSwap = 5 * time.Minute
	// cooldownAfterExhausted backs off when every account is above
	// threshold — nothing to swap to, so don't recompute every tick.
	cooldownAfterExhausted = 10 * time.Minute
	// candidateFreshWindow is how recent a candidate's usage snapshot must
	// be to trust it for a swap decision. Generous enough that an account
	// the background round-robin just polled counts as fresh (so the
	// just-in-time refresh doesn't redundantly re-poll it).
	candidateFreshWindow = 15 * time.Minute
	// maxJITRefresh bounds how many stale candidates the just-in-time
	// refresh will poll per decision, so a swap decision can't fan out into
	// an unbounded burst of token/usage calls.
	maxJITRefresh = 5
	// exhaustedPct is the utilization at which an account is hard-blocked
	// by Anthropic. An account at/over it on either window can't serve
	// requests and is never a swap candidate.
	exhaustedPct = 100.0
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
// active account's 5-hour OR 7-day utilization rises to or above the
// configured threshold, it arms a swap to the non-active account with
// the most headroom (judged relative to the active account — see
// pickCandidate), notifies the user, and fires the swap once the grace
// window elapses.
//
// Distinct from the legacy AutoSwitcher (autoswitch.go) which is
// tripwire-driven by JSONL samples and fires probe-based decisions.
type AutoSwapper struct {
	Store    *store.Store
	Provider provider.Provider
	Switcher accountSwitcher

	// Stderr surfaces operational messages. Nil → the daemon log writer.
	Stderr io.Writer

	// Now is the clock, injectable for tests. Nil → time.Now.
	Now func() time.Time

	// Notify posts a user-facing notification (title, body). Nil → a
	// best-effort macOS notification via osascript (no-op off darwin).
	// Injectable so tests can capture without touching the OS.
	Notify func(title, body string)

	// RefreshUsage, when set, fetches fresh usage for a (non-active)
	// candidate at decision time — refreshing its token if expired — so the
	// swap picks an account whose headroom is actually known, not assumed.
	// Called only when the active account is at/over threshold and only for
	// candidates whose stored usage is stale/unknown. Nil disables the
	// just-in-time refresh (selection then falls back to last-known data).
	RefreshUsage func(ctx context.Context, acct store.Account) (provider.Limits, error)

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
	// Just-in-time candidate refresh (A) runs BEFORE the mutex so we never
	// hold a.mu across token/usage network I/O. It only fires when a swap is
	// actually imminent (auto-swap on AND active at/over threshold), and only
	// refreshes candidates whose stored usage is stale/unknown — so a sound
	// decision doesn't rest on optimistic "assume available" data.
	if a.shouldRefreshCandidates(ctx, activeLabel) {
		if active, err := a.Store.GetAccountByLabel(ctx, activeLabel); err == nil {
			a.refreshStaleCandidates(ctx, active.ID)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	enabled, threshold5h, threshold7d, graceSec, err := a.config(ctx)
	if err != nil {
		return false, fmt.Errorf("auto-swap: read config: %w", err)
	}
	if !enabled {
		a.pending = nil
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

	// activeExhausted: the active account has hit a hard limit (>=100%) on a
	// window. That's an emergency — `claude` can't run — so it overrides both
	// the post-swap cooldown and the grace window below: get off it NOW.
	activeExhausted := activeLim.FiveHourPct >= exhaustedPct || activeLim.SevenDayPct >= exhaustedPct

	// Post-swap cooldown normally suppresses re-evaluation (anti-ping-pong),
	// but never when the active account is exhausted.
	if a.now().Before(a.cooldownUntil) && !activeExhausted {
		return false, nil
	}
	// Active is back below threshold on BOTH windows (window reset, or the
	// user swapped manually): cancel any armed swap. Either window at/over
	// its threshold keeps a swap in play — a weekly-capped account is dead
	// for days even while its 5-hour window is quiet.
	if activeLim.FiveHourPct < threshold5h && activeLim.SevenDayPct < threshold7d {
		a.pending = nil
		return false, nil
	}
	// The binding window is the one driving this decision (the one furthest
	// over its own threshold). Candidates are judged against it.
	binding, activePct, threshold := bindingWindow(activeLim, threshold5h, threshold7d)

	cand, found, err := a.pickCandidate(ctx, activeAcct.ID, activeLim, binding, threshold5h, threshold7d)
	if err != nil {
		return false, fmt.Errorf("auto-swap: pick candidate: %w", err)
	}
	if !found {
		a.log().Info("auto-swap no candidate",
			"window", binding, "active", activeLabel, "pct", activePct)
		a.pending = nil
		a.cooldownUntil = a.now().Add(cooldownAfterExhausted)
		body := fmt.Sprintf("%q hit %.0f%% of its %s limit — no other account has headroom.", activeLabel, activePct, binding)
		// If the only account with headroom is one the user excluded, suggest
		// switching to it manually — auto-swap won't, since it's excluded.
		if exLabel, ok := a.excludedCandidateWithHeadroom(ctx, activeAcct.ID, activeLim, binding); ok {
			body = fmt.Sprintf("%q hit %.0f%% (%s) — switch to %q (excluded, has headroom).", activeLabel, activePct, binding, exLabel)
		}
		a.notify("No account to switch to", body)
		return false, nil
	}

	// Swap immediately when grace is disabled (grace_sec=0) OR the active
	// account is already exhausted (no point waiting out a grace window on an
	// account that can't serve requests).
	if graceSec <= 0 || activeExhausted {
		return a.fireSwap(ctx, activeLabel, activePct, binding, cand)
	}

	// Arm on first detection (or when the best candidate changed), notifying
	// once. Subsequent ticks reuse the same deadline.
	if a.pending == nil || a.pending.target != cand.Label {
		a.pending = &pendingSwap{target: cand.Label, deadline: a.now().Add(time.Duration(graceSec) * time.Second)}
		a.log().Info("auto-swap armed",
			"account", activeLabel, "pct", activePct, "window", binding,
			"threshold", threshold, "target", cand.Label, "in_sec", graceSec)
		a.notify(
			fmt.Sprintf("Auto-swap in %ds", graceSec),
			fmt.Sprintf("%q hit %.0f%% of its %s limit. Switching to %q — running sessions will follow automatically.", activeLabel, activePct, binding, cand.Label),
		)
		return false, nil
	}

	// Still waiting out the grace window.
	if a.now().Before(a.pending.deadline) {
		return false, nil
	}

	// Deadline reached — fire.
	return a.fireSwap(ctx, activeLabel, activePct, binding, cand)
}

// windowKind names the rate-limit window driving a swap decision.
type windowKind string

const (
	window5h windowKind = "5-hour"
	window7d windowKind = "7-day"
)

// bindingWindow returns the window that should drive the swap decision —
// the window over its own threshold, or when both are over, the one
// furthest past its threshold — plus the active account's utilization on
// it and that window's threshold. Callers must already have established
// that at least one window is at/over its threshold.
func bindingWindow(lim provider.Limits, threshold5h, threshold7d float64) (windowKind, float64, float64) {
	over5 := lim.FiveHourPct >= threshold5h
	over7 := lim.SevenDayPct >= threshold7d
	if over7 && (!over5 || lim.SevenDayPct-threshold7d >= lim.FiveHourPct-threshold5h) {
		return window7d, lim.SevenDayPct, threshold7d
	}
	return window5h, lim.FiveHourPct, threshold5h
}

// windowResetCrossed reports whether either rate-limit window's reset time
// has already passed at `now` — i.e. the window has rolled over since the
// snapshot was taken, so the stored percentage is stale-high and no longer
// valid (the "just reset to 0% but still reads ~100%" case). A reset time is
// always in the future when fetched, so `now.After(resetAt)` unambiguously
// means "rolled over since". A zero reset time means the provider didn't
// report it — we can't reason about it, so it doesn't count as crossed.
func windowResetCrossed(lim provider.Limits, now time.Time) bool {
	if !lim.FiveHourResetAt.IsZero() && now.After(lim.FiveHourResetAt) {
		return true
	}
	if !lim.SevenDayResetAt.IsZero() && now.After(lim.SevenDayResetAt) {
		return true
	}
	return false
}

// shouldRefreshCandidates reports whether a just-in-time candidate refresh
// is warranted: the hook is wired, auto-swap is enabled, and the active
// account is at/over threshold (a swap is imminent). Reads are unlocked and
// best-effort — it deliberately does not consult the cooldown (that's the
// locked section's job); at worst it refreshes once during a cooldown.
func (a *AutoSwapper) shouldRefreshCandidates(ctx context.Context, activeLabel string) bool {
	if a.RefreshUsage == nil || activeLabel == "" {
		return false
	}
	enabled, threshold5h, threshold7d, _, err := a.config(ctx)
	if err != nil || !enabled {
		return false
	}
	acct, err := a.Store.GetAccountByLabel(ctx, activeLabel)
	if err != nil {
		return false
	}
	lim, err := a.Store.GetLimits(ctx, acct.ID)
	if err != nil {
		return false
	}
	// Either window over its threshold makes a swap imminent.
	return lim.FiveHourPct >= threshold5h || lim.SevenDayPct >= threshold7d
}

// refreshStaleCandidates fetches fresh usage for up to maxJITRefresh
// non-active accounts whose stored snapshot is stale or missing, so the
// subsequent pickCandidate ranks on known headroom. Runs WITHOUT a.mu held
// (it does network I/O); each refresh serializes on the switch lock inside
// RefreshAccountUsage. Best-effort — failures are logged and skipped.
func (a *AutoSwapper) refreshStaleCandidates(ctx context.Context, activeID int64) {
	accounts, err := a.Store.ListAccounts(ctx)
	if err != nil {
		return
	}
	excluded := a.excludedTargets(ctx)
	done := 0
	for _, acct := range accounts {
		if acct.ID == activeID {
			continue
		}
		// Barred as a swap target (issue #13) or parked after a 429 —
		// pickCandidate will skip it either way, so don't spend a refresh on it.
		if excluded[acct.ID] {
			continue
		}
		if acct.CooldownUntil.After(a.now()) {
			continue
		}
		if lim, err := a.Store.GetLimits(ctx, acct.ID); err == nil &&
			a.now().Sub(lim.FetchedAt) <= candidateFreshWindow && !windowResetCrossed(lim, a.now()) {
			continue // fresh AND no window has reset since the snapshot — trust it
		}
		if _, err := a.RefreshUsage(ctx, acct); err != nil {
			recordThrottle(ctx, a.Store, acct, err)
			a.log().Warn("auto-swap refresh candidate failed", "account", acct.Label, "err", err)
		}
		done++
		if done >= maxJITRefresh {
			break
		}
	}
}

// fireSwap performs the switch, records the audit row, arms the
// post-swap cooldown, and clears the pending state. Caller holds a.mu.
func (a *AutoSwapper) fireSwap(ctx context.Context, activeLabel string, activePct float64, binding windowKind, cand candidate) (bool, error) {
	a.log().Info("auto-swap switching",
		"account", activeLabel, "pct", activePct, "window", binding,
		"target", cand.Label, "target_5h", cand.FiveHourPct, "target_7d", cand.SevenDayPct)
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
		Reason: fmt.Sprintf("active %q %s%%=%.1f over threshold; candidate %q 5h%%=%.1f 7d%%=%.1f",
			activeLabel, binding, activePct, cand.Label, cand.FiveHourPct, cand.SevenDayPct),
	})
	a.notify(fmt.Sprintf("Switched to %s", cand.Label), "Running `claude` sessions pick up the new account automatically.")
	return true, nil
}

// candidate is a non-active account ranked for selection.
type candidate struct {
	Label       string
	FiveHourPct float64
	SevenDayPct float64
	LastUsedMs  int64
}

// pct returns the candidate's utilization on the given window.
func (c candidate) pct(w windowKind) float64 {
	if w == window7d {
		return c.SevenDayPct
	}
	return c.FiveHourPct
}

// pickCandidate returns the best non-active account, judged RELATIVE to the
// active account rather than against the absolute threshold:
//
//	tier 1 — lower than the active on BOTH windows: the clean win. Ranked by
//	         most overall headroom (lowest max(5h, 7d)).
//	tier 2 — lower than the active on the BINDING window only, AND with real
//	         headroom (under its threshold) on the OTHER window. The windows
//	         aren't symmetric: a 5h-warm account self-heals within hours while a
//	         weekly-capped one is dead for days, so escaping a 7d-capped active
//	         into a 5h-warm-but-weekly-healthy account is a win — but only if its
//	         5h is still under threshold. Switching into one already OVER its
//	         other-window threshold (e.g. 5h=96 while escaping a 7d cap) trips
//	         that window at once and ping-pongs back, so it's excluded. The lone
//	         exception: when the active account is exhausted (>=100%) on the
//	         binding window it can't serve at all, so any non-exhausted candidate
//	         beats staying. Ranked by most overall headroom (lowest max(5h, 7d)).
//
// An account EXHAUSTED (>= 100%) on either window is never a candidate:
// it cannot serve requests, so "lower on the binding window" is meaningless.
// Without this, a 5h-capped active happily switched into a weekly-dead
// account and ping-ponged back minutes later — each bounce burning token
// refreshes until Anthropic throttled the lot (observed live 2026-06-04).
//
// Within a tier, ties break by least-recently-used so accounts rotate
// evenly. Accounts with stale/unknown usage are the last resort — we can't
// vouch for their headroom — ranked only by least-recently-used. (The
// just-in-time refresh normally promotes those to fresh before we get
// here; they remain only when a refresh wasn't possible, e.g. an expired
// refresh token.)
//
// Returns (_, false, nil) when no account beats the active on the binding
// window — switching would gain nothing.
func (a *AutoSwapper) pickCandidate(ctx context.Context, activeID int64, activeLim provider.Limits, binding windowKind, threshold5h, threshold7d float64) (candidate, bool, error) {
	accounts, err := a.Store.ListAccounts(ctx)
	if err != nil {
		return candidate{}, false, err
	}
	// Accounts the user barred from being swap targets (issue #13). Empty set
	// = nothing excluded. Excluded accounts are skipped exactly like the active
	// one — never considered, never ranked.
	excluded := a.excludedTargets(ctx)
	activeBindingPct := activeLim.FiveHourPct
	if binding == window7d {
		activeBindingPct = activeLim.SevenDayPct
	}
	// The window we're NOT escaping, and its threshold. A tier-2 candidate must
	// have real headroom here (be under this threshold), else switching into it
	// trips that window immediately and ping-pongs back.
	nonBinding, nonBindingThreshold := window7d, threshold7d
	if binding == window7d {
		nonBinding, nonBindingThreshold = window5h, threshold5h
	}
	// Exception: if the active account is exhausted (>=100%) on the binding
	// window it can't serve at all, so any non-exhausted candidate beats staying
	// — accept it even if it's over-threshold on the other window. (Safe from
	// ping-pong: the account we leave is then >=100%, so the >=100 check below
	// rules it out as a swap-back target.)
	activeBindingExhausted := activeBindingPct >= exhaustedPct
	now := a.now()
	var tier1, tier2, uncertain []candidate
	for _, acct := range accounts {
		if acct.ID == activeID {
			continue
		}
		// User barred this account from being a swap target (issue #13).
		if excluded[acct.ID] {
			continue
		}
		// Parked after a 429: it would just 429 again on use, so it's not a
		// real candidate however much headroom its stored snapshot shows.
		if acct.CooldownUntil.After(now) {
			continue
		}
		lim, err := a.Store.GetLimits(ctx, acct.ID)
		known := err == nil
		// A snapshot whose window has reset since FetchedAt reads a stale-high
		// pct (a just-reset account still showing ~100%). Treat it as not-fresh
		// so it drops to the uncertain (last-resort) tier instead of being
		// excluded as exhausted below. The JIT refresh above normally re-fetches
		// it to real post-reset headroom first; this is the fallback for when a
		// refresh wasn't possible (e.g. an expired refresh token).
		isFresh := known && now.Sub(lim.FetchedAt) <= candidateFreshWindow && !windowResetCrossed(lim, now)
		if !isFresh {
			uncertain = append(uncertain, candidate{
				Label:      acct.Label,
				LastUsedMs: acct.LastUsedAt.UnixMilli(),
			})
			continue
		}
		// Exhausted on either window → unusable, never a candidate.
		if lim.FiveHourPct >= exhaustedPct || lim.SevenDayPct >= exhaustedPct {
			continue
		}
		c := candidate{
			Label:       acct.Label,
			FiveHourPct: lim.FiveHourPct,
			SevenDayPct: lim.SevenDayPct,
			LastUsedMs:  acct.LastUsedAt.UnixMilli(),
		}
		switch {
		case c.FiveHourPct < activeLim.FiveHourPct && c.SevenDayPct < activeLim.SevenDayPct:
			tier1 = append(tier1, c)
		case c.pct(binding) < activeBindingPct && (activeBindingExhausted || c.pct(nonBinding) < nonBindingThreshold):
			tier2 = append(tier2, c)
		}
		// Not a candidate when: not lower on the binding window (switching gains
		// nothing on the constraint that fired), or — for tier 2 — over its
		// threshold on the OTHER window (it'd trip that one immediately and bounce
		// back, unless the active account is already exhausted on the binding one).
	}
	switch {
	case len(tier1) > 0:
		sort.Slice(tier1, func(i, j int) bool {
			mi := max(tier1[i].FiveHourPct, tier1[i].SevenDayPct)
			mj := max(tier1[j].FiveHourPct, tier1[j].SevenDayPct)
			if mi != mj {
				return mi < mj
			}
			return tier1[i].LastUsedMs < tier1[j].LastUsedMs
		})
		return tier1[0], true, nil
	case len(tier2) > 0:
		// Candidates here already have headroom on the non-binding window (the
		// acceptance check above), so rank by MOST overall headroom — lowest
		// max(5h, 7d) — to pick the most balanced, longest-usable target rather
		// than the lowest binding pct alone.
		sort.Slice(tier2, func(i, j int) bool {
			mi := max(tier2[i].FiveHourPct, tier2[i].SevenDayPct)
			mj := max(tier2[j].FiveHourPct, tier2[j].SevenDayPct)
			if mi != mj {
				return mi < mj
			}
			return tier2[i].LastUsedMs < tier2[j].LastUsedMs
		})
		return tier2[0], true, nil
	case len(uncertain) > 0:
		sort.Slice(uncertain, func(i, j int) bool {
			return uncertain[i].LastUsedMs < uncertain[j].LastUsedMs
		})
		return uncertain[0], true, nil
	default:
		return candidate{}, false, nil
	}
}

func (a *AutoSwapper) config(ctx context.Context) (enabled bool, threshold5h, threshold7d float64, graceSec int, err error) {
	enabled = DefaultAutoSwapEnabled
	threshold5h = DefaultAutoSwapThreshold5h
	threshold7d = DefaultAutoSwapThreshold7d
	graceSec = DefaultAutoSwapGraceSec

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapEnabled); getErr == nil {
		if b, perr := strconv.ParseBool(v); perr == nil {
			enabled = b
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold5h, threshold7d, graceSec, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapThreshold); getErr == nil {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil && f > 0 && f <= 100 {
			threshold5h = f
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold5h, threshold7d, graceSec, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapThreshold7d); getErr == nil {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil && f > 0 && f <= 100 {
			threshold7d = f
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold5h, threshold7d, graceSec, getErr
	}

	if v, getErr := a.Store.GetSetting(ctx, SettingsKeyAutoSwapGrace); getErr == nil {
		// >= 0; 0 means "swap immediately, no grace".
		if n, perr := strconv.Atoi(v); perr == nil && n >= 0 {
			graceSec = n
		}
	} else if !errors.Is(getErr, store.ErrSettingNotFound) {
		return enabled, threshold5h, threshold7d, graceSec, getErr
	}

	return enabled, threshold5h, threshold7d, graceSec, nil
}

// excludedTargets returns the set of account IDs the user has barred from
// being auto-swap targets (auto_swap.excluded_account_ids). An unset/empty
// setting — or any unparseable entry — yields an empty set, i.e. nothing
// excluded and every account eligible. Best-effort: a read error is logged and
// treated as "no exclusions" rather than blocking a swap decision (auto-swap
// is a safety feature; an unreadable preference must never strand it).
func (a *AutoSwapper) excludedTargets(ctx context.Context) map[int64]bool {
	excluded := map[int64]bool{}
	v, err := a.Store.GetSetting(ctx, SettingsKeyAutoSwapExcluded)
	if err != nil {
		if !errors.Is(err, store.ErrSettingNotFound) {
			a.log().Warn("auto-swap read excluded targets", "err", err)
		}
		return excluded
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, perr := strconv.ParseInt(part, 10, 64); perr == nil {
			excluded[id] = true
		}
	}
	return excluded
}

// excludedCandidateWithHeadroom finds an account the user excluded as a swap
// target (auto_swap.excluded_account_ids) that nonetheless WOULD be a viable
// target — fresh snapshot, not exhausted, and lower binding-window usage than
// the active account. It exists so the "no candidate" notification can be
// actionable: when the only account with headroom is one the user excluded, the
// exclusion (not exhaustion) is what's blocking the swap, and we can name it and
// point them at the toggle. Returns the lowest-usage such account, or ("", false).
func (a *AutoSwapper) excludedCandidateWithHeadroom(ctx context.Context, activeID int64, activeLim provider.Limits, binding windowKind) (string, bool) {
	excluded := a.excludedTargets(ctx)
	if len(excluded) == 0 {
		return "", false
	}
	accounts, err := a.Store.ListAccounts(ctx)
	if err != nil {
		return "", false
	}
	now := a.now()
	activeBindingPct := activeLim.FiveHourPct
	if binding == window7d {
		activeBindingPct = activeLim.SevenDayPct
	}
	best, bestPct := "", activeBindingPct // candidate must beat the active to count
	for _, acct := range accounts {
		if acct.ID == activeID || !excluded[acct.ID] {
			continue
		}
		if acct.CooldownUntil.After(now) {
			continue
		}
		lim, err := a.Store.GetLimits(ctx, acct.ID)
		if err != nil {
			continue
		}
		// Same freshness/exhaustion bar pickCandidate uses — don't vouch for a
		// stale, reset-crossed, or exhausted account's "headroom".
		if now.Sub(lim.FetchedAt) > candidateFreshWindow || windowResetCrossed(lim, now) {
			continue
		}
		if lim.FiveHourPct >= exhaustedPct || lim.SevenDayPct >= exhaustedPct {
			continue
		}
		p := lim.FiveHourPct
		if binding == window7d {
			p = lim.SevenDayPct
		}
		if p < bestPct {
			best, bestPct = acct.Label, p
		}
	}
	return best, best != ""
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

func (a *AutoSwapper) log() *slog.Logger {
	return loggerOver(a.Stderr)
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
