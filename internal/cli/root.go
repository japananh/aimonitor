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
		newDoctorCmd(),
		newUpdateCmd(),
		newVersionCmd(),
		newUninstallCmd(),
	)

	return root
}
