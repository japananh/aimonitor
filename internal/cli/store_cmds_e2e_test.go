package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Hermetic e2e coverage for the data/store commands: list, status, rename,
// log, version, probe (cache branch). All drive the real cobra commands via
// the account_e2e harness (e2eEnv/runCLI/seedAccount/openStoreAt). No keyring
// (beyond the claude seam), no network, no t.Parallel.

// ---- list -------------------------------------------------------------------

func TestList_Empty(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "list")
	if err != nil {
		t.Fatalf("list: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No accounts configured") {
		t.Errorf("output = %q, want the empty-list hint", out)
	}
}

func TestList_PopulatedMarksActive(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	activeBlob := validBlob("sk-active")
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, activeBlob)
	seedAccount(t, dbPath, store.Account{Label: "play"}, validBlob("sk-play"))

	// Make "work" the active account: its stash byte-matches the live slot.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, activeBlob); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "list")
	if err != nil {
		t.Fatalf("list: %v (output: %q)", err, out)
	}
	for _, want := range []string{"LABEL", "EMAIL", "ACTIVE", "work", "play", "w@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q\n%s", want, out)
		}
	}
	// The active marker (✓) must be on the "work" row.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "work") && !strings.Contains(line, "✓") {
			t.Errorf("work row should carry the active marker: %q", line)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "play") && strings.Contains(line, "✓") {
			t.Errorf("play row should NOT carry the active marker: %q", line)
		}
	}
}

func TestList_ShowsLastUsed(t *testing.T) {
	_, dbPath := e2eEnv(t)
	acct := seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	s := openStoreAt(t, dbPath)
	ts := time.Date(2025, 1, 2, 3, 4, 0, 0, time.Local)
	if err := s.UpdateAccountLastUsed(context.Background(), acct.ID, ts); err != nil {
		t.Fatalf("update last used: %v", err)
	}

	out, err := runCLI(t, "", "list")
	if err != nil {
		t.Fatalf("list: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, ts.Format("2006-01-02 15:04")) {
		t.Errorf("list LAST USED column should show the timestamp\n%s", out)
	}
}

func TestDash(t *testing.T) {
	if dash("") != "-" {
		t.Errorf("dash(empty) should be -")
	}
	if dash("x") != "x" {
		t.Errorf("dash(x) should be x")
	}
}

// ---- status -----------------------------------------------------------------

func TestStatus_EmptySlotErrors(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))

	out, err := runCLI(t, "", "status")
	if err == nil {
		t.Fatalf("expected error with empty live slot, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "no Claude Code credential") {
		t.Errorf("error = %q, want empty-slot message", err.Error())
	}
}

func TestStatus_MatchesActiveAccount(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	blob := validBlob("sk-work")
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, blob)
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blob); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "status")
	if err != nil {
		t.Fatalf("status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Active account: work") || !strings.Contains(out, "w@example.com") {
		t.Errorf("status output = %q, want it to name 'work' + email", out)
	}
}

func TestStatus_ActiveNotStashed(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk-work"))
	// Live slot carries a blob that matches NO stash.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-orphan")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "status")
	if err != nil {
		t.Fatalf("status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "not one of aimonitor's stashed accounts") {
		t.Errorf("status output = %q, want the unrecognized-credential message", out)
	}
}

// ---- rename -----------------------------------------------------------------

func TestRename_HappyPath(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "old"}, validBlob("sk"))

	out, err := runCLI(t, "", "rename", "old", "new")
	if err != nil {
		t.Fatalf("rename: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Renamed") {
		t.Errorf("output = %q, want a 'Renamed' confirmation", out)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(context.Background(), "new"); err != nil {
		t.Errorf("account should be reachable by new label: %v", err)
	}
	if _, err := s.GetAccountByLabel(context.Background(), "old"); err == nil {
		t.Errorf("old label should no longer resolve")
	}
}

func TestRename_UnknownLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "rename", "ghost", "new")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected error naming 'ghost', got %v", err)
	}
}

func TestRename_EmptyNewLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "rename", "old", "   ")
	if err == nil || !strings.Contains(err.Error(), "new label is required") {
		t.Fatalf("expected empty-new-label error, got %v", err)
	}
}

func TestRename_SameLabelIsNoOp(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	out, err := runCLI(t, "", "rename", "work", "work")
	if err != nil {
		t.Fatalf("same-label rename should be a no-op, got %v (output: %q)", err, out)
	}
}

// ---- log --------------------------------------------------------------------

func TestLog_Empty(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "log")
	if err != nil {
		t.Fatalf("log: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No switch events yet") {
		t.Errorf("output = %q, want the empty-log message", out)
	}
}

func TestLog_Populated(t *testing.T) {
	_, dbPath := e2eEnv(t)
	s := openStoreAt(t, dbPath)
	if err := s.InsertSwitchAudit(context.Background(), store.SwitchAuditRecord{
		FromLabel:           "work",
		ToLabel:             "play",
		Trigger:             store.TriggerManual,
		Reason:              "manual swap",
		FromProbedRemaining: 1000,
		ToProbedRemaining:   2000,
	}); err != nil {
		t.Fatalf("seed switch audit: %v", err)
	}

	out, err := runCLI(t, "", "log")
	if err != nil {
		t.Fatalf("log: %v (output: %q)", err, out)
	}
	for _, want := range []string{"WHEN", "TRIGGER", "work", "play", "manual", "1000", "2000", "manual swap"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q\n%s", want, out)
		}
	}
}

func TestLog_LimitFlag(t *testing.T) {
	_, dbPath := e2eEnv(t)
	s := openStoreAt(t, dbPath)
	for i := 0; i < 3; i++ {
		if err := s.InsertSwitchAudit(context.Background(), store.SwitchAuditRecord{
			ToLabel: "t", Trigger: store.TriggerManual,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out, err := runCLI(t, "", "log", "-n", "1")
	if err != nil {
		t.Fatalf("log -n 1: %v (output: %q)", err, out)
	}
	// Header + exactly one data row.
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("log -n 1 produced %d non-empty lines, want 2 (header + 1)\n%s", lines, out)
	}
}

// ---- version ----------------------------------------------------------------

func TestVersion_Text(t *testing.T) {
	out, err := runCLI(t, "", "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "aimonitor") || !strings.Contains(out, "commit") {
		t.Errorf("version text = %q, want it to mention aimonitor + commit", out)
	}
}

func TestVersion_JSON(t *testing.T) {
	out, err := runCLI(t, "", "version", "--json")
	if err != nil {
		t.Fatalf("version --json: %v", err)
	}
	if !strings.Contains(out, `"version"`) || !strings.Contains(out, `"buildDate"`) {
		t.Errorf("version --json = %q, want version/buildDate keys", out)
	}
}

// ---- probe (cache branch only — a fresh cached probe skips the network) -----

// seedFreshProbe writes a probe_result with ProbedAt=now so probeWithCache
// returns it from cache (no live network probe).
func seedFreshProbe(t *testing.T, s *store.Store, accountID int64, remaining int64) {
	t.Helper()
	if err := s.PutProbeResult(context.Background(), accountID, provider.RateLimit{
		AccountID:       accountID,
		ProbedAt:        time.Now(),
		TokensRemaining: remaining,
		ResetAt:         time.Now().Add(time.Hour),
		HTTPStatus:      200,
	}); err != nil {
		t.Fatalf("seed probe: %v", err)
	}
}

func TestProbe_CacheHit(t *testing.T) {
	_, dbPath := e2eEnv(t)
	acct := seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	s := openStoreAt(t, dbPath)
	seedFreshProbe(t, s, acct.ID, 4242)

	out, err := runCLI(t, "", "probe", "work")
	if err != nil {
		t.Fatalf("probe (cache): %v (output: %q)", err, out)
	}
	for _, want := range []string{"LABEL", "REMAINING", "work", "4242", "cache"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe output missing %q\n%s", want, out)
		}
	}
}

func TestProbe_AllCacheHits(t *testing.T) {
	_, dbPath := e2eEnv(t)
	a := seedAccount(t, dbPath, store.Account{Label: "alpha"}, validBlob("sk-a"))
	b := seedAccount(t, dbPath, store.Account{Label: "bravo"}, validBlob("sk-b"))
	s := openStoreAt(t, dbPath)
	// Fresh probes for BOTH so --all never falls through to a live probe.
	seedFreshProbe(t, s, a.ID, 111)
	seedFreshProbe(t, s, b.ID, 222)

	out, err := runCLI(t, "", "probe", "--all")
	if err != nil {
		t.Fatalf("probe --all (cache): %v (output: %q)", err, out)
	}
	for _, want := range []string{"alpha", "bravo", "111", "222", "cache"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe --all output missing %q\n%s", want, out)
		}
	}
}

func TestProbe_NoTargetErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "probe")
	if err == nil || !strings.Contains(err.Error(), "specify a <label> or --all") {
		t.Fatalf("expected 'specify a label or --all', got %v", err)
	}
}

func TestProbe_AllAndLabelMutuallyExclusive(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	_, err := runCLI(t, "", "probe", "--all", "work")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestProbe_UnknownLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "probe", "ghost")
	if err == nil {
		t.Fatalf("expected error for unknown label, got nil")
	}
}

func TestProbe_AllOnEmptyStore(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "probe", "--all")
	if err != nil {
		t.Fatalf("probe --all on empty store: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No accounts to probe") {
		t.Errorf("output = %q, want 'No accounts to probe'", out)
	}
}
