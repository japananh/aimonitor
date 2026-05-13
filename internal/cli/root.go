package cli

import (
	"github.com/japananh/aimonitor/internal/version"
	"github.com/spf13/cobra"
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
		newListCmd(),
		newSwitchCmd(),
		newStatusCmd(),
		newConfigCmd(),
		newProbeCmd(),
		newLogCmd(),
		newDaemonCmd(),
		newDoctorCmd(),
		newUninstallCmd(),
	)

	return root
}
