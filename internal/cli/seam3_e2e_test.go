package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Coverage for the stash-failure / store-open-failure / stale-probe error
// branches. No t.Parallel; hermetic via the keyring seams + a directory as the
// store path (which store.Open cannot use, so openStore errors).

// ---- import: stash write failures (erroring claude-seam keyring) ------------

// TestImport_NewAccountStashFails: the credential READ succeeds (healthy
// secret.Default ring) but the STASH write fails (erroring claude-seam ring),
// so the new account is reported failed. Covers runImport's stash-credential
// error branch.
func TestImport_NewAccountStashFails(t *testing.T) {
	ring, _ := e2eEnv(t)
	// Override ONLY the claude stash seam with a Set-failing ring; the read
	// path (secret.Default) keeps using the healthy ring.
	t.Cleanup(claude.SetKeyringForTest(&errKeyring{delegate: ring, failSet: true}))

	seedImportRegistryAndCreds(t, ring, []cbAccount{
		{Number: 1, Email: "new@example.com", OrganizationUUID: "org-n", Nickname: "new"},
	})

	out, err := runCLI(t, "", "import")
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "stash credential") {
		t.Errorf("a failed stash should be reported per-account\n%s", out)
	}
	if !strings.Contains(out, "0 added, 0 refreshed, 1 failed") {
		t.Errorf("summary should count the stash failure\n%s", out)
	}
}

// TestImport_RefreshStashFails: an EXISTING account whose stash refresh fails is
// reported failed. Covers runImport's refresh-stash error branch.
func TestImport_RefreshStashFails(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{
		Label: "dup", Email: "dup@example.com", OrganizationUUID: "org-d",
	}, validBlob("sk-old"))
	t.Cleanup(claude.SetKeyringForTest(&errKeyring{delegate: ring, failSet: true}))
	seedImportRegistryAndCreds(t, ring, []cbAccount{
		{Number: 7, Email: "dup@example.com", OrganizationUUID: "org-d", Nickname: "dup"},
	})

	out, err := runCLI(t, "", "import")
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "refresh stash") {
		t.Errorf("a failed refresh-stash should be reported\n%s", out)
	}
	if !strings.Contains(out, "0 added, 0 refreshed, 1 failed") {
		t.Errorf("summary should count the refresh failure\n%s", out)
	}
}

// ---- doctor: stale probe branch ---------------------------------------------

// TestDoctor_StaleProbe: an old probe row surfaces ErrProbeStale, exercising
// runDoctor's stale-probe per-account branch.
func TestDoctor_StaleProbe(t *testing.T) {
	_, dbPath := e2eEnv(t)
	ctx := context.Background()
	acct := seedAccount(t, dbPath, store.Account{Label: "staley"}, validBlob("sk"))
	s := openStoreAt(t, dbPath)
	// ProbedAt far in the past → GetProbeResult returns ErrProbeStale.
	if err := s.PutProbeResult(ctx, acct.ID, provider.RateLimit{
		ProbedAt:        time.Now().Add(-24 * time.Hour),
		TokensRemaining: 9,
		HTTPStatus:      200,
	}); err != nil {
		t.Fatalf("seed stale probe: %v", err)
	}

	out, err := runCLI(t, "", "doctor")
	if err != nil {
		t.Fatalf("doctor: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "probe staley") || !strings.Contains(out, "stale") {
		t.Errorf("doctor should report the stale probe\n%s", out)
	}
}

// ---- store-open failures (withRuntime / openStore / openConfigStore) --------

// badStorePath points AIMONITOR_STORE_PATH at a directory so store.Open fails.
func badStorePath(t *testing.T) {
	t.Helper()
	t.Setenv("AIMONITOR_STORE_PATH", t.TempDir())
}

// TestList_StoreOpenFails: `list` against an unopenable store path errors in
// withRuntime → openStore (covers runtime.go's open-store error branch).
func TestList_StoreOpenFails(t *testing.T) {
	_, _ = e2eEnv(t)
	badStorePath(t)
	_, err := runCLI(t, "", "list")
	if err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected an open-store error, got %v", err)
	}
}

// TestConfigGet_StoreOpenFails: `config get <store-key>` opens the config store
// (openConfigStore); a bad path errors there (getStoreSetting open branch).
func TestConfigGet_StoreOpenFails(t *testing.T) {
	_, _ = e2eEnv(t)
	badStorePath(t)
	_, err := runCLI(t, "", "config", "get", "auto_swap.enabled")
	if err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected an open-store error from config get, got %v", err)
	}
}

// TestConfigSet_StoreOpenFails: `config set <store-key>` opens the config store
// in setStoreSetting; a bad path errors (its open branch).
func TestConfigSet_StoreOpenFails(t *testing.T) {
	_, _ = e2eEnv(t)
	badStorePath(t)
	_, err := runCLI(t, "", "config", "set", "auto_swap.enabled", "true")
	if err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected an open-store error from config set, got %v", err)
	}
}

// TestConfigExport_StoreOpenFails: export goes through withRuntime → openStore.
func TestConfigExport_StoreOpenFails(t *testing.T) {
	_, _ = e2eEnv(t)
	badStorePath(t)
	_, err := runCLI(t, "", "config", "export")
	if err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected an open-store error from config export, got %v", err)
	}
}

// ---- runConfigImport: decrypted-records JSON parse failure ------------------

// TestConfigImport_DecryptedNotJSON: a bundle whose decrypted payload is valid
// to decrypt but isn't a JSON []encAccount fails parsing the decrypted records.
func TestConfigImport_DecryptedNotJSON(t *testing.T) {
	_, _ = e2eEnv(t)
	env, err := encryptTokens([]byte("not-a-json-array"), "pw")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	bundle := map[string]any{
		"version":   bundleVersion,
		"encrypted": true,
		"cipher":    cipherAES256GCM,
		"kdf":       env.KDF,
		"salt":      env.Salt,
		"nonce":     env.Nonce,
		"data":      env.Data,
	}
	raw, _ := json.Marshal(bundle)
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(bundlePath, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")

	_, err = runCLI(t, "", "config", "import", bundlePath)
	if err == nil || !strings.Contains(err.Error(), "parse decrypted records") {
		t.Fatalf("expected a decrypted-records parse error, got %v", err)
	}
}
