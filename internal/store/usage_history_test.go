package store

import (
	"context"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// newAccountForHistory inserts a minimal account so usage_history's FK is
// satisfied, returning its id.
func newAccountForHistory(t *testing.T, s *Store, label string) int64 {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), Account{
		Provider:   "claude",
		Label:      label,
		KeyringRef: "ref-" + label,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return a.ID
}

func TestUsageHistory_AppendAndList(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	id := newAccountForHistory(t, s, "acct")

	base := time.Now().Add(-2 * time.Hour).Truncate(time.Millisecond)
	for i := 0; i < 3; i++ {
		err := s.AppendUsageHistory(ctx, id, UsageSample{
			Ts:          base.Add(time.Duration(i) * time.Minute),
			FiveHourPct: float64(10 * i),
			SevenDayPct: float64(5 * i),
		})
		if err != nil {
			t.Fatalf("AppendUsageHistory[%d]: %v", i, err)
		}
	}

	got, err := s.ListUsageHistory(ctx, id, base.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ListUsageHistory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 points, got %d", len(got))
	}
	// Ascending by ts, values preserved.
	for i, p := range got {
		if p.FiveHourPct != float64(10*i) || p.SevenDayPct != float64(5*i) {
			t.Errorf("point %d: got 5h=%.0f 7d=%.0f", i, p.FiveHourPct, p.SevenDayPct)
		}
	}

	// `since` filter excludes earlier points.
	got, err = s.ListUsageHistory(ctx, id, base.Add(90*time.Second))
	if err != nil {
		t.Fatalf("ListUsageHistory(since): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 point after since, got %d", len(got))
	}
}

func TestUsageHistory_PrunesOldPointsOnAppend(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	id := newAccountForHistory(t, s, "acct")

	// One ancient point (well past retention) then a fresh one. The fresh
	// append must prune the ancient point.
	old := time.Now().Add(-UsageHistoryRetention - 24*time.Hour)
	if err := s.AppendUsageHistory(ctx, id, UsageSample{Ts: old, FiveHourPct: 99}); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if err := s.AppendUsageHistory(ctx, id, UsageSample{Ts: time.Now(), FiveHourPct: 1}); err != nil {
		t.Fatalf("append fresh: %v", err)
	}

	all, err := s.ListUsageHistory(ctx, id, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageHistory: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ancient point should be pruned; got %d points", len(all))
	}
	if all[0].FiveHourPct != 1 {
		t.Errorf("surviving point should be the fresh one, got %.0f", all[0].FiveHourPct)
	}
}

// PutLimits must also record a history point — the single chokepoint that
// keeps the sparkline populated without touching every fetch call site.
func TestPutLimits_RecordsHistory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	id := newAccountForHistory(t, s, "acct")

	if err := s.PutLimits(ctx, id, provider.Limits{
		FiveHourPct: 42,
		SevenDayPct: 17,
		FetchedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("PutLimits: %v", err)
	}
	pts, err := s.ListUsageHistory(ctx, id, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageHistory: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("PutLimits should append exactly 1 history point, got %d", len(pts))
	}
	if pts[0].FiveHourPct != 42 || pts[0].SevenDayPct != 17 {
		t.Errorf("history point mismatch: 5h=%.0f 7d=%.0f", pts[0].FiveHourPct, pts[0].SevenDayPct)
	}
}
