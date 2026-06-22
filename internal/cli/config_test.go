package cli

import (
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/daemon"
)

func TestValidateStoreValue(t *testing.T) {
	ok := []struct{ key, in, want string }{
		{daemon.SettingsKeyAutoSwapEnabled, "yes", "true"},
		{daemon.SettingsKeyAutoSwapEnabled, "off", "false"},
		{daemon.SettingsKeyAutoSwapThreshold, "80", "80"},
		{daemon.SettingsKeyAutoSwapThreshold, "62.5", "62.5"},
		{daemon.SettingsKeyAutoSwapGrace, "0", "0"},
		{daemon.SettingsKeyAutoSwapGrace, "120", "120"},
		{daemon.SettingsKeyAutoSwapExcluded, "3,1,1,2", "1,2,3"}, // dedupe + sort
		{daemon.SettingsKeyAutoSwapExcluded, " 2 , 5 ", "2,5"},   // trim whitespace
		{daemon.SettingsKeyAutoSwapExcluded, "", ""},             // empty = exclude nothing
	}
	for _, c := range ok {
		got, err := validateStoreValue(c.key, c.in)
		if err != nil {
			t.Errorf("validateStoreValue(%s,%q) unexpected err: %v", c.key, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("validateStoreValue(%s,%q) = %q want %q", c.key, c.in, got, c.want)
		}
	}

	bad := []struct{ key, in string }{
		{daemon.SettingsKeyAutoSwapEnabled, "maybe"},
		{daemon.SettingsKeyAutoSwapThreshold, "0"},   // must be > 0
		{daemon.SettingsKeyAutoSwapThreshold, "150"}, // must be <= 100
		{daemon.SettingsKeyAutoSwapThreshold, "abc"},
		{daemon.SettingsKeyAutoSwapGrace, "-5"}, // must be >= 0
		{daemon.SettingsKeyAutoSwapGrace, "1.5"},
		{daemon.SettingsKeyAutoSwapExcluded, "abc"},   // not an account id
		{daemon.SettingsKeyAutoSwapExcluded, "1,abc"}, // one bad entry fails the whole value
	}
	for _, c := range bad {
		if _, err := validateStoreValue(c.key, c.in); err == nil {
			t.Errorf("validateStoreValue(%s,%q) should have errored", c.key, c.in)
		}
	}
}

func TestStoreKeyDefaultMatchesDaemon(t *testing.T) {
	if got := storeKeyDefault(daemon.SettingsKeyAutoSwapEnabled); got != "true" {
		t.Errorf("enabled default = %q want true", got)
	}
	if got := storeKeyDefault(daemon.SettingsKeyAutoSwapThreshold); got != "80" {
		t.Errorf("threshold default = %q want 80", got)
	}
	if got := storeKeyDefault(daemon.SettingsKeyAutoSwapGrace); got != "60" {
		t.Errorf("grace default = %q want 60", got)
	}
}

func TestDeprecatedKeysRejected(t *testing.T) {
	for _, k := range []string{"autoswitch", "thresholds", "autoswitch_cooldown_seconds"} {
		if _, dep := deprecatedKeys[k]; !dep {
			t.Errorf("%q should be in deprecatedKeys", k)
		}
		// get should surface the deprecation, not a value.
		if _, err := getConfigValue(t.Context(), k); err == nil || !strings.Contains(err.Error(), "deprecated") {
			t.Errorf("getConfigValue(%q) should return a deprecation error, got %v", k, err)
		}
	}
}
