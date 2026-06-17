package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestRootCmd_SilencesUsageAndErrorsOnFailure guards the popover fix: a
// failing command must NOT dump cobra's usage/flags help (it was being
// surfaced verbatim in the menu-bar popover on a failed `usage refresh`).
func TestRootCmd_SilencesUsageAndErrorsOnFailure(t *testing.T) {
	root := NewRootCmd()
	if !root.SilenceUsage || !root.SilenceErrors {
		t.Fatalf("root must silence usage+errors: usage=%v errors=%v", root.SilenceUsage, root.SilenceErrors)
	}

	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"nope-not-a-real-command"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	combined := out.String() + errBuf.String()
	if strings.Contains(combined, "Usage:") || strings.Contains(combined, "Flags:") {
		t.Errorf("usage/flags help leaked into command output:\n%s", combined)
	}
}
