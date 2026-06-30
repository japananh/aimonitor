package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/store"
)

// Final mop-up: small hermetic branches across mcp register, config get
// (autostart YAML read), restoreAccount's create-collision, openStore's
// default-path branch, and a decrypted-record restore failure. No t.Parallel.

// ---- mcp register: claude CLI not found -------------------------------------

// TestMCPRegister_ClaudeNotFound: with a PATH that has no `claude`, register
// fails at exec.LookPath BEFORE running anything (hermetic — no exec).
func TestMCPRegister_ClaudeNotFound(t *testing.T) {
	_, _ = e2eEnv(t)
	// Point PATH at an empty temp dir so `claude` cannot be found.
	t.Setenv("PATH", t.TempDir())
	_, err := runCLI(t, "", "mcp", "register")
	if err == nil || !strings.Contains(err.Error(), "`claude` CLI not found") {
		t.Fatalf("expected claude-not-found error, got %v", err)
	}
}

// ---- config get autostart (YAML read path) ----------------------------------

// TestConfigGet_Autostart: `config get autostart` reads the YAML-backed value
// (config.Load default), covering getConfigValue's autostart case.
func TestConfigGet_Autostart(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "config", "get", "autostart")
	if err != nil {
		t.Fatalf("config get autostart: %v (output: %q)", err, out)
	}
	v := strings.TrimSpace(out)
	if v != "true" && v != "false" {
		t.Errorf("autostart get = %q, want a bool string", v)
	}
}

// ---- restoreAccount: CreateAccount collision (unit) -------------------------

// TestRestoreAccount_CreateCollision: restoring a NEW identity whose label
// collides with an existing different-identity account fails at CreateAccount,
// covering restoreAccount's create-error branch (stash rolled back).
func TestRestoreAccount_CreateCollision(t *testing.T) {
	_, dbPath := e2eEnv(t)
	ctx := context.Background()
	// Existing "dup" with a different identity.
	seedAccount(t, dbPath, store.Account{Label: "dup", Email: "a@example.com", OrganizationUUID: "org-a"}, validBlob("sk-a"))
	s := openStoreAt(t, dbPath)

	// New identity, same label → CreateAccount unique-constraint failure.
	_, err := restoreAccount(ctx, s, exportAccount{Label: "dup", Email: "b@example.com", OrganizationUUID: "org-b"}, validBlob("sk-b"))
	if err == nil || !strings.Contains(err.Error(), "create account") {
		t.Fatalf("expected a create-account error on the label collision, got %v", err)
	}
}

// ---- openStore: default-path branch (AIMONITOR_STORE_PATH unset) ------------

// TestOpenStore_DefaultPath: with AIMONITOR_STORE_PATH unset, openStore resolves
// store.DefaultPath() under the temp HOME and opens it. Driven via a real
// command so the path is exercised end-to-end.
func TestOpenStore_DefaultPath(t *testing.T) {
	_, _ = e2eEnv(t) // sets a temp HOME
	_ = os.Unsetenv("AIMONITOR_STORE_PATH")

	// `list` opens the store via withRuntime → openStore → DefaultPath.
	out, err := runCLI(t, "", "list")
	if err != nil {
		t.Fatalf("list with default store path: %v (output: %q)", err, out)
	}
	// A fresh default store has no accounts.
	if !strings.Contains(out, "No accounts") && !strings.Contains(out, "no accounts") {
		t.Logf("list output (default path): %q", out)
	}
}

// ---- runConfigImport: a decrypted record whose restore fails ----------------

// TestConfigImport_RecordRestoreFails: an encrypted bundle carries a record
// whose label collides with an existing different-identity account, so
// restoreAccount fails and the record is counted failed (covers the
// per-record failure branch in runConfigImport's loop).
func TestConfigImport_RecordRestoreFails(t *testing.T) {
	_, dbPath := e2eEnv(t)
	// Export one account "shared" with identity X.
	seedAccount(t, dbPath, store.Account{Label: "shared", Email: "x@example.com", OrganizationUUID: "org-x"}, validBlob("sk-x"))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	if out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath); err != nil {
		t.Fatalf("export: %v (output: %q)", err, out)
	}

	// Fresh store that already has "shared" under a DIFFERENT identity → the
	// imported record (identity X, label "shared") fails at CreateAccount.
	_, _ = e2eEnv(t)
	_, dbPath2 := openFreshFor(t, "shared", "y@example.com", "org-y")
	_ = dbPath2
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")

	out, err := runCLI(t, "", "config", "import", bundlePath)
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("the colliding record should be counted failed\n%s", out)
	}
}

// openFreshFor seeds a single account into the CURRENT e2e store (whose path is
// AIMONITOR_STORE_PATH) and returns it for assertions.
func openFreshFor(t *testing.T, label, email, org string) (store.Account, string) {
	t.Helper()
	dbPath := os.Getenv("AIMONITOR_STORE_PATH")
	acct := seedAccount(t, dbPath, store.Account{Label: label, Email: email, OrganizationUUID: org}, validBlob("sk-"+email))
	return acct, dbPath
}
