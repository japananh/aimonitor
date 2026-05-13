// Package cli implements the aimonitor CLI subcommands (add, list, switch,
// status, config, probe, log, daemon, doctor, uninstall). Subcommands are
// thin wrappers that talk to the aimonitor daemon over a Unix socket.
package cli

import (
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a Claude account (opens claude login, then stashes the resulting credential)",
		Long: `Onboarding flow:
  1. Stash the current Claude Code-credentials blob in memory.
  2. Invoke 'claude login' and wait for OAuth completion.
  3. Read the newly-written blob.
  4. Move it into an aimonitor-namespaced Keychain entry under <label>.
  5. Restore the original blob so the previously-active account stays active.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("add")
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "label to assign to the new account (prompted if omitted)")
	return cmd
}
