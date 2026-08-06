package claude

import (
	"errors"
	"fmt"
	"testing"
)

func TestRequiresRelogin(t *testing.T) {
	unusable := &CredentialUnusableError{Label: "BE 1", Reason: "no tokens"}

	// IsCredentialUnusable unwraps.
	if !IsCredentialUnusable(unusable) {
		t.Error("IsCredentialUnusable should match the bare error")
	}
	if !IsCredentialUnusable(fmt.Errorf("refresh: %w", unusable)) {
		t.Error("IsCredentialUnusable should unwrap a wrapped error")
	}
	if IsCredentialUnusable(errors.New("network boom")) {
		t.Error("IsCredentialUnusable must not match an unrelated error")
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unusable-credential", unusable, true},
		{"wrapped-unusable", fmt.Errorf("x: %w", unusable), true},
		{"refresh-expired", &TokenRefreshExpiredError{Status: 400}, true},
		{"usage-401", &UsageAuthError{Status: 401}, true},
		{"wrapped-usage-401", fmt.Errorf("fetch: %w", &UsageAuthError{Status: 401}), true},
		{"network", errors.New("dial tcp: timeout"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := RequiresRelogin(c.err); got != c.want {
			t.Errorf("RequiresRelogin(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
