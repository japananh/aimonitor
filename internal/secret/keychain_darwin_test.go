//go:build darwin

package secret

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func exitErr(t *testing.T, script string) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", script).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("script %q: want ExitError, got %v", script, err)
	}
	return err
}

func TestClassifyFindError(t *testing.T) {
	tests := []struct {
		name         string
		runErr       error
		ctxErr       error
		stderr       string
		wantNotFound bool
		wantContains []string
	}{
		{name: "exit 44 is item-not-found", runErr: exitErr(t, "exit 44"), wantNotFound: true},
		{
			name:         "non-44 exit with empty stderr is a read failure, not missing",
			runErr:       exitErr(t, "exit 36"),
			wantContains: []string{"exit status 36", "svc/acct"},
		},
		{
			name:         "timeout kill is reported as a timeout",
			runErr:       exitErr(t, "kill -9 $$"),
			ctxErr:       context.DeadlineExceeded,
			wantContains: []string{"timed out after"},
		},
		{
			name:         "stderr is kept when present",
			runErr:       exitErr(t, "exit 1"),
			stderr:       "security: SecKeychainSearchCopyNext: User interaction is not allowed.",
			wantContains: []string{"exit status 1", "User interaction is not allowed"},
		},
		{
			name:         "killed without a deadline still names the signal",
			runErr:       exitErr(t, "kill -9 $$"),
			wantContains: []string{"signal: killed"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyFindError("svc", "acct", tc.runErr, tc.ctxErr, bytes.NewBufferString(tc.stderr))
			if tc.wantNotFound {
				if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) {
					t.Fatalf("err = %v, want ErrNotFound only", err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrUnavailable only", err)
			}
			for _, frag := range tc.wantContains {
				if !strings.Contains(err.Error(), frag) {
					t.Errorf("err %q missing %q", err, frag)
				}
			}
			if strings.Contains(err.Error(), "(no stderr)") {
				t.Errorf("err %q must not hide the cause behind \"(no stderr)\"", err)
			}
		})
	}
}
