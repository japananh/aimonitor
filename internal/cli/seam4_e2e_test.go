package cli

import (
	"strings"
	"testing"
)

// Cheap hermetic coverage for the per-command top-level error branches reached
// when the store can't be opened (AIMONITOR_STORE_PATH points at a directory).
// These exercise each command's withRuntime/openStore error return. No
// t.Parallel.

func runWithBadStore(t *testing.T, args ...string) error {
	t.Helper()
	_, _ = e2eEnv(t)
	t.Setenv("AIMONITOR_STORE_PATH", t.TempDir()) // a dir → store.Open fails
	_, err := runCLI(t, "", args...)
	return err
}

func TestStatus_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "status"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from status, got %v", err)
	}
}

func TestSwitch_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "switch", "x"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from switch, got %v", err)
	}
}

func TestRename_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "rename", "a", "b"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from rename, got %v", err)
	}
}

func TestTokens_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "tokens"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from tokens, got %v", err)
	}
}

func TestLog_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "log"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from log, got %v", err)
	}
}

func TestProbe_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "probe", "--all"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from probe, got %v", err)
	}
}

func TestAdd_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "add", "--adopt-current", "--label", "x"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from add, got %v", err)
	}
}

func TestRemove_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "remove", "x", "--yes"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from remove, got %v", err)
	}
}

func TestConfigAudit_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "config", "audit"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from config audit, got %v", err)
	}
}

func TestUsageRefresh_StoreOpenFails(t *testing.T) {
	if err := runWithBadStore(t, "usage", "refresh"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from usage refresh, got %v", err)
	}
}

func TestMCPStatus_StoreOpenFails(t *testing.T) {
	// mcp status opens the config store first.
	if err := runWithBadStore(t, "mcp", "status"); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("expected open-store error from mcp status, got %v", err)
	}
}
