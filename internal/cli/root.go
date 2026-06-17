package cli

import (
	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/version"
)

// NewRootCmd returns the top-level cobra command with every aimonitor
// subcommand attached. main() wires it up and calls Execute().
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "aimonitor",
		Short:   "Multi-account Claude Code session monitor and account switcher",
		Version: version.Version,
		Long: `aimonitor watches Claude Code's local JSONL transcripts to estimate
per-account session usage and lets you switch between multiple Claude
OAuth accounts stashed in your OS keyring.

It is local-first, has no telemetry, and never phones home.`,
		// On a runtime error, don't dump the command's usage/flags block, and
		// let main() print the error (it already does, to stderr + exit 1).
		// Without these, cobra prints "Error: …" + the full Usage/Flags help
		// AND main() prints the error again — that whole blob was getting
		// surfaced verbatim in the menu-bar popover on a failed refresh.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newAddCmd(),
		newImportCmd(),
		newRemoveCmd(),
		newRenameCmd(),
		newListCmd(),
		newSwitchCmd(),
		newStatusCmd(),
		newConfigCmd(),
		newProbeCmd(),
		newUsageCmd(),
		newLogCmd(),
		newDaemonCmd(),
		newMCPCmd(),
		newDoctorCmd(),
		newUpdateCmd(),
		newVersionCmd(),
		newUninstallCmd(),
	)

	return root
}
