package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// These hermetic e2e tests exercise the code paths that the dual keyring seam
// (claude.SetKeyringForTest + secret.SetDefaultForTest, installed by e2eEnv)
// now makes reachable without touching the real OS keychain or the network:
// the MCP connect/disconnect/status credential plumbing, `import`'s credential
// read + stash, and doctor's keyring round-trip.
//
// Slack token verification is hermetic for BOT tokens (xoxp-/xoxe- are user
// tokens; xoxb- is a bot token rejected by VerifySlack BEFORE any network call),
// so an "xoxb-…" token drives the verify branch with no I/O. ClickUp and the
// verify-SUCCESS path always need the network and are left uncovered on purpose.
//
// No t.Parallel: the seams + AIMONITOR_STORE_PATH are process globals.

// osUser is the account field NewCredStore (and import) use when reading/writing
// our keyring entries.
func osUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	return u.Username
}

// mcpService is the keychain service name aimonitor stores its own copy of an
// integration token under (mirrors mcpserver.keyringService).
func mcpService(svc string) string { return "aimonitor-mcp:" + svc }

// errKeyring is a secret.Keyring whose ops fail, to drive the "keychain error"
// branches (doctor's checkKeyring Set/Get failures, mcp status's read error).
// failGet/failSet pick which op errors; the others succeed via the delegate.
type errKeyring struct {
	delegate   secret.Keyring
	failGet    bool
	failSet    bool
	failDelete bool
}

func (e *errKeyring) Get(service, account string) ([]byte, error) {
	if e.failGet {
		return nil, fmt.Errorf("boom: get %s/%s", service, account)
	}
	return e.delegate.Get(service, account)
}

func (e *errKeyring) Set(service, account string, data []byte) error {
	if e.failSet {
		return fmt.Errorf("boom: set %s/%s", service, account)
	}
	return e.delegate.Set(service, account, data)
}

func (e *errKeyring) Delete(service, account string) error {
	if e.failDelete {
		return fmt.Errorf("boom: delete %s/%s", service, account)
	}
	return e.delegate.Delete(service, account)
}

// ---- mcp disconnect (fully hermetic: delete from the faked keyring) ---------

// TestMCPDisconnect_RemovesStoredToken seeds a token under aimonitor's MCP
// service, disconnects, and asserts it is gone from the (faked) keychain.
func TestMCPDisconnect_RemovesStoredToken(t *testing.T) {
	ring, _ := e2eEnv(t)
	if err := ring.Set(mcpService("slack"), osUser(t), []byte("xoxp-seed")); err != nil {
		t.Fatalf("seed mcp token: %v", err)
	}

	out, err := runCLI(t, "", "mcp", "disconnect", "slack")
	if err != nil {
		t.Fatalf("mcp disconnect: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Disconnected slack") {
		t.Errorf("output = %q, want a 'Disconnected slack' confirmation", out)
	}
	if _, gerr := ring.Get(mcpService("slack"), osUser(t)); gerr == nil {
		t.Errorf("token should be gone from the keychain after disconnect")
	}
}

// TestMCPDisconnect_Idempotent: disconnecting a service with no stored token is
// not an error (Delete swallows ErrNotFound).
func TestMCPDisconnect_Idempotent(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "mcp", "disconnect", "clickup")
	if err != nil {
		t.Fatalf("disconnect with no token should be a no-op, got: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Disconnected clickup") {
		t.Errorf("output = %q, want a 'Disconnected clickup' confirmation", out)
	}
}

// ---- mcp status (text + json) -----------------------------------------------

// TestMCPStatus_NotConnected_Text: with no tokens stored, status reports both
// services as not connected and lists the exposed tools (no verify, no network).
func TestMCPStatus_NotConnected_Text(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "mcp", "status")
	if err != nil {
		t.Fatalf("mcp status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "slack") || !strings.Contains(out, "not connected") {
		t.Errorf("status should list slack as not connected\n%s", out)
	}
	if !strings.Contains(out, "Tools exposed") {
		t.Errorf("status should report the exposed tools\n%s", out)
	}
}

// TestMCPStatus_NotConnected_JSON: the --json branch emits a decodable object
// with a services array and a (possibly empty but non-null) tools array.
func TestMCPStatus_NotConnected_JSON(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "mcp", "status", "--json")
	if err != nil {
		t.Fatalf("mcp status --json: %v (output: %q)", err, out)
	}
	var doc struct {
		Services []struct {
			Service   string `json:"service"`
			Connected bool   `json:"connected"`
			Enabled   bool   `json:"enabled"`
		} `json:"services"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Services) != 3 {
		t.Errorf("expected slack + clickup + sentry in services, got %+v", doc.Services)
	}
	for _, s := range doc.Services {
		if s.Connected {
			t.Errorf("service %q should not be connected with no token", s.Service)
		}
	}
	if doc.Tools == nil {
		t.Errorf("tools must serialise as [] not null")
	}
}

// TestMCPStatus_ConnectedButVerifyFails_Text seeds a BOT token (xoxb-) which
// VerifySlack rejects with no network, exercising status's connected→verify-
// failed sub-branch in the text path.
func TestMCPStatus_ConnectedButVerifyFails_Text(t *testing.T) {
	ring, _ := e2eEnv(t)
	if err := ring.Set(mcpService("slack"), osUser(t), []byte("xoxb-bot-token")); err != nil {
		t.Fatalf("seed bot token: %v", err)
	}
	out, err := runCLI(t, "", "mcp", "status")
	if err != nil {
		t.Fatalf("mcp status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "verification failed") {
		t.Errorf("status should report verification failure for a bot token\n%s", out)
	}
}

// TestMCPStatus_ConnectedButVerifyFails_JSON: same as above through the --json
// branch, which records the verify error in the per-service "error" field.
func TestMCPStatus_ConnectedButVerifyFails_JSON(t *testing.T) {
	ring, _ := e2eEnv(t)
	if err := ring.Set(mcpService("slack"), osUser(t), []byte("xoxb-bot-token")); err != nil {
		t.Fatalf("seed bot token: %v", err)
	}
	out, err := runCLI(t, "", "mcp", "status", "--json")
	if err != nil {
		t.Fatalf("mcp status --json: %v (output: %q)", err, out)
	}
	var doc struct {
		Services []struct {
			Service   string `json:"service"`
			Connected bool   `json:"connected"`
			Error     string `json:"error"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	var slackSeen bool
	for _, s := range doc.Services {
		if s.Service == "slack" {
			slackSeen = true
			if !s.Connected {
				t.Errorf("slack should be marked connected (token present), got %+v", s)
			}
			if !strings.Contains(s.Error, "bot token") {
				t.Errorf("slack error should mention the bot token rejection, got %q", s.Error)
			}
		}
	}
	if !slackSeen {
		t.Errorf("slack missing from services: %+v", doc.Services)
	}
}

// TestMCPStatus_KeychainError_Text injects an erroring keyring so creds.Token
// returns an error, exercising the "keychain error" state branch.
func TestMCPStatus_KeychainError_Text(t *testing.T) {
	ring, _ := e2eEnv(t)
	// Install an erroring ring on TOP of the e2e seam (LIFO cleanup is fine).
	t.Cleanup(secret.SetDefaultForTest(&errKeyring{delegate: ring, failGet: true}))

	out, err := runCLI(t, "", "mcp", "status")
	if err != nil {
		t.Fatalf("mcp status with keychain error: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "keychain error") {
		t.Errorf("status should surface the keychain read error\n%s", out)
	}
}

// ---- mcp connect (hermetic verify via bot token / empty stdin) --------------

// TestMCPConnect_TokenFlag_VerifyFails: `--token xoxb-…` reaches the verifier,
// which rejects a bot token without any network call.
func TestMCPConnect_TokenFlag_VerifyFails(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "mcp", "connect", "slack", "--token", "xoxb-bot")
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("expected token verification failure, got %v", err)
	}
}

// TestMCPConnect_PastedToken_VerifyFails: no claude-bar entry → falls through to
// the stdin prompt; a pasted bot token fails verification (no network).
func TestMCPConnect_PastedToken_VerifyFails(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "xoxb-pasted\n", "mcp", "connect", "slack")
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("expected pasted-token verification failure, got %v (output: %q)", err, out)
	}
	// The Slack-scopes preamble and the claude-bar fallback messaging should
	// have been printed before the prompt.
	if !strings.Contains(out, "scopes required") {
		t.Errorf("connect should print the required Slack scopes\n%s", out)
	}
}

// TestMCPConnect_EmptyStdin_NoTokenEntered: empty stdin → the "no token entered"
// error, after the claude-bar fallback path (no network, no verify).
func TestMCPConnect_EmptyStdin_NoTokenEntered(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "\n", "mcp", "connect", "clickup")
	if err == nil || !strings.Contains(err.Error(), "no token entered") {
		t.Fatalf("expected 'no token entered', got %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No claude-bar token found") {
		t.Errorf("connect should note no claude-bar token was found\n%s", out)
	}
}

// TestMCPConnect_StdinReadError: stdin that ends with no newline makes the
// prompt's ReadString('\n') return an error, covering connect's read-token error
// branch (clickup never verifies, so no network).
func TestMCPConnect_StdinReadError(t *testing.T) {
	_, _ = e2eEnv(t)
	// "noeol" has no trailing newline → bufio.ReadString('\n') returns io.EOF.
	out, err := runCLI(t, "noeol", "mcp", "connect", "clickup")
	if err == nil || !strings.Contains(err.Error(), "read token") {
		t.Fatalf("expected a read-token error on a newline-less stdin, got %v (output: %q)", err, out)
	}
}

// TestMCPConnect_MigratesFromClaudeBar: a bot token in claude-bar's slot is read
// (migration path) and fails verification before any network — covering
// MigrateFromClaudeBar's read + verify-fail branch through the CLI.
func TestMCPConnect_MigratesFromClaudeBar_VerifyFails(t *testing.T) {
	ring, _ := e2eEnv(t)
	// claude-bar's service name for slack (mcpserver.claudeBarService).
	if err := ring.Set("claude-bar-mcp:shared:slack", osUser(t), []byte("xoxb-bar")); err != nil {
		t.Fatalf("seed claude-bar token: %v", err)
	}
	out, err := runCLI(t, "\n", "mcp", "connect", "slack")
	// Migration verify fails (bot token) → printed as "migration unavailable",
	// then falls to the stdin prompt; empty stdin → no token entered.
	if err == nil || !strings.Contains(err.Error(), "no token entered") {
		t.Fatalf("expected fall-through to prompt after failed migration, got %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "migration unavailable") {
		t.Errorf("a failed claude-bar migration should be reported\n%s", out)
	}
}

// ---- import (credential read + stash; now hermetic via the seam) ------------

// seedImportRegistryAndCreds writes a claude-bar registry under the temp HOME
// and seeds each account's backup credential in the faked keyring.
func seedImportRegistryAndCreds(t *testing.T, ring *secret.MemoryKeyring, accts []cbAccount) {
	t.Helper()
	reg := cbRegistry{Accounts: map[string]cbAccount{}}
	for i, a := range accts {
		reg.Accounts[fmt.Sprintf("a%d", i)] = a
		svc := fmt.Sprintf(claudeBarBackupServiceFmt, a.Number, a.Email)
		if err := ring.Set(svc, osUser(t), validBlob("sk-"+a.Email)); err != nil {
			t.Fatalf("seed backup cred for %s: %v", a.Email, err)
		}
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	writeClaudeBarRegistry(t, string(raw))
}

// TestImport_HappyPath_AddsAndDisablesAutoSwap imports two fresh accounts: each
// credential is read from the faked keychain, stashed, and a row created; and
// auto-swap is disabled by default.
func TestImport_HappyPath_AddsAndDisablesAutoSwap(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	seedImportRegistryAndCreds(t, ring, []cbAccount{
		{Number: 1, Email: "one@example.com", OrganizationUUID: "org-1", OrganizationName: "Org One", Nickname: "first"},
		{Number: 2, Email: "two@example.com", OrganizationUUID: "org-2", OrganizationName: "Org Two"},
	})

	out, err := runCLI(t, "", "import")
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Imported one@example.com as \"first\"") {
		t.Errorf("output should report the nicknamed account imported\n%s", out)
	}
	if !strings.Contains(out, "Imported two@example.com as \"two\"") {
		t.Errorf("output should fall back to the email local part as label\n%s", out)
	}
	if !strings.Contains(out, "2 added, 0 refreshed, 0 failed") {
		t.Errorf("summary = wrong\n%s", out)
	}
	if !strings.Contains(out, "Auto-swap disabled") {
		t.Errorf("import should disable auto-swap by default\n%s", out)
	}

	s := openStoreAt(t, dbPath)
	acct, err := s.GetAccountByLabel(ctx, "first")
	if err != nil {
		t.Fatalf("imported account 'first' should exist: %v", err)
	}
	if acct.Email != "one@example.com" || acct.OrganizationUUID != "org-1" {
		t.Errorf("identity = %q/%q, want one@example.com/org-1", acct.Email, acct.OrganizationUUID)
	}
	if _, err := claude.RetrieveStash(ctx, acct.KeyringRef); err != nil {
		t.Errorf("imported account should carry a stash: %v", err)
	}
	if v, _ := s.GetSetting(ctx, "auto_swap.enabled"); v != "false" {
		t.Errorf("auto_swap.enabled = %q, want false", v)
	}
}

// TestImport_RefreshesExistingAndRelabels: an account already registered under
// the same identity is refreshed + relabeled to the claude-bar nickname, and
// auto-swap is kept when --keep-auto-swap is passed.
func TestImport_RefreshesExistingAndRelabels(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	// Pre-existing account with the same identity but a different label.
	seedAccount(t, dbPath, store.Account{
		Label: "oldlabel", Email: "dup@example.com", OrganizationUUID: "org-d", OrganizationName: "Dup Org",
	}, validBlob("sk-old"))
	seedImportRegistryAndCreds(t, ring, []cbAccount{
		{Number: 5, Email: "dup@example.com", OrganizationUUID: "org-d", OrganizationName: "Dup Org", Nickname: "newlabel"},
	})

	out, err := runCLI(t, "", "import", "--keep-auto-swap")
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Refreshed dup@example.com") || !strings.Contains(out, "relabeled") {
		t.Errorf("output should report a refresh + relabel\n%s", out)
	}
	if !strings.Contains(out, "0 added, 1 refreshed, 0 failed") {
		t.Errorf("summary wrong\n%s", out)
	}
	if strings.Contains(out, "Auto-swap disabled") {
		t.Errorf("--keep-auto-swap must NOT disable auto-swap\n%s", out)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "newlabel"); err != nil {
		t.Errorf("account should have been relabeled to the nickname: %v", err)
	}
}

// TestImport_SkipsAccountWithMissingCredential: an account in the registry whose
// backup credential is absent from the keychain is reported failed and skipped,
// while a sibling with a credential still imports.
func TestImport_SkipsAccountWithMissingCredential(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	// Write the registry with two accounts but seed a credential for only one.
	reg := cbRegistry{Accounts: map[string]cbAccount{
		"a": {Number: 1, Email: "good@example.com", OrganizationUUID: "org-g", Nickname: "good"},
		"b": {Number: 2, Email: "missing@example.com", OrganizationUUID: "org-m", Nickname: "missing"},
	}}
	if err := ring.Set(fmt.Sprintf(claudeBarBackupServiceFmt, 1, "good@example.com"), osUser(t), validBlob("sk-good")); err != nil {
		t.Fatalf("seed good cred: %v", err)
	}
	raw, _ := json.Marshal(reg)
	writeClaudeBarRegistry(t, string(raw))

	out, err := runCLI(t, "", "import")
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "skip missing@example.com") {
		t.Errorf("missing-credential account should be skipped\n%s", out)
	}
	if !strings.Contains(out, "1 added, 0 refreshed, 1 failed") {
		t.Errorf("summary wrong\n%s", out)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "good"); err != nil {
		t.Errorf("the account with a credential should still import: %v", err)
	}
}

// ---- doctor (keyring round-trip + healthy tail; failing keyring) ------------

// TestDoctor_HealthyRun: full runDoctor with a healthy faked keyring + a seeded
// account carrying an oauth_usage snapshot, so every check (config, sqlite,
// provider, jsonl, keyring, accounts, per-account usage) is green.
func TestDoctor_HealthyRun(t *testing.T) {
	_, dbPath := e2eEnv(t)
	ctx := context.Background()
	acct := seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk-work"))
	s := openStoreAt(t, dbPath)
	if err := s.PutLimits(ctx, acct.ID, provider.Limits{
		FiveHourPct: 42,
		SevenDayPct: 17,
	}); err != nil {
		t.Fatalf("seed oauth_usage: %v", err)
	}

	out, err := runCLI(t, "", "doctor")
	if err != nil {
		t.Fatalf("doctor should pass on a healthy hermetic env: %v (output: %q)", err, out)
	}
	for _, want := range []string{"keyring", "round-trip ok", "sqlite open", "claude provider", "usage work"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q\n%s", want, out)
		}
	}
}

// TestDoctor_KeyringSetFails: an erroring keyring makes checkKeyring's Set step
// fail, so runDoctor reports a failed check and returns a non-nil error.
func TestDoctor_KeyringSetFails(t *testing.T) {
	ring, _ := e2eEnv(t)
	t.Cleanup(secret.SetDefaultForTest(&errKeyring{delegate: ring, failSet: true}))

	out, err := runCLI(t, "", "doctor")
	if err == nil {
		t.Fatalf("doctor should fail when the keyring Set fails (output: %q)", out)
	}
	if !strings.Contains(out, "✗ keyring") || !strings.Contains(out, "Set:") {
		t.Errorf("doctor should mark the keyring check failed with a Set error\n%s", out)
	}
}

// TestCheckKeyring_Healthy / Failing exercise the helper directly.
func TestCheckKeyring_Healthy(t *testing.T) {
	_, _ = e2eEnv(t)
	c := checkKeyring(context.Background())
	if !c.ok || !strings.Contains(c.detail, "round-trip ok") {
		t.Errorf("healthy keyring check = %+v, want ok round-trip", c)
	}
}

func TestCheckKeyring_GetFails(t *testing.T) {
	ring, _ := e2eEnv(t)
	t.Cleanup(secret.SetDefaultForTest(&errKeyring{delegate: ring, failGet: true}))
	c := checkKeyring(context.Background())
	if c.ok || !strings.Contains(c.detail, "Get:") {
		t.Errorf("get-failing keyring check = %+v, want not-ok with Get error", c)
	}
}

// ---- runAdd adopt branches (no new seam needed, but exercises more paths) ----

// TestAdd_AdoptCurrent_EmailFlagOnly: with NO ~/.claude.json identity, --email
// supplies the identity fallback (resolveIdentity's emailFlag branch); since the
// email IS new, the account is created carrying just that email.
func TestAdd_AdoptCurrent_EmailFlagOnly(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-x")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	// No seedClaudeJSON → ~/.claude.json absent; --email is the only identity.
	out, err := runCLI(t, "", "add", "--adopt-current", "--label", "byemail", "--email", "me@example.com")
	if err != nil {
		t.Fatalf("add --adopt-current --email: %v (output: %q)", err, out)
	}
	s := openStoreAt(t, dbPath)
	acct, err := s.GetAccountByLabel(ctx, "byemail")
	if err != nil {
		t.Fatalf("account should exist: %v", err)
	}
	if acct.Email != "me@example.com" {
		t.Errorf("email = %q, want me@example.com from --email", acct.Email)
	}
}

// TestAdd_AdoptCurrent_NoIdentity: no ~/.claude.json AND no --email → the
// identity-dedup block is skipped (ident.Email == "") and a row with empty
// identity is created. Covers runAdd's create-with-empty-identity branch.
func TestAdd_AdoptCurrent_NoIdentity(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-noid")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	out, err := runCLI(t, "", "add", "--adopt-current", "--label", "noident")
	if err != nil {
		t.Fatalf("add --adopt-current (no identity): %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "added") {
		t.Errorf("output = %q, want an 'added' confirmation", out)
	}
	s := openStoreAt(t, dbPath)
	acct, err := s.GetAccountByLabel(ctx, "noident")
	if err != nil {
		t.Fatalf("account should exist with empty identity: %v", err)
	}
	if acct.Email != "" {
		t.Errorf("email should be empty with no identity source, got %q", acct.Email)
	}
}

// TestAdd_LabelFromPrompt: with --label omitted, the label is read from stdin
// (promptLine). Drives the prompt branch of runAdd.
func TestAdd_LabelFromPrompt(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-prompt")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	out, err := runCLI(t, "prompted\n", "add", "--adopt-current")
	if err != nil {
		t.Fatalf("add with prompted label: %v (output: %q)", err, out)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "prompted"); err != nil {
		t.Errorf("account 'prompted' should exist after reading the label from stdin: %v", err)
	}
}

// TestAdd_EmptyLabelPromptErrors: an empty label from the prompt is rejected.
func TestAdd_EmptyLabelPromptErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "\n", "add", "--adopt-current")
	if err == nil || !strings.Contains(err.Error(), "label is required") {
		t.Fatalf("expected 'label is required', got %v", err)
	}
}

// ---- remove confirm-prompt branches -----------------------------------------

// TestRemove_ConfirmYesDeletes: without --yes, a "y" on stdin confirms removal.
func TestRemove_ConfirmYesDeletes(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	acct := seedAccount(t, dbPath, store.Account{Label: "byebye"}, validBlob("sk-bye"))
	// A different account is active so "byebye" is inactive.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-other")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "y\n", "remove", "byebye")
	if err != nil {
		t.Fatalf("remove with confirm: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("output = %q, want 'Removed'", out)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "byebye"); err == nil {
		t.Errorf("account should be gone after a confirmed remove")
	}
	if _, err := claude.RetrieveStash(ctx, acct.KeyringRef); err == nil {
		t.Errorf("stash should be gone after a confirmed remove")
	}
}

// TestRemove_ConfirmNoAborts: a "n" (or anything but y/yes) aborts the removal.
func TestRemove_ConfirmNoAborts(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	seedAccount(t, dbPath, store.Account{Label: "keepme"}, validBlob("sk-keep"))
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-other")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	_, err := runCLI(t, "n\n", "remove", "keepme")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected an 'aborted' error on a 'n' answer, got %v", err)
	}
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "keepme"); err != nil {
		t.Errorf("account should survive an aborted remove: %v", err)
	}
}

// ---- setConfigValue store-key path (covers the Set confirmation line) -------

// TestSetConfigValue_StoreKey drives setConfigValue through the store-backed
// branch (autostart's YAML branch hits launchctl, so it is skipped).
func TestSetConfigValue_StoreKey(t *testing.T) {
	_, dbPath := e2eEnv(t)
	out, err := runCLI(t, "", "config", "set", daemon.SettingsKeyAutoSwapGrace, "30")
	if err != nil {
		t.Fatalf("config set store key: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Set "+daemon.SettingsKeyAutoSwapGrace+" = 30") {
		t.Errorf("output = %q, want the set confirmation", out)
	}
	s := openStoreAt(t, dbPath)
	if v, _ := s.GetSetting(context.Background(), daemon.SettingsKeyAutoSwapGrace); v != "30" {
		t.Errorf("persisted grace = %q, want 30", v)
	}
}

// ---- decryptTokens error branches (unit, hermetic) --------------------------

func TestDecryptTokens_ErrorBranches(t *testing.T) {
	// Unsupported KDF.
	if _, err := decryptTokens(&CryptoEnvelope{KDF: "scrypt"}, "pw"); err == nil ||
		!strings.Contains(err.Error(), "unsupported kdf") {
		t.Errorf("unsupported kdf should error, got %v", err)
	}
	// Bad base64 salt.
	if _, err := decryptTokens(&CryptoEnvelope{KDF: kdfArgon2id, Salt: "!!!"}, "pw"); err == nil ||
		!strings.Contains(err.Error(), "decode salt") {
		t.Errorf("bad salt should error, got %v", err)
	}
	// Valid salt, bad base64 nonce.
	if _, err := decryptTokens(&CryptoEnvelope{KDF: kdfArgon2id, Salt: "AAAA", Nonce: "!!!"}, "pw"); err == nil ||
		!strings.Contains(err.Error(), "decode nonce") {
		t.Errorf("bad nonce should error, got %v", err)
	}
	// Valid salt+nonce, bad base64 data.
	if _, err := decryptTokens(&CryptoEnvelope{KDF: kdfArgon2id, Salt: "AAAA", Nonce: "AAAA", Data: "!!!"}, "pw"); err == nil ||
		!strings.Contains(err.Error(), "decode data") {
		t.Errorf("bad data should error, got %v", err)
	}
}

// TestEncryptDecryptTokens_RoundTripAndWrongPassphrase: a correct round-trip,
// then a wrong passphrase fails the GCM open.
func TestEncryptDecryptTokens_RoundTripAndWrongPassphrase(t *testing.T) {
	plain := []byte(`[{"label":"x"}]`)
	env, err := encryptTokens(plain, "right")
	if err != nil {
		t.Fatalf("encryptTokens: %v", err)
	}
	got, err := decryptTokens(env, "right")
	if err != nil {
		t.Fatalf("decryptTokens (right pass): %v", err)
	}
	if string(got) != string(plain) {
		t.Errorf("round-trip mismatch: %q != %q", got, plain)
	}
	if _, err := decryptTokens(env, "wrong"); err == nil ||
		!strings.Contains(err.Error(), "decrypt failed") {
		t.Errorf("wrong passphrase should fail GCM open, got %v", err)
	}
}

// ---- restoreAccount lookup-error branch -------------------------------------

// TestConfigImport_DecryptedBadCredentialSkipped exercises runConfigImport's
// per-record skip when a decrypted credential has invalid base64. We build a
// real encrypted bundle whose inner record carries a bad token, then import it.
func TestConfigImport_DecryptedBadCredentialSkipped(t *testing.T) {
	_, _ = e2eEnv(t)
	// Inner records JSON with one bad-base64 token.
	inner := `[{"label":"bad","email":"b@example.com","organization_uuid":"o","token":"!!notb64!!"}]`
	env, err := encryptTokens([]byte(inner), "pw")
	if err != nil {
		t.Fatalf("encrypt inner: %v", err)
	}
	bundle := map[string]any{
		"version":   bundleVersion,
		"encrypted": true,
		"cipher":    cipherAES256GCM,
		"kdf":       env.KDF,
		"salt":      env.Salt,
		"nonce":     env.Nonce,
		"data":      env.Data,
		"accounts":  []map[string]string{{"label": "bad", "email": "b@example.com"}},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(bundlePath, raw, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")

	out, err := runCLI(t, "", "config", "import", bundlePath)
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "0 added, 0 refreshed, 1 failed") {
		t.Errorf("a bad-base64 credential should be skipped as failed\n%s", out)
	}
}
