package daemon

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

// Daily-summary settings (SQLite settings table, like notify.* / auto_swap.*).
const (
	SettingsKeyDailySummaryEnabled = "daily_summary.enabled"
	// SettingsKeyDailySummaryLast holds the local date (YYYY-MM-DD) of the
	// most recent day already summarized — so we notify at most once per day
	// and don't repeat after a daemon restart.
	SettingsKeyDailySummaryLast = "daily_summary.last"
)

// DefaultDailySummaryEnabled is applied when the setting is unset.
const DefaultDailySummaryEnabled = true

// dailySummaryInterval is how often the notifier checks whether the local day
// has rolled over. Coarse on purpose: the recap just needs to land sometime
// after midnight, not on the second.
const dailySummaryInterval = 10 * time.Minute

// DailySummaryNotifier posts one OS notification per local day recapping the
// PREVIOUS day's token usage across all accounts (from usage_samples). The
// daily total is only meaningful once the day is complete, so it summarizes
// yesterday, fired the first time the daemon checks on a new local day.
type DailySummaryNotifier struct {
	Store    *store.Store
	Notify   func(title, body string) // nil → notifyMacOS
	Now      func() time.Time         // nil → time.Now
	Interval time.Duration            // 0 → dailySummaryInterval
}

func (d *DailySummaryNotifier) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DailySummaryNotifier) notify(title, body string) {
	if d.Notify != nil {
		d.Notify(title, body)
		return
	}
	notifyMacOS(title, body)
}

// Run does an initial catch-up Evaluate (so a same-day restart after midnight
// still sends yesterday's recap), then re-checks on a ticker until ctx is
// cancelled.
func (d *DailySummaryNotifier) Run(ctx context.Context) error {
	d.Evaluate(ctx)
	iv := d.Interval
	if iv <= 0 {
		iv = dailySummaryInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			d.Evaluate(ctx)
		}
	}
}

// Evaluate sends yesterday's summary once, when enabled and not already sent.
// Best-effort: store misses are silently skipped — a dropped recap is
// invisible, never worth failing the daemon.
func (d *DailySummaryNotifier) Evaluate(ctx context.Context) {
	if !d.enabled(ctx) {
		return
	}
	now := d.now()
	y, m, day := now.Date()
	todayStart := time.Date(y, m, day, 0, 0, 0, 0, now.Location())
	yStart := todayStart.AddDate(0, 0, -1)
	yKey := yStart.Format("2006-01-02")

	if last, err := d.Store.GetSetting(ctx, SettingsKeyDailySummaryLast); err == nil && last == yKey {
		return // already summarized yesterday
	}

	rows, err := d.Store.TokenUsageByDay(ctx, 0, yStart, todayStart)
	if err != nil {
		return
	}
	// Mark yesterday handled regardless of usage, so a zero-usage day doesn't
	// make us re-query every tick (and we never notify "0 tokens").
	_ = d.Store.PutSetting(ctx, SettingsKeyDailySummaryLast, yKey)

	var total, topTotal, topID int64
	for _, r := range rows {
		total += r.Total
		if r.Total > topTotal {
			topTotal, topID = r.Total, r.AccountID
		}
	}
	if total <= 0 {
		return
	}

	accounts := len(rows) // one row per account for a single day
	body := fmt.Sprintf("%s tokens across %d account%s", compactTokensInt(total), accounts, plural(accounts))
	if accounts > 1 {
		if label := d.accountLabel(ctx, topID); label != "" {
			body += fmt.Sprintf(" — top: %s (%s)", label, compactTokensInt(topTotal))
		}
	}
	d.notify(fmt.Sprintf("AIMonitor — %s usage", yStart.Format("Jan 2")), body+".")
}

func (d *DailySummaryNotifier) accountLabel(ctx context.Context, id int64) string {
	if id == 0 {
		return ""
	}
	acct, err := d.Store.GetAccountByID(ctx, id)
	if err != nil {
		return ""
	}
	return acct.Label
}

func (d *DailySummaryNotifier) enabled(ctx context.Context) bool {
	v, err := d.Store.GetSetting(ctx, SettingsKeyDailySummaryEnabled)
	if err != nil {
		return DefaultDailySummaryEnabled
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return DefaultDailySummaryEnabled
	}
	return b
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// compactTokensInt renders a token count compactly (742, 1.2K, 12.3M, 3.4B).
// Local to the daemon — the CLI has its own copy; sharing a one-liner would
// couple the packages for no real gain.
func compactTokensInt(n int64) string {
	f := float64(n)
	switch {
	case n < 1_000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", f/1_000)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", f/1_000_000)
	default:
		return fmt.Sprintf("%.1fB", f/1_000_000_000)
	}
}
