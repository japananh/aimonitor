package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

// summaryFixture returns a store with one account and a notifier whose clock
// is pinned to 2026-06-17 10:00 local (so "yesterday" is 2026-06-16). The
// returned pointer captures the last notification, if any.
func summaryFixture(t *testing.T) (*store.Store, int64, *DailySummaryNotifier, *[]string) {
	t.Helper()
	s := openStore(t)
	a, err := s.CreateAccount(context.Background(), store.Account{Label: "work", KeyringRef: "r-work"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	var notes []string
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.Local)
	d := &DailySummaryNotifier{
		Store:  s,
		Now:    func() time.Time { return now },
		Notify: func(title, body string) { notes = append(notes, title+" | "+body) },
	}
	return s, a.ID, d, &notes
}

func seedYesterday(t *testing.T, s *store.Store, acct int64, id string, total int64) {
	t.Helper()
	// 2026-06-16 12:00 local is squarely inside "yesterday".
	ts := time.Date(2026, 6, 16, 12, 0, 0, 0, time.Local)
	if _, err := s.InsertUsageSample(context.Background(), acct, store.TokenSample{
		Ts: ts, MessageID: id, RequestID: id, Input: total,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestDailySummary_NotifiesYesterday(t *testing.T) {
	s, acct, d, notes := summaryFixture(t)
	ctx := context.Background()
	seedYesterday(t, s, acct, "m1", 1_500_000)

	d.Evaluate(ctx)

	if len(*notes) != 1 {
		t.Fatalf("got %d notifications, want 1: %v", len(*notes), *notes)
	}
	msg := (*notes)[0]
	for _, want := range []string{"Jun 16", "1.5M tokens", "1 account"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notification %q missing %q", msg, want)
		}
	}
	// last-summarized marker set so a second Evaluate is a no-op.
	if v, _ := s.GetSetting(ctx, SettingsKeyDailySummaryLast); v != "2026-06-16" {
		t.Errorf("last = %q, want 2026-06-16", v)
	}
	d.Evaluate(ctx)
	if len(*notes) != 1 {
		t.Errorf("second Evaluate re-notified: %v", *notes)
	}
}

func TestDailySummary_TopAccountWhenMultiple(t *testing.T) {
	s, acct, d, notes := summaryFixture(t)
	ctx := context.Background()
	other, _ := s.CreateAccount(ctx, store.Account{Label: "personal", KeyringRef: "r-personal"})
	seedYesterday(t, s, acct, "m1", 8_000_000) // work biggest
	seedYesterday(t, s, other.ID, "m2", 2_000_000)

	d.Evaluate(ctx)
	if len(*notes) != 1 {
		t.Fatalf("want 1 notification, got %v", *notes)
	}
	msg := (*notes)[0]
	for _, want := range []string{"2 accounts", "top: work (8.0M)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notification %q missing %q", msg, want)
		}
	}
}

func TestDailySummary_SkipsZeroDayButMarksHandled(t *testing.T) {
	s, _, d, notes := summaryFixture(t)
	ctx := context.Background()
	// No usage seeded for yesterday.
	d.Evaluate(ctx)
	if len(*notes) != 0 {
		t.Errorf("notified on a zero-usage day: %v", *notes)
	}
	if v, _ := s.GetSetting(ctx, SettingsKeyDailySummaryLast); v != "2026-06-16" {
		t.Errorf("last = %q, want 2026-06-16 (marked handled)", v)
	}
}

func TestDailySummary_Disabled(t *testing.T) {
	s, acct, d, notes := summaryFixture(t)
	ctx := context.Background()
	if err := s.PutSetting(ctx, SettingsKeyDailySummaryEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	seedYesterday(t, s, acct, "m1", 1_000_000)

	d.Evaluate(ctx)
	if len(*notes) != 0 {
		t.Errorf("notified while disabled: %v", *notes)
	}
}
