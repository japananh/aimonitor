package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// reloginNotify posts the "session expired" banner when an account first needs
// re-login. A package var so tests can stub it; the default shells out to
// Notification Center on darwin (no-op elsewhere).
var reloginNotify = notifyMacOS

// Per-account cooldown bounds. A 429 parks an account for the server's
// Retry-After when present; otherwise for cooldownDefault doubled per strike in
// the current streak. Always clamped so a missing or absurd header can neither
// leave it effectively un-parked nor sideline it for a day.
const (
	cooldownDefault = 15 * time.Minute
	cooldownMin     = 1 * time.Minute
	cooldownMax     = 1 * time.Hour
	// throttleStreakWindow: a 429 this long after the previous one starts a
	// new streak. Longer than cooldownMax so a success between two capped
	// cooldowns does not reset the escalation.
	throttleStreakWindow = 2 * cooldownMax
	// minRefreshAge is the youngest snapshot an on-demand refresh (CLI, popover,
	// widget) will re-fetch — the scheduler's default baseline, so no path
	// polls an account faster than the background schedule does.
	minRefreshAge  = 5 * time.Minute
	throttleReason = "rate-limited (429)"
)

// throttleBackoff is the no-Retry-After cooldown for the Nth strike:
// cooldownDefault · 2^(strikes-1), capped at cooldownMax.
func throttleBackoff(strikes int) time.Duration {
	d := cooldownDefault
	for i := 1; i < strikes && d < cooldownMax; i++ {
		d *= 2
	}
	return min(d, cooldownMax)
}

// recordThrottle parks acct after a 429. See recordThrottleAt.
func recordThrottle(ctx context.Context, st *store.Store, acct store.Account, err error) (time.Time, bool) {
	return recordThrottleAt(ctx, st, acct, err, time.Now())
}

// recordThrottleAt parks acct after a 429 and returns the cooldown deadline.
// The duration honors Retry-After, else grows with the streak. No-op for
// non-throttle errors. Best-effort: a store failure is logged, never
// propagated. Returns ok=false when no cooldown was set.
func recordThrottleAt(ctx context.Context, st *store.Store, acct store.Account, err error, now time.Time) (time.Time, bool) {
	if !claude.IsThrottledError(err) {
		return time.Time{}, false
	}
	// Re-read: another process (CLI refresh vs daemon) may have extended the
	// streak since acct was loaded.
	cur, gerr := st.GetAccountByID(ctx, acct.ID)
	if gerr != nil {
		cur = acct
	}
	strikes := 1
	if !cur.ThrottledAt.IsZero() && now.Sub(cur.ThrottledAt) < throttleStreakWindow {
		strikes = cur.ThrottleStrikes + 1
	}
	dur := throttleBackoff(strikes)
	if ra, ok := claude.ThrottleRetryAfter(err); ok {
		dur = ra
	}
	dur = clampDuration(dur, cooldownMin, cooldownMax)
	until := now.Add(dur)
	if serr := st.SetThrottle(ctx, acct.ID, until, throttleReason, strikes, now); serr != nil {
		logger.Warn("set cooldown failed", "account", acct.Label, "err", serr)
		return time.Time{}, false
	}
	logger.Warn("account parked after 429", "account", acct.Label, "strike", strikes, "for", dur, "until", until.Format(time.RFC3339))
	return until, true
}

// SkipReason says why a usage fetch was withheld.
type SkipReason string

const (
	// SkipRateLimited: the account is in its post-429 cooldown.
	SkipRateLimited SkipReason = "rate-limited"
	// SkipAtLimit: a window is at 100% and has not reset yet, so a fetch can
	// only return the same number.
	SkipAtLimit SkipReason = "at-limit"
	// SkipFresh: the stored snapshot is younger than the minimum refresh age.
	SkipFresh SkipReason = "fresh"
)

// RefreshSkippedError reports a usage fetch withheld without any network
// call. Cached is the stored snapshot (HasCached=false when there is none).
type RefreshSkippedError struct {
	Label     string
	Reason    SkipReason
	Until     time.Time
	Cached    provider.Limits
	HasCached bool
}

func (e *RefreshSkippedError) Error() string {
	at := e.Until.Local().Format("15:04")
	switch e.Reason {
	case SkipRateLimited:
		return fmt.Sprintf("%q is rate-limited by Anthropic until %s; not re-fetched", e.Label, at)
	case SkipAtLimit:
		return fmt.Sprintf("%q is at its usage limit until %s; not re-fetched", e.Label, e.Until.Local().Format("Jan 2 15:04"))
	default:
		return fmt.Sprintf("%q was fetched under %s ago; not re-fetched", e.Label, minRefreshAge)
	}
}

// exhaustedUntil returns when acct's usage can next change: the latest reset
// among windows at/over exhaustedPct whose reset is still ahead. Zero when no
// window is exhausted, or its reset time is unknown or already passed.
func exhaustedUntil(lim provider.Limits, now time.Time) time.Time {
	var until time.Time
	if lim.FiveHourPct >= exhaustedPct && lim.FiveHourResetAt.After(now) {
		until = lim.FiveHourResetAt
	}
	if lim.SevenDayPct >= exhaustedPct && lim.SevenDayResetAt.After(now) && lim.SevenDayResetAt.After(until) {
		until = lim.SevenDayResetAt
	}
	return until
}

// fetchGate decides, from stored state only, whether acct's usage may be
// fetched at now. Every path that calls Anthropic for usage (scheduler,
// auto-swap candidate refresh, CLI/UI refresh) goes through it before any
// token refresh or HTTP. minAge=0 disables the freshness check. Returns nil
// when the fetch may proceed.
func fetchGate(ctx context.Context, st *store.Store, acct store.Account, now time.Time, minAge time.Duration) *RefreshSkippedError {
	cur, err := st.GetAccountByID(ctx, acct.ID)
	if err != nil {
		cur = acct
	}
	lim, lerr := st.GetLimits(ctx, acct.ID)
	has := lerr == nil
	skip := func(r SkipReason, until time.Time) *RefreshSkippedError {
		return &RefreshSkippedError{Label: acct.Label, Reason: r, Until: until, Cached: lim, HasCached: has}
	}
	if has {
		if until := exhaustedUntil(lim, now); !until.IsZero() {
			return skip(SkipAtLimit, until)
		}
	}
	if cur.CooldownUntil.After(now) {
		return skip(SkipRateLimited, cur.CooldownUntil)
	}
	if has && minAge > 0 && now.Sub(lim.FetchedAt) < minAge && !windowResetCrossed(lim, now) {
		return skip(SkipFresh, lim.FetchedAt.Add(minAge))
	}
	return nil
}

// clearThrottle lifts any cooldown on acct after a successful fetch.
// Best-effort and cheap (the UPDATE only touches a currently-cooling row).
func clearThrottle(ctx context.Context, st *store.Store, acct store.Account) {
	if err := st.ClearCooldown(ctx, acct.ID); err != nil {
		logger.Warn("clear cooldown failed", "account", acct.Label, "err", err)
	}
}

// markRelogin flags acct as needing re-login when err means its OAuth refresh
// token is dead, or clears the flag when err is nil (a successful refresh).
// Other errors (network, 429) leave the flag untouched. Best-effort: a store
// miss is logged, never propagated. Every CLI and daemon refresh runs through
// the usage_refresh helpers + the scheduler, so calling this there surfaces
// the "Session expired" badge wherever a refresh is attempted.
func markRelogin(ctx context.Context, st *store.Store, acct store.Account, err error) {
	switch {
	case err == nil:
		if e := st.SetNeedsRelogin(ctx, acct.ID, false); e != nil {
			logger.Warn("clear needs_relogin failed", "account", acct.Label, "err", e)
		}
	case claude.RequiresRelogin(err):
		if e := st.SetNeedsRelogin(ctx, acct.ID, true); e != nil {
			logger.Warn("set needs_relogin failed", "account", acct.Label, "err", e)
			return
		}
		logger.Warn("account needs re-login", "account", acct.Label, "err", err)
		// Notify once, on the false→true transition. acct reflects the flag as
		// loaded this cycle; once it's true the account is skipped on later
		// cycles, so this fires a single banner per expiry, not one per poll.
		if !acct.NeedsRelogin {
			reloginNotify("Claude session expired",
				fmt.Sprintf("%s can't sign in — open aimonitor and click Re-login.", acct.Label))
		}
	}
}
