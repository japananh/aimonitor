package store

import (
	"context"
	"testing"
	"time"
)

// TestTokenUsage_LocalDayBuckets verifies day buckets follow LOCAL time, not
// UTC. The SQL bucket string must equal Go's local-time formatting of the
// same instant; on any non-UTC machine (e.g. the +07 dev box) a UTC-only
// query would produce a different day string and fail here.
func TestTokenUsage_LocalDayBuckets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")

	// Two samples ~3h apart on one instant's local day, one ~30h earlier on
	// the previous local day. Use a fixed instant so the test is stable.
	base := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	samples := []TokenSample{
		{Ts: base, MessageID: "m1", RequestID: "r1", Input: 100, Output: 10},
		{Ts: base.Add(3 * time.Hour), MessageID: "m2", RequestID: "r2", Input: 200, Output: 20, CacheRead: 5},
		{Ts: base.Add(-30 * time.Hour), MessageID: "m3", RequestID: "r3", Input: 50, Output: 5},
	}
	for _, smp := range samples {
		if _, err := s.InsertUsageSample(ctx, acct, smp); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	buckets, err := s.TokenUsageByDay(ctx, acct, base.Add(-72*time.Hour), base.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d day buckets, want 2: %+v", len(buckets), buckets)
	}

	dayEarly := base.Add(-30 * time.Hour).Local().Format("2006-01-02")
	dayMain := base.Local().Format("2006-01-02")
	if buckets[0].Bucket != dayEarly {
		t.Errorf("bucket[0] = %q, want local day %q", buckets[0].Bucket, dayEarly)
	}
	if buckets[1].Bucket != dayMain {
		t.Errorf("bucket[1] = %q, want local day %q", buckets[1].Bucket, dayMain)
	}
	// Main day aggregates m1+m2: total = 110 + 225 = 335.
	if buckets[1].Total != 335 {
		t.Errorf("main day Total = %d, want 335", buckets[1].Total)
	}
	if buckets[1].CacheRead != 5 {
		t.Errorf("main day CacheRead = %d, want 5", buckets[1].CacheRead)
	}
	if buckets[1].Messages != 2 {
		t.Errorf("main day Messages = %d, want 2", buckets[1].Messages)
	}
}

// TestTokenUsage_HourBucketsAndLocalFormat checks hour granularity and that
// the hour label matches Go's local-time format.
func TestTokenUsage_HourBucketsAndLocalFormat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")

	base := time.Date(2026, 6, 16, 10, 15, 0, 0, time.UTC)
	for i, smp := range []TokenSample{
		{Ts: base, MessageID: "m1", RequestID: "r1", Input: 100},
		{Ts: base.Add(20 * time.Minute), MessageID: "m2", RequestID: "r2", Input: 50}, // same hour
		{Ts: base.Add(90 * time.Minute), MessageID: "m3", RequestID: "r3", Input: 10}, // next hour
	} {
		if _, err := s.InsertUsageSample(ctx, acct, smp); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	buckets, err := s.TokenUsageByHour(ctx, acct, base.Add(-time.Hour), base.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByHour: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d hour buckets, want 2: %+v", len(buckets), buckets)
	}
	wantHour := base.Local().Format("2006-01-02 15:00")
	if buckets[0].Bucket != wantHour {
		t.Errorf("bucket[0] = %q, want local hour %q", buckets[0].Bucket, wantHour)
	}
	if buckets[0].Total != 150 {
		t.Errorf("first hour Total = %d, want 150", buckets[0].Total)
	}
}

// TestTokenUsageByModel groups per model, orders by descending total, and
// reports an empty model as "(unknown)".
func TestTokenUsageByModel(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")
	now := time.Now()

	ins := func(id, model string, in, out int64) {
		if _, err := s.InsertUsageSample(ctx, acct, TokenSample{
			Ts: now, MessageID: id, RequestID: id, Input: in, Output: out, Model: model,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ins("m1", "claude-opus-4-8", 100, 50)  // opus total 150
	ins("m2", "claude-opus-4-8", 200, 100) // opus total +300 => 450
	ins("m3", "claude-haiku-4-5", 10, 5)   // haiku total 15
	ins("m4", "", 1, 1)                    // unknown total 2

	rows, err := s.TokenUsageByModel(ctx, acct, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByModel: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d model rows, want 3: %+v", len(rows), rows)
	}
	// Ordered by descending total: opus (450) > haiku (15) > unknown (2).
	if rows[0].Model != "claude-opus-4-8" || rows[0].Total != 450 || rows[0].Messages != 2 {
		t.Errorf("row[0] = %+v, want opus total 450 msgs 2", rows[0])
	}
	if rows[1].Model != "claude-haiku-4-5" || rows[1].Total != 15 {
		t.Errorf("row[1] = %+v, want haiku total 15", rows[1])
	}
	if rows[2].Model != "(unknown)" || rows[2].Total != 2 {
		t.Errorf("row[2] = %+v, want (unknown) total 2", rows[2])
	}
}

// TestTokenUsage_AllAccounts checks that accountID 0 spans accounts and
// groups per (bucket, account).
func TestTokenUsage_AllAccounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := newAccountForTokens(t, s, "a")
	b := newAccountForTokens(t, s, "b")
	now := time.Now()

	if _, err := s.InsertUsageSample(ctx, a, TokenSample{Ts: now, MessageID: "m1", RequestID: "r1", Input: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertUsageSample(ctx, b, TokenSample{Ts: now, MessageID: "m2", RequestID: "r2", Input: 200}); err != nil {
		t.Fatal(err)
	}

	buckets, err := s.TokenUsageByDay(ctx, 0, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay(all): %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2 (one per account)", len(buckets))
	}
	byAcct := map[int64]int64{}
	for _, bk := range buckets {
		byAcct[bk.AccountID] = bk.Total
	}
	if byAcct[a] != 100 || byAcct[b] != 200 {
		t.Errorf("per-account totals = %v, want {a:100, b:200}", byAcct)
	}
}
