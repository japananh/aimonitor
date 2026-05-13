package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestStubsReturnNotImplemented is a coarse smoke test: every CLI
// subcommand that currently returns errNotImplemented must do so without
// panicking or exiting cleanly. Once a subcommand grows a real
// implementation, drop it from this table.
func TestStubsReturnNotImplemented(t *testing.T) {
	// add, list, switch, status are now wired (Phase 2). The remaining
	// subcommands below still return errNotImplemented — drop a row from
	// this list when its real implementation lands.
	cases := []struct {
		name string
		args []string
	}{
		{"log", []string{"log"}},
		{"daemon-start", []string{"daemon", "start"}},
		{"daemon-stop", []string{"daemon", "stop"}},
		{"daemon-status", []string{"daemon", "status"}},
		{"doctor", []string{"doctor"}},
		{"uninstall", []string{"uninstall"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCmd()
			cmd.SetArgs(tc.args)
			// Quiet cobra's own usage/error printing — the test only
			// cares about the returned error, not cobra's stderr noise.
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected error from %v, got nil (output: %q)", tc.args, out.String())
			}
			if !strings.Contains(err.Error(), "not implemented") {
				t.Fatalf("expected 'not implemented' in error for %v, got %q", tc.args, err.Error())
			}
		})
	}
}

// TestHelpAndVersion confirms two no-op flag paths work end-to-end. These
// shouldn't print anything to stderr because cobra's stable paths.
func TestHelpAndVersion(t *testing.T) {
	for _, flag := range []string{"--help", "--version"} {
		t.Run(flag, func(t *testing.T) {
			cmd := NewRootCmd()
			cmd.SetArgs([]string{flag})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%s: %v", flag, err)
			}
			if out.Len() == 0 {
				t.Fatalf("%s produced no output", flag)
			}
			// Reject nil/sentinel content; the exact phrasing belongs to cobra.
			if !strings.Contains(out.String(), "aimonitor") {
				t.Fatalf("%s output did not mention aimonitor: %q", flag, out.String())
			}
		})
	}
}

// Sanity check: errNotImplemented wraps to something user-distinguishable.
func TestErrNotImplemented(t *testing.T) {
	err := errNotImplemented("foo")
	if errors.Unwrap(err) != nil {
		t.Errorf("errNotImplemented should not wrap; got %v", errors.Unwrap(err))
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error should mention command name; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error should mention 'not implemented'; got %q", err.Error())
	}
}
