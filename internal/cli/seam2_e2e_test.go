package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/store"
)

// More hermetic coverage for the reachable error/edge branches not hit by the
// happy-path tests. No t.Parallel, no real keychain/network/exec.

// ---- runAdd: AdoptCurrent error when the live slot is empty -----------------

// TestAdd_AdoptCurrent_EmptySlotErrors: `add --adopt-current` with NO bytes in
// the live Claude Code slot → AdoptCurrent returns the "nothing to adopt" error,
// covering runAdd's adopt-error branch.
func TestAdd_AdoptCurrent_EmptySlotErrors(t *testing.T) {
	_, _ = e2eEnv(t) // empty in-memory ring → live slot is empty
	_, err := runCLI(t, "", "add", "--adopt-current", "--label", "nope")
	if err == nil || !strings.Contains(err.Error(), "no credential to adopt") {
		t.Fatalf("expected 'no credential to adopt' error, got %v", err)
	}
}

// ---- runConfigExport: stdout (no --out) + skip-missing-stash on tokens ------

// TestConfigExport_ToStdout: with no --out, the bundle is printed to stdout.
func TestConfigExport_ToStdout(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, validBlob("sk"))

	out, err := runCLI(t, "", "config", "export")
	if err != nil {
		t.Fatalf("config export to stdout: %v (output: %q)", err, out)
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("stdout export is not valid JSON: %v\n%s", err, out)
	}
	if b["version"] == nil {
		t.Errorf("stdout bundle missing version: %v", b)
	}
}

// TestConfigExport_IncludeTokens_SkipsMissingStash: an account row whose stash
// is absent is skipped during a token export (the warning branch), while the
// account WITH a stash is included.
func TestConfigExport_IncludeTokens_SkipsMissingStash(t *testing.T) {
	_, dbPath := e2eEnv(t)
	// "good" has a stash; "orphan" has a KeyringRef but no stashed bytes.
	seedAccount(t, dbPath, store.Account{Label: "good", Email: "g@example.com"}, validBlob("sk-good"))
	seedAccount(t, dbPath, store.Account{Label: "orphan", Email: "o@example.com", KeyringRef: "ref-orphan"}, nil)

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath)
	if err != nil {
		t.Fatalf("export --include-tokens: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "skip token for \"orphan\"") {
		t.Errorf("the stash-less account should be skipped with a warning\n%s", out)
	}
}

// ---- runConfigImport: multi-record encrypted bundle (added + refreshed) -----

// TestConfigImport_EncryptedMultiRecord drives runConfigImport's per-record loop
// over BOTH branches: one identity is new (added) and one already exists
// (refreshed). The bundle is produced by the real export path.
func TestConfigImport_EncryptedMultiRecord(t *testing.T) {
	_, dbPath := e2eEnv(t)
	ctx := context.Background()
	seedAccount(t, dbPath, store.Account{Label: "alpha", Email: "a@example.com", OrganizationUUID: "org-a"}, validBlob("sk-a"))
	seedAccount(t, dbPath, store.Account{Label: "bravo", Email: "b@example.com", OrganizationUUID: "org-b"}, validBlob("sk-b"))

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	if out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath); err != nil {
		t.Fatalf("export: %v (output: %q)", err, out)
	}

	// Fresh store that ALREADY has 'alpha' (so it refreshes) but not 'bravo'.
	_, dbPath2 := e2eEnv(t)
	seedAccount(t, dbPath2, store.Account{Label: "alpha-old", Email: "a@example.com", OrganizationUUID: "org-a"}, validBlob("sk-a2"))
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")

	out, err := runCLI(t, "", "config", "import", bundlePath)
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "1 added, 1 refreshed, 0 failed") {
		t.Errorf("expected 1 added (bravo) + 1 refreshed (alpha)\n%s", out)
	}
	s := openStoreAt(t, dbPath2)
	if _, err := s.GetAccountByLabel(ctx, "bravo"); err != nil {
		t.Errorf("bravo should have been added: %v", err)
	}
}

// TestConfigImport_WrongPassphraseFails: an encrypted bundle imported with the
// wrong passphrase fails at decryptTokens (the runConfigImport decrypt-error
// branch).
func TestConfigImport_WrongPassphraseFails(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, validBlob("sk"))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "right-pass")
	if out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath); err != nil {
		t.Fatalf("export: %v (output: %q)", err, out)
	}

	_, _ = e2eEnv(t)
	t.Setenv("AIMONITOR_PASSPHRASE", "wrong-pass")
	_, err := runCLI(t, "", "config", "import", bundlePath)
	if err == nil || !strings.Contains(err.Error(), "decrypt failed") {
		t.Fatalf("expected decrypt-failed error with the wrong passphrase, got %v", err)
	}
}

// TestConfigImport_EncryptedMissingPassphrase: an encrypted bundle without any
// passphrase available errors at resolvePassphrase.
func TestConfigImport_EncryptedMissingPassphrase(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, validBlob("sk"))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	if out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath); err != nil {
		t.Fatalf("export: %v (output: %q)", err, out)
	}

	_, _ = e2eEnv(t)
	_ = os.Unsetenv("AIMONITOR_PASSPHRASE")
	_, err := runCLI(t, "", "config", "import", bundlePath)
	if err == nil || !strings.Contains(err.Error(), "passphrase is required") {
		t.Fatalf("expected passphrase-required error, got %v", err)
	}
}

// ---- restoreAccount lookup-error (default) branch ---------------------------

// TestRestoreAccount_LookupError: a closed store makes GetAccountByIdentity
// return a non-not-found error, exercising restoreAccount's default branch.
func TestRestoreAccount_LookupError(t *testing.T) {
	_, dbPath := e2eEnv(t)
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = s.Close() // closed → queries error

	_, rerr := restoreAccount(context.Background(), s, exportAccount{Label: "x", Email: "x@example.com"}, validBlob("sk"))
	if rerr == nil || !strings.Contains(rerr.Error(), "look up") {
		t.Fatalf("expected a lookup error from the closed store, got %v", rerr)
	}
}

// ---- doctor: account with no usage yet (the "no usage yet" branch) ----------

// TestDoctor_AccountNoUsage: a healthy run where the account has NO oauth_usage
// row exercises runDoctor's ErrLimitsNotFound per-account branch ("no usage
// yet") — the expected state before the daemon's first usage fetch.
func TestDoctor_AccountNoUsage(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "fresh"}, validBlob("sk-fresh"))

	out, err := runCLI(t, "", "doctor")
	if err != nil {
		t.Fatalf("doctor: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "usage fresh") || !strings.Contains(out, "no usage yet") {
		t.Errorf("doctor should report 'no usage yet' for an unfetched account\n%s", out)
	}
}
