package cli

import (
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/secret"
)

// Last few hermetic mcp branches: disconnect's Delete error, and status's
// disabled / read-only flag suffixes. No t.Parallel.

// TestMCPDisconnect_DeleteError: an erroring keyring makes creds.Delete return a
// non-not-found error, covering disconnect's delete-error branch.
func TestMCPDisconnect_DeleteError(t *testing.T) {
	ring, _ := e2eEnv(t)
	if err := ring.Set(mcpService("slack"), osUser(t), []byte("xoxp-seed")); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	t.Cleanup(secret.SetDefaultForTest(&errKeyring{delegate: ring, failDelete: true}))

	_, err := runCLI(t, "", "mcp", "disconnect", "slack")
	if err == nil || !strings.Contains(err.Error(), "delete slack token") {
		t.Fatalf("expected a delete-token error, got %v", err)
	}
}

// TestMCPStatus_DisabledAndReadOnlyFlags: with slack disabled and clickup
// read-only, status's text path appends the [disabled] / [read-only] suffixes.
func TestMCPStatus_DisabledAndReadOnlyFlags(t *testing.T) {
	_, _ = e2eEnv(t)
	if _, err := runCLI(t, "", "config", "set", "mcp.slack.enabled", "false"); err != nil {
		t.Fatalf("disable slack: %v", err)
	}
	if _, err := runCLI(t, "", "config", "set", "mcp.clickup.read_only", "true"); err != nil {
		t.Fatalf("clickup read-only: %v", err)
	}

	out, err := runCLI(t, "", "mcp", "status")
	if err != nil {
		t.Fatalf("mcp status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("status should show the [disabled] flag for slack\n%s", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Errorf("status should show the [read-only] flag for clickup\n%s", out)
	}
}

// TestConfigList_StoreErrorPerKey: `config list` with an unopenable store makes
// getConfigValue error for the store-backed keys, so list prints "(error: …)"
// for them instead of failing the whole command (covers config list's per-key
// error branch).
func TestConfigList_StoreErrorPerKey(t *testing.T) {
	_, _ = e2eEnv(t)
	t.Setenv("AIMONITOR_STORE_PATH", t.TempDir()) // a dir → store.Open fails

	out, err := runCLI(t, "", "config", "list")
	if err != nil {
		t.Fatalf("config list should not fail wholesale on a per-key error: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "(error:") {
		t.Errorf("store-backed keys should render an inline error\n%s", out)
	}
}
