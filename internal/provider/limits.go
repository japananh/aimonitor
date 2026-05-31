package provider

import "time"

// Limits is the OAuth-introspected view of an account's current usage of
// its rate-limit windows. Distinct from Usage (which is aimonitor's local
// JSONL-derived per-machine estimate): Limits is the provider's
// authoritative server-side number, covering all devices the account is
// signed in on.
//
// For Claude this is fetched from /api/oauth/usage. Future providers will
// have analogous endpoints — keep the shape narrow enough that whatever
// shows up later can fit without breaking the SQLite schema or the UI.
type Limits struct {
	AccountID int64

	// FiveHourPct is the percentage (0..100) of the rolling 5-hour token
	// window consumed at FetchedAt. FiveHourResetAt is when the window
	// fully resets (zero value means the provider did not report it).
	FiveHourPct     float64
	FiveHourResetAt time.Time

	// SevenDayPct / SevenDayResetAt mirror the above for the rolling 7-day
	// window. Anthropic's Claude exposes both; if a future provider only
	// has one, leave the other zero and the UI hides the bar.
	SevenDayPct     float64
	SevenDayResetAt time.Time

	// Source identifies how this snapshot was obtained: "oauth" for the
	// /api/oauth/usage endpoint, "web" for a scrape of claude.ai. Useful
	// in the audit log and for the cross-check that compares the two
	// sources without flapping the displayed bars.
	Source string

	// FetchedAt is the wall-clock at which the snapshot was taken. The
	// daemon uses this to decide when the next scheduled fetch is due.
	FetchedAt time.Time
}
