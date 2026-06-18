package store

import (
	"context"
	"testing"
	"time"
)

func newAccountForTokens(t *testing.T, s *Store, label string) int64 {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), Account{Label: label, KeyringRef: "ref-" + label})
	if err != nil {
		t.Fatalf("CreateAccount(%s): %v", label, err)
	}
	return a.ID
}

func TestInsertUsageSample_DedupesOnMessageRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")
	now := time.Now()

	// Same (message_id, request_id) logged three times (streaming/retry),
	// identical tokens — exactly the real-world duplication pattern.
	dup := TokenSample{Ts: now, MessageID: "msg_1", RequestID: "req_1", Input: 6382, Output: 618}
	inserted, err := s.InsertUsageSample(ctx, acct, dup)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !inserted {
		t.Fatal("first insert should report inserted=true")
	}
	for i := 0; i < 2; i++ {
		inserted, err = s.InsertUsageSample(ctx, acct, dup)
		if err != nil {
			t.Fatalf("dup insert: %v", err)
		}
		if inserted {
			t.Fatal("duplicate insert should report inserted=false")
		}
	}

	// A genuinely different response is kept.
	if _, err := s.InsertUsageSample(ctx, acct, TokenSample{
		Ts: now, MessageID: "msg_2", RequestID: "req_2", Input: 2, Output: 3,
	}); err != nil {
		t.Fatalf("distinct insert: %v", err)
	}

	buckets, err := s.TokenUsageByDay(ctx, acct, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(buckets))
	}
	// 2 unique rows, NOT 4 — dedup held. Total = (6382+618) + (2+3) = 7005.
	if buckets[0].Messages != 2 {
		t.Errorf("Messages = %d, want 2 (dedup)", buckets[0].Messages)
	}
	if buckets[0].Total != 7005 {
		t.Errorf("Total = %d, want 7005", buckets[0].Total)
	}
}

// TestInsertUsageSample_StreamingPartialsKeepMax reproduces the real
// transcript pattern where a response is logged multiple times with GROWING
// counts (output_tokens climbs as streaming completes). The stored row must
// end up with the final/largest counts, regardless of arrival order — not
// the first (smallest) partial.
func TestInsertUsageSample_StreamingPartialsKeepMax(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")
	now := time.Now()

	// Forward order: small partial first, full last.
	partial := TokenSample{Ts: now, MessageID: "m1", RequestID: "r1", Input: 3, Output: 3, CacheRead: 5825, CacheWrite: 7028}
	full := TokenSample{Ts: now, MessageID: "m1", RequestID: "r1", Input: 3, Output: 136, CacheRead: 5825, CacheWrite: 7028}
	mustInsert(t, s, acct, partial)
	mustInsert(t, s, acct, full)

	// Reverse order on a second message: full first, then a stale small
	// partial — must NOT regress to the smaller value.
	full2 := TokenSample{Ts: now, MessageID: "m2", RequestID: "r2", Input: 5, Output: 90, CacheRead: 2055, CacheWrite: 12853}
	partial2 := TokenSample{Ts: now, MessageID: "m2", RequestID: "r2", Input: 5, Output: 1, CacheRead: 2055, CacheWrite: 12853}
	mustInsert(t, s, acct, full2)
	mustInsert(t, s, acct, partial2)

	buckets, err := s.TokenUsageByDay(ctx, acct, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay: %v", err)
	}
	if len(buckets) != 1 || buckets[0].Messages != 2 {
		t.Fatalf("got %d buckets / messages, want 1 bucket of 2 messages: %+v", len(buckets), buckets)
	}
	// m1 final = 3+136+5825+7028 = 12992; m2 final = 5+90+2055+12853 = 15003.
	want := int64(12992 + 15003)
	if buckets[0].Total != want {
		t.Errorf("Total = %d, want %d (final counts, not partials)", buckets[0].Total, want)
	}
	if buckets[0].Output != 136+90 {
		t.Errorf("Output = %d, want %d", buckets[0].Output, 136+90)
	}
}

func mustInsert(t *testing.T, s *Store, acct int64, smp TokenSample) {
	t.Helper()
	if _, err := s.InsertUsageSample(context.Background(), acct, smp); err != nil {
		t.Fatalf("InsertUsageSample: %v", err)
	}
}

func TestInsertUsageSample_RequiresAccount(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.InsertUsageSample(context.Background(), 0, TokenSample{MessageID: "m", RequestID: "r"}); err == nil {
		t.Error("expected error for accountID=0")
	}
}

func TestPruneUsageSamples(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct := newAccountForTokens(t, s, "a")
	now := time.Now()

	old := TokenSample{Ts: now.Add(-100 * 24 * time.Hour), MessageID: "old", RequestID: "old", Input: 10}
	fresh := TokenSample{Ts: now, MessageID: "new", RequestID: "new", Input: 20}
	if _, err := s.InsertUsageSample(ctx, acct, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertUsageSample(ctx, acct, fresh); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneUsageSamples(ctx, UsageSamplesRetention)
	if err != nil {
		t.Fatalf("PruneUsageSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	buckets, _ := s.TokenUsageByDay(ctx, acct, now.Add(-200*24*time.Hour), now.Add(time.Hour))
	var total int64
	for _, b := range buckets {
		total += b.Total
	}
	if total != 20 {
		t.Errorf("remaining total = %d, want 20 (old pruned)", total)
	}
}
