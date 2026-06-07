package daemon

import (
	"context"
	"time"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Per-account cooldown bounds. A 429 parks an account for the server's
// Retry-After when present, otherwise a conservative default — always clamped
// so a missing or absurd header can neither leave it effectively un-parked nor
// sideline an account for a day.
const (
	cooldownDefault = 15 * time.Minute
	cooldownMin     = 1 * time.Minute
	cooldownMax     = 1 * time.Hour
)

// recordThrottle parks acct after a 429, honoring its Retry-After (clamped to
// [cooldownMin, cooldownMax]). No-op for non-throttle errors. Best-effort: a
// store failure is logged, never propagated — cooldown is an optimization, not
// a correctness requirement. Returns true when a cooldown was set.
func recordThrottle(ctx context.Context, st *store.Store, acct store.Account, err error) bool {
	if !claude.IsThrottledError(err) {
		return false
	}
	dur := cooldownDefault
	if ra, ok := claude.ThrottleRetryAfter(err); ok {
		dur = ra
	}
	dur = clampDuration(dur, cooldownMin, cooldownMax)
	until := time.Now().Add(dur)
	if serr := st.SetCooldown(ctx, acct.ID, until, "rate-limited (429)"); serr != nil {
		logger.Warn("set cooldown failed", "account", acct.Label, "err", serr)
		return false
	}
	logger.Warn("account parked after 429", "account", acct.Label, "for", dur, "until", until.Format(time.RFC3339))
	return true
}

// clearThrottle lifts any cooldown on acct after a successful fetch.
// Best-effort and cheap (the UPDATE only touches a currently-cooling row).
func clearThrottle(ctx context.Context, st *store.Store, acct store.Account) {
	if err := st.ClearCooldown(ctx, acct.ID); err != nil {
		logger.Warn("clear cooldown failed", "account", acct.Label, "err", err)
	}
}
