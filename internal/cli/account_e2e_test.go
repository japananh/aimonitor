package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// These are HERMETIC, CI-safe end-to-end tests that drive the real cobra
// commands against a temp SQLite store + temp HOME, with the TEST-ONLY keyring
// seam (claude.SetKeyringForTest) standing in for the OS Keychain.
//
// No t.Parallel anywhere: the seam (opsOverride) and AIMONITOR_STORE_PATH are
// process globals, and switch/remove touch $HOME/.aimonitor-lock + ~/.claude.json.
//
// `add --adopt-current` reads the live Claude Code slot through
// claude.AdoptCurrent, which routes via sharedOps() — so the SetKeyringForTest
// seam redirects it too. The live slot is seeded with
// ring.Set(ClaudeCodeService, KeychainUserForTest, blob) and the identity via a
// ~/.claude.json under the temp HOME.

// e2eEnv installs the in-memory keyring seam, points the CLI at a temp store,
// and isolates HOME (for the lock file + ~/.claude.json). Returns the memory
// keyring (so a test can seed/read the live Claude Code slot directly) and the
// store-file path.
func e2eEnv(t *testing.T) (*secret.MemoryKeyring, string) {
	t.Helper()
	ring := secret.NewMemoryKeyring()
	// Install BOTH keyring seams pointing at the SAME in-memory ring: the claude
	// package's ops (stash helpers + Provider live slot) and secret.Default
	// (used by the MCP creds, doctor's keyring check, and import). A test that
	// set only one would get a fake on one path and the real keychain on the
	// other.
	t.Cleanup(claude.SetKeyringForTest(ring))
	t.Cleanup(secret.SetDefaultForTest(ring))

	dbPath := filepath.Join(t.TempDir(), "store.db")
	t.Setenv("AIMONITOR_STORE_PATH", dbPath)
	t.Setenv("HOME", t.TempDir())
	return ring, dbPath
}

// runCLI builds a fresh root command, runs it with args, and returns combined
// stdout+stderr and the command error. stdin is optional (for prompts).
func runCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}
	err := cmd.Execute()
	return out.String(), err
}

// seedAccount opens the temp store, inserts an account row, and stashes blob
// under a fresh keyring ref via the seam. The store handle is closed before
// returning so the command under test gets a clean connection (SQLite file DBs
// are shared across connections; closing avoids "database is locked").
func seedAccount(t *testing.T, dbPath string, acct store.Account, blob []byte) store.Account {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	defer s.Close()
	if acct.KeyringRef == "" {
		acct.KeyringRef = "ref-" + acct.Label
	}
	created, err := s.CreateAccount(ctx, acct)
	if err != nil {
		t.Fatalf("seed account %q: %v", acct.Label, err)
	}
	if len(blob) > 0 {
		if err := claude.StashCredential(ctx, created.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
			t.Fatalf("seed stash for %q: %v", acct.Label, err)
		}
	}
	return created
}

// validBlob is a syntactically-valid Claude OAuth blob with a far-future expiry
// so switch never needs a network refresh.
func validBlob(accessToken string) []byte {
	b, err := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  accessToken,
			"refreshToken": "rtok-" + accessToken,
			"expiresAt":    time.Now().Add(24 * time.Hour).UnixMilli(),
		},
	})
	if err != nil {
		panic(err)
	}
	return b
}

// openStoreAt re-opens the temp store for assertions.
func openStoreAt(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store for assert: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---- add --------------------------------------------------------------------

// TestAdd_DuplicateLabelRejected: `add --adopt-current --label X` when an
// account labelled X already exists must fail with an "already exists" error,
// BEFORE touching the keyring (the collision check precedes the adopt branch).
func TestAdd_DuplicateLabelRejected(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk-work"))

	out, err := runCLI(t, "", "add", "--adopt-current", "--label", "work")
	if err == nil {
		t.Fatalf("expected duplicate-label error, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to mention 'already exists'", err.Error())
	}
}

// seedClaudeJSON writes a ~/.claude.json identity under the temp HOME so
// resolveIdentity (called by `add`) captures email/org.
func seedClaudeJSON(t *testing.T, email, orgUUID, orgName string) {
	t.Helper()
	cc := claudeconfig.NewAt(filepath.Join(os.Getenv("HOME"), ".claude.json"))
	if err := cc.WriteOAuthAccount(context.Background(), claudeconfig.OAuthAccount{
		EmailAddress:     email,
		OrganizationUUID: orgUUID,
		OrganizationName: orgName,
	}); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}
}

// TestAdd_AdoptCurrent_HappyPath: a credential in the live slot + an identity in
// ~/.claude.json → `add --adopt-current` registers a new account carrying that
// identity, with a stash holding the adopted blob.
func TestAdd_AdoptCurrent_HappyPath(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()

	live := validBlob("sk-adopt")
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, live); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	seedClaudeJSON(t, "adopt@example.com", "org-adopt", "Adopt Org")

	out, err := runCLI(t, "", "add", "--adopt-current", "--label", "personal")
	if err != nil {
		t.Fatalf("add --adopt-current: %v (output: %q)", err, out)
	}

	s := openStoreAt(t, dbPath)
	acct, err := s.GetAccountByLabel(ctx, "personal")
	if err != nil {
		t.Fatalf("account 'personal' should exist after adopt: %v", err)
	}
	if acct.Email != "adopt@example.com" || acct.OrganizationUUID != "org-adopt" {
		t.Errorf("identity = %q/%q, want adopt@example.com/org-adopt", acct.Email, acct.OrganizationUUID)
	}
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("stash should exist for the adopted account: %v", err)
	}
	if !bytes.Equal(stash.Bytes, live) {
		t.Errorf("stashed blob does not match the adopted live blob")
	}
}

// TestAdd_AdoptCurrent_ReAddSameIdentityRefreshes: re-adding under a new label
// when the live identity already maps to an existing account refreshes that
// account's stash instead of creating a duplicate.
func TestAdd_AdoptCurrent_ReAddSameIdentityRefreshes(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()

	existing := seedAccount(t, dbPath, store.Account{
		Label:            "old",
		Email:            "dup@example.com",
		OrganizationUUID: "org-dup",
		OrganizationName: "Dup Org",
	}, validBlob("sk-old"))

	// Live slot carries a NEW blob but the SAME identity as "old".
	live := validBlob("sk-new")
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, live); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	seedClaudeJSON(t, "dup@example.com", "org-dup", "Dup Org")

	out, err := runCLI(t, "", "add", "--adopt-current", "--label", "new")
	if err != nil {
		t.Fatalf("re-add: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "already registered") {
		t.Errorf("output = %q, want it to note the identity is already registered", out)
	}

	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "new"); err == nil {
		t.Errorf("re-adding the same identity must NOT create a 'new' account")
	}
	stash, err := claude.RetrieveStash(ctx, existing.KeyringRef)
	if err != nil {
		t.Fatalf("existing stash should still exist: %v", err)
	}
	if !bytes.Equal(stash.Bytes, live) {
		t.Errorf("existing account's stash should have been refreshed to the adopted blob")
	}
}

// ---- switch -----------------------------------------------------------------

// TestSwitch_MakesAccountActive: seed a target account (row + stash), run
// `switch <label>`, and assert it becomes the active account (the live slot
// byte-matches its stash, which is what ResolveActiveAccount reports).
func TestSwitch_MakesAccountActive(t *testing.T) {
	_, dbPath := e2eEnv(t)
	blob := validBlob("sk-target")
	target := seedAccount(t, dbPath, store.Account{
		Label:            "target",
		Email:            "target@example.com",
		OrganizationUUID: "org-1",
		OrganizationName: "Org One",
	}, blob)

	out, err := runCLI(t, "", "switch", "target")
	if err != nil {
		t.Fatalf("switch: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Switched to") {
		t.Errorf("output = %q, want a 'Switched to' confirmation", out)
	}

	// Assert active via the same resolver the daemon/status uses.
	s := openStoreAt(t, dbPath)
	p, err := provider.Lookup(claude.Name)
	if err != nil {
		t.Fatalf("provider.Lookup: %v", err)
	}
	cc := claudeconfig.NewAt(filepath.Join(t.TempDir(), "unused.json"))
	active, found, err := daemon.ResolveActiveAccount(context.Background(), s, p, cc)
	if err != nil {
		t.Fatalf("ResolveActiveAccount: %v", err)
	}
	if !found || active.ID != target.ID {
		t.Errorf("resolved active = (%+v, found=%v), want target id=%d", active, found, target.ID)
	}
}

// TestSwitch_UnknownLabelErrors: `switch <unknown>` must error.
func TestSwitch_UnknownLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)

	out, err := runCLI(t, "", "switch", "ghost")
	if err == nil {
		t.Fatalf("expected error for unknown label, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "no account") {
		t.Errorf("error = %q, want it to name the unknown label", err.Error())
	}
}

// ---- remove -----------------------------------------------------------------

// TestRemove_DeletesRowAndStash: seed a NON-active account (row + stash), run
// `remove <label> --yes`, and assert both the row and the stash are gone.
func TestRemove_DeletesRowAndStash(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	acct := seedAccount(t, dbPath, store.Account{Label: "gone"}, validBlob("sk-gone"))

	// Make a DIFFERENT account active so "gone" is provably inactive: write a
	// distinct blob into the live Claude Code slot via the seam.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-other")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "remove", "gone", "--yes")
	if err != nil {
		t.Fatalf("remove: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("output = %q, want a 'Removed' confirmation", out)
	}

	// Row gone.
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(context.Background(), "gone"); err == nil {
		t.Errorf("account row should be gone after remove")
	}
	// Stash gone.
	if _, err := claude.RetrieveStash(context.Background(), acct.KeyringRef); err == nil {
		t.Errorf("stash should be gone after remove")
	}
}

// TestRemove_RefusesActiveAccount: removing the ACTIVE account must be refused.
// Active = the account whose stash byte-matches the live slot.
func TestRemove_RefusesActiveAccount(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	blob := validBlob("sk-active")
	acct := seedAccount(t, dbPath, store.Account{Label: "active"}, blob)

	// Live slot == this account's stash → it is the active account.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blob); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "remove", "active", "--yes")
	if err == nil {
		t.Fatalf("expected refusal removing the active account, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "active account") {
		t.Errorf("error = %q, want it to refuse the active account", err.Error())
	}

	// The account must still exist (and its stash too).
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(context.Background(), "active"); err != nil {
		t.Errorf("active account should NOT have been removed: %v", err)
	}
	if _, err := claude.RetrieveStash(context.Background(), acct.KeyringRef); err != nil {
		t.Errorf("active account stash should NOT have been removed: %v", err)
	}
}

// TestRemove_UnknownLabelErrors: `remove <unknown> --yes` must error.
func TestRemove_UnknownLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)

	out, err := runCLI(t, "", "remove", "ghost", "--yes")
	if err == nil {
		t.Fatalf("expected error for unknown label, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "no account") {
		t.Errorf("error = %q, want it to name the unknown label", err.Error())
	}
}
