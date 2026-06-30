package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

// sampleSeq makes seeded message/request IDs unique across calls. The dedup
// index UNIQUE(message_id, request_id) is GLOBAL (not per-account), so two
// samples sharing a ts would otherwise collide and the second would be a no-op.
var sampleSeq int

// These hermetic e2e tests drive `aimonitor tokens` against a temp store seeded
// with usage_samples rows. No keyring or network is touched — tokens reads only
// the SQLite usage_samples table. Reuses the account_e2e harness (e2eEnv/runCLI/
// seedAccount/openStoreAt). No t.Parallel (globals).

// seedSample inserts one per-message token sample for an account at ts.
func seedSample(t *testing.T, s *store.Store, accountID int64, ts time.Time, sample store.TokenSample) {
	t.Helper()
	sample.Ts = ts
	sampleSeq++
	uniq := fmt.Sprintf("%d-%d", accountID, sampleSeq)
	if sample.MessageID == "" {
		sample.MessageID = "msg-" + uniq
	}
	if sample.RequestID == "" {
		sample.RequestID = "req-" + uniq
	}
	if _, err := s.InsertUsageSample(context.Background(), accountID, sample); err != nil {
		t.Fatalf("seed usage sample: %v", err)
	}
}

// TestTokens_EmptyWindow: no samples → the "no token usage recorded" hint.
func TestTokens_EmptyWindow(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, nil)

	out, err := runCLI(t, "", "tokens")
	if err != nil {
		t.Fatalf("tokens: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No token usage recorded") {
		t.Errorf("output = %q, want the empty-window hint", out)
	}
}

// TestTokens_DailyTableAllAccounts: two accounts with samples → the daily table
// shows both labels, the ACCOUNT column, and a TOTAL row.
func TestTokens_DailyTableAllAccounts(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "alpha"}, nil)
	b := seedAccount(t, dbPath, store.Account{Label: "bravo"}, nil)

	s := openStoreAt(t, dbPath)
	now := time.Now().Add(-2 * time.Hour)
	seedSample(t, s, a.ID, now, store.TokenSample{Input: 1000, Output: 2000, CacheRead: 500, CacheWrite: 100, Model: "claude-opus-4", Project: "-Users-x-proj"})
	seedSample(t, s, b.ID, now, store.TokenSample{Input: 10, Output: 20, Model: "claude-sonnet-4", Project: "-Users-x-other"})

	out, err := runCLI(t, "", "tokens")
	if err != nil {
		t.Fatalf("tokens: %v (output: %q)", err, out)
	}
	for _, want := range []string{"WHEN", "ACCOUNT", "alpha", "bravo", "TOTAL", "CACHE%"} {
		if !strings.Contains(out, want) {
			t.Errorf("daily table missing %q\n%s", want, out)
		}
	}
}

// TestTokens_HourlyForOneAccount: --hourly --account hides the ACCOUNT column
// (single account) and uses an hour bucket.
func TestTokens_HourlyForOneAccount(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "solo"}, nil)
	s := openStoreAt(t, dbPath)
	seedSample(t, s, a.ID, time.Now().Add(-30*time.Minute), store.TokenSample{Input: 5, Output: 5, Model: "m"})

	out, err := runCLI(t, "", "tokens", "--hourly", "--account", "solo")
	if err != nil {
		t.Fatalf("tokens --hourly --account: %v (output: %q)", err, out)
	}
	if strings.Contains(out, "ACCOUNT") {
		t.Errorf("single-account table should hide the ACCOUNT column\n%s", out)
	}
	if !strings.Contains(out, "WHEN") {
		t.Errorf("hourly table missing WHEN header\n%s", out)
	}
}

// TestTokens_ByModel renders the per-model grouping (MODEL header).
func TestTokens_ByModel(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "work"}, nil)
	s := openStoreAt(t, dbPath)
	seedSample(t, s, a.ID, time.Now().Add(-time.Hour), store.TokenSample{Input: 100, Output: 50, Model: "claude-opus-4"})

	out, err := runCLI(t, "", "tokens", "--by-model")
	if err != nil {
		t.Fatalf("tokens --by-model: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "MODEL") || !strings.Contains(out, "claude-opus-4") {
		t.Errorf("by-model output missing MODEL/model name\n%s", out)
	}
}

// TestTokens_ByProject renders the per-project grouping with a prettified path.
func TestTokens_ByProject(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "work"}, nil)
	s := openStoreAt(t, dbPath)
	seedSample(t, s, a.ID, time.Now().Add(-time.Hour), store.TokenSample{Input: 100, Output: 50, Model: "m", Project: "-Users-nana-proj"})

	out, err := runCLI(t, "", "tokens", "--by-project")
	if err != nil {
		t.Fatalf("tokens --by-project: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "PROJECT") || !strings.Contains(out, "/Users/nana/proj") {
		t.Errorf("by-project output missing PROJECT/pretty path\n%s", out)
	}
}

// TestTokens_CSV emits a CSV header + a data row (account always present).
func TestTokens_CSV(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "work"}, nil)
	s := openStoreAt(t, dbPath)
	seedSample(t, s, a.ID, time.Now().Add(-time.Hour), store.TokenSample{Input: 100, Output: 50, CacheRead: 25, Model: "m"})

	out, err := runCLI(t, "", "tokens", "--format", "csv")
	if err != nil {
		t.Fatalf("tokens --format csv: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "day,account,input,output,cache_read,cache_write,cache_hit_pct,total,messages") {
		t.Errorf("csv missing header row\n%s", out)
	}
	if !strings.Contains(out, "work") {
		t.Errorf("csv missing data row for 'work'\n%s", out)
	}
}

// TestTokens_JSON emits a JSON array with the expected keys.
func TestTokens_JSON(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "work"}, nil)
	s := openStoreAt(t, dbPath)
	seedSample(t, s, a.ID, time.Now().Add(-time.Hour), store.TokenSample{Input: 100, Output: 50, Model: "m"})

	out, err := runCLI(t, "", "tokens", "--by-model", "--format", "json")
	if err != nil {
		t.Fatalf("tokens --format json: %v (output: %q)", err, out)
	}
	var recs []map[string]any
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(recs) == 0 {
		t.Fatalf("json output had no records\n%s", out)
	}
	if recs[0]["dimension"] != "model" || recs[0]["account"] != "work" {
		t.Errorf("json record fields wrong: %+v", recs[0])
	}
}

// TestTokens_ByModelAndByProjectMutuallyExclusive errors out.
func TestTokens_ByModelAndByProjectMutuallyExclusive(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, nil)

	_, err := runCLI(t, "", "tokens", "--by-model", "--by-project")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

// TestTokens_InvalidFormat errors out.
func TestTokens_InvalidFormat(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, nil)

	_, err := runCLI(t, "", "tokens", "--format", "xml")
	if err == nil || !strings.Contains(err.Error(), "invalid --format") {
		t.Fatalf("expected invalid-format error, got %v", err)
	}
}

// TestTokens_BadSince errors out.
func TestTokens_BadSince(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, nil)

	_, err := runCLI(t, "", "tokens", "--since", "banana")
	if err == nil || !strings.Contains(err.Error(), "invalid --since") {
		t.Fatalf("expected invalid-since error, got %v", err)
	}
}

// TestTokens_UnknownAccount errors out.
func TestTokens_UnknownAccount(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, nil)

	_, err := runCLI(t, "", "tokens", "--account", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected unknown-account error naming 'ghost', got %v", err)
	}
}

// TestParseSince covers the day/week unit parsing and the empty defaults.
func TestParseSince(t *testing.T) {
	cases := []struct {
		in     string
		hourly bool
		want   time.Duration
	}{
		{"", false, 7 * 24 * time.Hour},
		{"", true, 24 * time.Hour},
		{"3d", false, 3 * 24 * time.Hour},
		{"2w", false, 2 * 7 * 24 * time.Hour},
		{"90m", false, 90 * time.Minute},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, c.hourly)
		if err != nil {
			t.Errorf("parseSince(%q,%v) err: %v", c.in, c.hourly, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSince(%q,%v) = %v want %v", c.in, c.hourly, got, c.want)
		}
	}
	for _, bad := range []string{"0d", "-1w", "xyz", "5x"} {
		if _, err := parseSince(bad, false); err == nil {
			t.Errorf("parseSince(%q) should error", bad)
		}
	}
}

// TestPrettyProject / TestLabelFor / TestRound1 cover the small helpers.
func TestPrettyProject(t *testing.T) {
	cases := map[string]string{
		"":                  "(unknown)",
		"(unknown)":         "(unknown)",
		"-Users-nana-proj":  "/Users/nana/proj",
		"-a-b":              "/a/b",
	}
	for in, want := range cases {
		if got := prettyProject(in); got != want {
			t.Errorf("prettyProject(%q) = %q want %q", in, got, want)
		}
	}
}

func TestLabelFor(t *testing.T) {
	m := map[int64]string{1: "work"}
	if got := labelFor(m, 1); got != "work" {
		t.Errorf("labelFor known = %q want work", got)
	}
	if got := labelFor(m, 99); got != "#99" {
		t.Errorf("labelFor unknown = %q want #99", got)
	}
}

func TestRound1(t *testing.T) {
	if got := round1(33.333); got != 33.3 {
		t.Errorf("round1(33.333) = %v want 33.3", got)
	}
}
