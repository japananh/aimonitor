package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Hermetic e2e coverage for `aimonitor config` (get/set/list/audit) and
// `config export`/`config import`. STORE-backed keys only — never `config set
// autostart`, which triggers install.EnableAutostart (real launchctl). All on
// the account_e2e harness (e2eEnv points AIMONITOR_STORE_PATH at a temp DB).
// No t.Parallel.

// ---- config get/set/list/audit ----------------------------------------------

func TestConfig_SetGetRoundTrip_StoreKey(t *testing.T) {
	_, dbPath := e2eEnv(t)

	out, err := runCLI(t, "", "config", "set", daemon.SettingsKeyAutoSwapThreshold, "62.5")
	if err != nil {
		t.Fatalf("config set: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Set "+daemon.SettingsKeyAutoSwapThreshold+" = 62.5") {
		t.Errorf("set output = %q", out)
	}

	out, err = runCLI(t, "", "config", "get", daemon.SettingsKeyAutoSwapThreshold)
	if err != nil {
		t.Fatalf("config get: %v (output: %q)", err, out)
	}
	if strings.TrimSpace(out) != "62.5" {
		t.Errorf("get = %q, want 62.5", out)
	}

	// The value must be persisted in the temp store.
	s := openStoreAt(t, dbPath)
	v, err := s.GetSetting(context.Background(), daemon.SettingsKeyAutoSwapThreshold)
	if err != nil || v != "62.5" {
		t.Errorf("persisted setting = %q (err %v), want 62.5", v, err)
	}
}

func TestConfig_SetNormalizesBool(t *testing.T) {
	_, _ = e2eEnv(t)
	if _, err := runCLI(t, "", "config", "set", daemon.SettingsKeyAutoSwapEnabled, "yes"); err != nil {
		t.Fatalf("config set: %v", err)
	}
	out, err := runCLI(t, "", "config", "get", daemon.SettingsKeyAutoSwapEnabled)
	if err != nil {
		t.Fatalf("config get: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		t.Errorf("'yes' should normalize to true, got %q", out)
	}
}

func TestConfig_GetUnsetReturnsDefault(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "config", "get", daemon.SettingsKeyAutoSwapGrace)
	if err != nil {
		t.Fatalf("config get default: %v", err)
	}
	if strings.TrimSpace(out) != "60" {
		t.Errorf("unset grace = %q, want the daemon default 60", out)
	}
}

func TestConfig_SetInvalidValueErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "set", daemon.SettingsKeyAutoSwapThreshold, "150")
	if err == nil || !strings.Contains(err.Error(), "(0, 100]") {
		t.Fatalf("expected range error, got %v", err)
	}
}

func TestConfig_SetUnknownKeyErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "set", "nonsense.key", "x")
	if err == nil || !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestConfig_SetDeprecatedKeyErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "set", "autoswitch", "true")
	if err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("expected deprecation error, got %v", err)
	}
}

func TestConfig_List(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "config", "list")
	if err != nil {
		t.Fatalf("config list: %v (output: %q)", err, out)
	}
	// Every canonical key should appear, including the YAML-backed autostart
	// (read path only) and a store-backed key.
	for _, want := range []string{"autostart =", daemon.SettingsKeyAutoSwapEnabled + " =", daemon.SettingsKeyAutoSwapThreshold + " ="} {
		if !strings.Contains(out, want) {
			t.Errorf("config list missing %q\n%s", want, out)
		}
	}
}

func TestConfig_AuditEmpty(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "config", "audit")
	if err != nil {
		t.Fatalf("config audit: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No configuration changes recorded yet") {
		t.Errorf("output = %q, want empty-audit message", out)
	}
}

func TestConfig_AuditAfterSet(t *testing.T) {
	_, _ = e2eEnv(t)
	if _, err := runCLI(t, "", "config", "set", daemon.SettingsKeyNotifyWarnPct, "55"); err != nil {
		t.Fatalf("config set: %v", err)
	}
	out, err := runCLI(t, "", "config", "audit")
	if err != nil {
		t.Fatalf("config audit: %v (output: %q)", err, out)
	}
	for _, want := range []string{"WHEN", "KEY", "SOURCE", daemon.SettingsKeyNotifyWarnPct, "55", "cli"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit output missing %q\n%s", want, out)
		}
	}
}

func TestConfig_GetUnknownKeyErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "get", "bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

// isStoreKey / unknownConfigKey unit coverage (cheap, deterministic).
func TestIsStoreKey(t *testing.T) {
	if !isStoreKey(daemon.SettingsKeyAutoSwapEnabled) {
		t.Error("auto_swap.enabled should be a store key")
	}
	if isStoreKey("autostart") {
		t.Error("autostart is YAML-backed, not a store key")
	}
	if err := unknownConfigKey("zzz"); !strings.Contains(err.Error(), "zzz") {
		t.Errorf("unknownConfigKey should name the key, got %v", err)
	}
}

// ---- config export / import (round-trip, hermetic via the claude seam) ------

func TestConfigExportImport_SettingsOnly(t *testing.T) {
	_, dbPath := e2eEnv(t)
	// Seed a non-default setting to verify it travels in the bundle.
	if _, err := runCLI(t, "", "config", "set", daemon.SettingsKeyAutoSwapThreshold, "73"); err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	// An account with an identity but no token → listed for re-add on import.
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com", OrganizationUUID: "org-1"}, validBlob("sk"))

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	out, err := runCLI(t, "", "config", "export", "--out", bundlePath)
	if err != nil {
		t.Fatalf("config export: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Exported") {
		t.Errorf("export output = %q", out)
	}
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !strings.Contains(string(raw), `"73"`) {
		t.Errorf("bundle should carry the 73 threshold:\n%s", raw)
	}
	// Plaintext bundle: no encrypted payload.
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("bundle not valid JSON: %v", err)
	}
	if enc, _ := b["encrypted"].(bool); enc {
		t.Errorf("settings-only bundle must not be encrypted")
	}

	// Import into a fresh store: settings restored, account listed for re-add.
	_, dbPath2 := e2eEnv(t)
	_ = dbPath2
	out, err = runCLI(t, "", "config", "import", bundlePath)
	if err != nil {
		t.Fatalf("config import: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "Restored") {
		t.Errorf("import output = %q, want 'Restored …'", out)
	}
	if !strings.Contains(out, "no credentials") || !strings.Contains(out, "work") {
		t.Errorf("token-less import should list 'work' for re-add\n%s", out)
	}
	// The threshold should now be present in the new store.
	got, err := runCLI(t, "", "config", "get", daemon.SettingsKeyAutoSwapThreshold)
	if err != nil || strings.TrimSpace(got) != "73" {
		t.Errorf("restored threshold = %q (err %v), want 73", got, err)
	}
}

// TestConfigExport_WithTokens_EncryptsAndHidesIdentity covers the encrypt
// branch (resolvePassphrase + encryptTokens) on CORRECT behavior: the file is
// marked encrypted, carries cipher/data, and leaks no plaintext identity.
func TestConfigExport_WithTokens_EncryptsAndHidesIdentity(t *testing.T) {
	_, dbPath := e2eEnv(t)
	blob := validBlob("sk-secret")
	seedAccount(t, dbPath, store.Account{
		Label: "work", Email: "w@example.com", OrganizationUUID: "org-1", OrganizationName: "Org One",
	}, blob)

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "correct horse battery staple")

	out, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath)
	if err != nil {
		t.Fatalf("export --include-tokens: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "encrypted") {
		t.Errorf("export output should note it is encrypted: %q", out)
	}
	raw, _ := os.ReadFile(bundlePath)
	// Encrypted bundle must NOT contain the plaintext email/org/label.
	if strings.Contains(string(raw), "w@example.com") || strings.Contains(string(raw), "Org One") {
		t.Errorf("encrypted bundle leaked plaintext identity:\n%s", raw)
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("bundle not valid JSON: %v", err)
	}
	if enc, _ := b["encrypted"].(bool); !enc {
		t.Errorf("--include-tokens bundle must set encrypted=true: %v", b)
	}
	if b["cipher"] != cipherAES256GCM || b["data"] == nil || b["data"] == "" {
		t.Errorf("encrypted bundle missing cipher/data: %v", b)
	}
}

// TestConfigImport_EncryptedBundle_KnownParseBug is a CHARACTERIZATION test for
// a genuine production bug: exportBundle embeds *cryptoEnvelope, and
// cryptoEnvelope is unexported. encoding/json can Marshal through an embedded
// unexported-type pointer (export works) but CANNOT Unmarshal into one — so
// `config import` of any --include-tokens bundle fails at json.Unmarshal,
// before the version check / decrypt / restore. Encrypted credential backups
// are therefore unrecoverable.
//
// This test locks in the CURRENT (buggy) behavior. When the prod bug is fixed
// (export the type, or add a named field + custom (Un)marshalJSON), flip this
// to assert a successful round-trip restore.
func TestConfigImport_EncryptedBundle_KnownParseBug(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com", OrganizationUUID: "org-1"}, validBlob("sk-secret"))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	if _, err := runCLI(t, "", "config", "export", "--include-tokens", "--out", bundlePath); err != nil {
		t.Fatalf("export: %v", err)
	}

	_, _ = e2eEnv(t)
	t.Setenv("AIMONITOR_PASSPHRASE", "pw")
	_, err := runCLI(t, "", "config", "import", bundlePath)
	if err == nil || !strings.Contains(err.Error(), "cannot set embedded pointer") {
		t.Fatalf("KNOWN BUG regressed: expected the embedded-pointer parse error, got %v", err)
	}
}

// TestExportBundle_EncryptedUnmarshalFails pins the root cause directly: the
// json decoder cannot populate the embedded *cryptoEnvelope. Companion to the
// command-level characterization test above.
func TestExportBundle_EncryptedUnmarshalFails(t *testing.T) {
	err := json.Unmarshal([]byte(`{"version":1,"encrypted":true,"cipher":"aes-256-gcm"}`), &exportBundle{})
	if err == nil || !strings.Contains(err.Error(), "cannot set embedded pointer") {
		t.Fatalf("expected embedded-pointer unmarshal error, got %v", err)
	}
}

func TestConfigExport_IncludeTokensMissingPassphrase(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	_ = os.Unsetenv("AIMONITOR_PASSPHRASE")
	_, err := runCLI(t, "", "config", "export", "--include-tokens")
	if err == nil || !strings.Contains(err.Error(), "passphrase is required") {
		t.Fatalf("expected passphrase-required error, got %v", err)
	}
}

func TestExportedSettingKeys(t *testing.T) {
	keys := exportedSettingKeys()
	if len(keys) == 0 {
		t.Fatal("exportedSettingKeys returned nothing")
	}
	// It must carry behavioral prefs but NOT machine-local update.skipped_version.
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, daemon.SettingsKeyAutoSwapEnabled) {
		t.Errorf("exported keys should include auto_swap.enabled: %v", keys)
	}
	if strings.Contains(joined, "update.skipped_version") {
		t.Errorf("exported keys must exclude update.skipped_version: %v", keys)
	}
}

func TestConfigImport_MissingFileErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "import", filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("expected read error for missing bundle, got %v", err)
	}
}

// TestConfigImport_SkipsInvalidSetting drives runConfigImport's per-setting
// validation skip: a hand-crafted plaintext bundle with one good + one bad
// setting restores the good one and reports the bad one skipped.
func TestConfigImport_SkipsInvalidSetting(t *testing.T) {
	_, _ = e2eEnv(t)
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	body := `{
  "version": 1,
  "encrypted": false,
  "settings": {
    "` + daemon.SettingsKeyAutoSwapThreshold + `": "44",
    "` + daemon.SettingsKeyAutoSwapThreshold7d + `": "999"
  }
}`
	if err := os.WriteFile(bundlePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	out, err := runCLI(t, "", "config", "import", bundlePath)
	if err != nil {
		t.Fatalf("import: %v (output: %q)", err, out)
	}
	// One restored, one skipped (999 is out of the (0,100] range).
	if !strings.Contains(out, "Restored 1 settings (1 skipped)") {
		t.Errorf("import summary = %q, want '1 restored, 1 skipped'", out)
	}
	got, _ := runCLI(t, "", "config", "get", daemon.SettingsKeyAutoSwapThreshold)
	if strings.TrimSpace(got) != "44" {
		t.Errorf("the valid setting should be restored, got %q", got)
	}
}

// TestRestoreAccount_CreateAndRefresh exercises restoreAccount directly (both
// branches). The CLI import path can't reach it — encrypted bundles fail to
// parse first (see TestConfigImport_EncryptedBundle_KnownParseBug) — but the
// function itself is fully hermetic: temp store + the claude keyring seam.
func TestRestoreAccount_CreateAndRefresh(t *testing.T) {
	_, dbPath := e2eEnv(t)
	ctx := context.Background()
	s := openStoreAt(t, dbPath)

	id := exportAccount{Label: "work", Email: "w@example.com", OrganizationUUID: "org-1", OrganizationName: "Org One"}

	// validBlob embeds time.Now() (so re-calling it yields different bytes), and
	// restoreAccount zeroes the credential it is handed (defer cred.Zero(), which
	// blanks the passed slice's backing array). So build each blob once and keep
	// an independent copy of the expected bytes for the post-call comparison.
	blobV1 := validBlob("sk-v1")
	wantV1 := append([]byte(nil), blobV1...)
	blobV2 := validBlob("sk-v2")
	wantV2 := append([]byte(nil), blobV2...)

	// First call: new identity → creates the account, returns added=true.
	added, err := restoreAccount(ctx, s, id, blobV1)
	if err != nil {
		t.Fatalf("restoreAccount create: %v", err)
	}
	if !added {
		t.Errorf("first restore of a new identity should report added=true")
	}
	acct, err := s.GetAccountByLabel(ctx, "work")
	if err != nil {
		t.Fatalf("account should exist after restore: %v", err)
	}
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("stash should exist after restore: %v", err)
	}
	if string(stash.Bytes) != string(wantV1) {
		t.Errorf("stash should hold the restored blob")
	}

	// Second call: SAME identity, NEW blob → refreshes, returns added=false.
	added, err = restoreAccount(ctx, s, id, blobV2)
	if err != nil {
		t.Fatalf("restoreAccount refresh: %v", err)
	}
	if added {
		t.Errorf("re-restoring the same identity should report added=false (refresh)")
	}
	stash, err = claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("stash should still exist after refresh: %v", err)
	}
	if string(stash.Bytes) != string(wantV2) {
		t.Errorf("stash should have been refreshed to the new blob")
	}
}

// TestConfigImport_UnsupportedVersion errors on a future bundle version.
func TestConfigImport_UnsupportedVersion(t *testing.T) {
	_, _ = e2eEnv(t)
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(bundlePath, []byte(`{"version":99,"settings":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := runCLI(t, "", "config", "import", bundlePath)
	if err == nil || !strings.Contains(err.Error(), "unsupported bundle version") {
		t.Fatalf("expected unsupported-version error, got %v", err)
	}
}

// TestValidateStoreValue_MoreBranches covers the validation branches not hit by
// config_test.go: excluded-ids dedupe/sort, disabled_tools normalization,
// skipped_version trim, and the notify-pct range.
func TestValidateStoreValue_MoreBranches(t *testing.T) {
	cases := []struct{ key, in, want string }{
		{daemon.SettingsKeyNotifyWarnPct, "55", "55"},
		{daemon.SettingsKeyNotifyCritPct, "90", "90"},
		{daemon.SettingsKeyNotifyEnabled, "off", "false"},
		{daemon.SettingsKeyDailySummaryEnabled, "yes", "true"},
		{SettingsKeyAutoUpdateEnabled, "no", "false"},
		{SettingsKeyUpdateSkippedVersion, "  v1.2.3  ", "v1.2.3"},
		{"mcp.disabled_tools", " slack_post , clickup_create ,", "slack_post,clickup_create"},
		{"mcp.slack.enabled", "1", "true"},
		{"mcp.clickup.read_only", "0", "false"},
	}
	for _, c := range cases {
		got, err := validateStoreValue(c.key, c.in)
		if err != nil {
			t.Errorf("validateStoreValue(%s,%q) err: %v", c.key, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("validateStoreValue(%s,%q) = %q want %q", c.key, c.in, got, c.want)
		}
	}
	// notify pct out of range fails.
	if _, err := validateStoreValue(daemon.SettingsKeyNotifyWarnPct, "0"); err == nil {
		t.Error("notify pct 0 should fail the (0,100] check")
	}
}

// TestStoreKeyDefault_MoreKeys covers the notify / mcp / update defaults.
func TestStoreKeyDefault_MoreKeys(t *testing.T) {
	cases := map[string]string{
		"mcp.slack.enabled":      "true",
		"mcp.slack.read_only":    "false",
		"mcp.disabled_tools":     "",
		"update.skipped_version": "",
	}
	for k, want := range cases {
		if got := storeKeyDefault(k); got != want {
			t.Errorf("storeKeyDefault(%q) = %q want %q", k, got, want)
		}
	}
}

// TestResolvePassphrase_FromFile covers the --passphrase-file branch end-to-end
// by driving `config export --include-tokens --passphrase-file`.
func TestResolvePassphrase_FromFile(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work", Email: "w@example.com"}, validBlob("sk"))
	_ = os.Unsetenv("AIMONITOR_PASSPHRASE")

	passFile := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(passFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write passfile: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	out, err := runCLI(t, "", "config", "export", "--include-tokens", "--passphrase-file", passFile, "--out", bundlePath)
	if err != nil {
		t.Fatalf("export with passphrase-file: %v (output: %q)", err, out)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Errorf("bundle should have been written: %v", err)
	}
}

func TestResolvePassphrase_EmptyFileErrors(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "work"}, validBlob("sk"))
	_ = os.Unsetenv("AIMONITOR_PASSPHRASE")
	passFile := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(passFile, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := runCLI(t, "", "config", "export", "--include-tokens", "--passphrase-file", passFile)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-passphrase-file error, got %v", err)
	}
}
