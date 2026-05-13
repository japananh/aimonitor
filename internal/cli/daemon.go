package cli

import (
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control the aimonitor background daemon",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start the daemon",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon start") },
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the daemon",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon stop") },
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Restart the daemon",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon restart") },
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report the daemon's PID and uptime",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon status") },
		},
		&cobra.Command{
			Use:    "run",
			Short:  "Run the daemon in the foreground (used by launchd/systemd)",
			Hidden: true,
			RunE:   func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon run") },
		},
	)
	return cmd
}
