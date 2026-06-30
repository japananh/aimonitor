package cli

import (
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/daemon"
)

// Margin coverage: validateStoreValue's parse-error branches for the notify /
// daily-summary / auto-update keys, driven directly (cheap, deterministic).
// No t.Parallel.

func TestValidateStoreValue_ErrorBranches(t *testing.T) {
	cases := []struct{ key, in, wantErr string }{
		{daemon.SettingsKeyNotifyEnabled, "maybe", ""},           // bad bool
		{daemon.SettingsKeyNotifyWarnPct, "abc", "not a number"}, // bad float
		{daemon.SettingsKeyNotifyCritPct, "200", "(0, 100]"},     // out of range
		{daemon.SettingsKeyDailySummaryEnabled, "perhaps", ""},   // bad bool
		{SettingsKeyAutoUpdateEnabled, "kinda", ""},              // bad bool
	}
	for _, c := range cases {
		_, err := validateStoreValue(c.key, c.in)
		if err == nil {
			t.Errorf("validateStoreValue(%s, %q) should error", c.key, c.in)
			continue
		}
		if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("validateStoreValue(%s, %q) err = %q, want it to contain %q", c.key, c.in, err.Error(), c.wantErr)
		}
	}
}

// TestConfigSet_NotifyBadBool drives the same error through the CLI so the
// setConfigValue → setStoreSetting → validateStoreValue error return is
// exercised end-to-end.
func TestConfigSet_NotifyBadBool(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "config", "set", daemon.SettingsKeyNotifyEnabled, "maybe")
	if err == nil {
		t.Fatalf("config set notify.enabled=maybe should error")
	}
}
