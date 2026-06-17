package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

func recorderFixture(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s := openStore(t)
	a, err := s.CreateAccount(context.Background(), store.Account{Label: "work", KeyringRef: "ref-work"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return s, a.ID
}

func totalTokens(t *testing.T, s *store.Store, acct int64) (int64, int64) {
	t.Helper()
	buckets, err := s.TokenUsageByDay(context.Background(), acct,
		time.Now().Add(-365*24*time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay: %v", err)
	}
	var total, msgs int64
	for _, b := range buckets {
		total += b.Total
		msgs += b.Messages
	}
	return total, msgs
}

func TestSampleRecorder_RecordsFreshAttributedToActive(t *testing.T) {
	s, acct := recorderFixture(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	r := NewSampleRecorder(s,
		func(ctx context.Context) (store.Account, bool, error) {
			return store.Account{ID: acct}, true, nil
		}, nil)
	r.clock = func() time.Time { return now }

	r.Record(context.Background(), SampleEvent{Sample: claude.Sample{
		Ts: now.Add(-time.Second), MessageID: "m1", RequestID: "r1", InputTokens: 100, OutputTokens: 20,
	}})

	total, msgs := totalTokens(t, s, acct)
	if total != 120 || msgs != 1 {
		t.Fatalf("got total=%d msgs=%d, want 120/1", total, msgs)
	}
}

func TestSampleRecorder_SkipsStale(t *testing.T) {
	s, acct := recorderFixture(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	r := NewSampleRecorder(s,
		func(ctx context.Context) (store.Account, bool, error) {
			return store.Account{ID: acct}, true, nil
		}, nil)
	r.clock = func() time.Time { return now }

	// 2h old > 1h stale threshold → dropped (fresh-install replay guard).
	r.Record(context.Background(), SampleEvent{Sample: claude.Sample{
		Ts: now.Add(-2 * time.Hour), MessageID: "old", RequestID: "old", InputTokens: 999,
	}})

	total, msgs := totalTokens(t, s, acct)
	if total != 0 || msgs != 0 {
		t.Fatalf("stale sample persisted: total=%d msgs=%d", total, msgs)
	}
}

func TestSampleRecorder_SkipsWhenNoActiveAccount(t *testing.T) {
	s, acct := recorderFixture(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	r := NewSampleRecorder(s,
		func(ctx context.Context) (store.Account, bool, error) {
			return store.Account{}, false, nil // nothing resolves
		}, nil)
	r.clock = func() time.Time { return now }

	r.Record(context.Background(), SampleEvent{Sample: claude.Sample{
		Ts: now, MessageID: "m1", RequestID: "r1", InputTokens: 100,
	}})

	if total, _ := totalTokens(t, s, acct); total != 0 {
		t.Fatalf("recorded with no active account: total=%d", total)
	}
}

// TestSampleRecorder_EndToEndFromJSONL exercises the real chain that ships:
// claude.ParseReader (extracting message.id + requestId) → recorder.Record
// (stale guard + attribution + dedup upsert) → store → TokenUsageByDay. The
// only piece not covered here is fsnotify itself (see watcher_test.go).
func TestSampleRecorder_EndToEndFromJSONL(t *testing.T) {
	s, acct := recorderFixture(t)
	now := time.Now()
	r := NewSampleRecorder(s,
		func(ctx context.Context) (store.Account, bool, error) {
			return store.Account{ID: acct}, true, nil
		}, nil)
	// Default clock (time.Now); fresh timestamps below pass the stale guard.

	ts := now.UTC().Format(time.RFC3339Nano)
	lines := []string{
		// Two streaming partials of one response — output grows 3 → 136.
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"r1","message":{"id":"m1","model":"claude-opus-4-8","usage":{"input_tokens":3,"output_tokens":3,"cache_creation_input_tokens":7028,"cache_read_input_tokens":5825}}}`,
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"r1","message":{"id":"m1","model":"claude-opus-4-8","usage":{"input_tokens":3,"output_tokens":136,"cache_creation_input_tokens":7028,"cache_read_input_tokens":5825}}}`,
		// A user line — no usage, must be skipped.
		`{"type":"user","timestamp":"` + ts + `","message":{"content":"hi"}}`,
		// A distinct response.
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"r2","message":{"id":"m2","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":244}}}`,
	}
	jsonl := strings.Join(lines, "\n") + "\n"

	if _, err := claude.ParseReader(strings.NewReader(jsonl), func(smp claude.Sample) {
		r.Record(context.Background(), SampleEvent{Path: "p.jsonl", Sample: smp})
	}, nil); err != nil {
		t.Fatalf("ParseReader: %v", err)
	}

	buckets, err := s.TokenUsageByDay(context.Background(), acct, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("TokenUsageByDay: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("want 1 bucket, got %d: %+v", len(buckets), buckets)
	}
	// m1 final = 3+136+7028+5825 = 12992; m2 = 2+244 = 246; total = 13238.
	if buckets[0].Messages != 2 {
		t.Errorf("messages = %d, want 2 (dedup held)", buckets[0].Messages)
	}
	if buckets[0].Total != 13238 {
		t.Errorf("total = %d, want 13238 (final partial counts)", buckets[0].Total)
	}
	if buckets[0].Output != 136+244 {
		t.Errorf("output = %d, want %d", buckets[0].Output, 136+244)
	}
}

func TestProjectFromPath(t *testing.T) {
	cases := map[string]string{
		"/Users/nana/.claude/projects/-Users-nana-aimonitor/abc.jsonl": "-Users-nana-aimonitor",
		"relative/proj/x.jsonl": "proj",
		"x.jsonl":               "", // no parent dir component
		"":                      "",
	}
	for path, want := range cases {
		if got := projectFromPath(path); got != want {
			t.Errorf("projectFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSampleRecorder_DedupesAndCachesResolver(t *testing.T) {
	s, acct := recorderFixture(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	var resolveCalls int
	r := NewSampleRecorder(s,
		func(ctx context.Context) (store.Account, bool, error) {
			resolveCalls++
			return store.Account{ID: acct}, true, nil
		}, nil)
	r.clock = func() time.Time { return now }

	ev := SampleEvent{Sample: claude.Sample{
		Ts: now, MessageID: "m1", RequestID: "r1", InputTokens: 100,
	}}
	// Same line three times (streaming/retry) within the resolver TTL.
	r.Record(context.Background(), ev)
	r.Record(context.Background(), ev)
	r.Record(context.Background(), ev)

	total, msgs := totalTokens(t, s, acct)
	if total != 100 || msgs != 1 {
		t.Errorf("dedup failed: total=%d msgs=%d, want 100/1", total, msgs)
	}
	if resolveCalls != 1 {
		t.Errorf("resolver called %d times, want 1 (cached within TTL)", resolveCalls)
	}
}
