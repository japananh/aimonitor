package cli

import (
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Get or set aimonitor configuration values",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "get <key>",
			Short: "Print a configuration value",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("config get") },
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Update a configuration value",
			Args:  cobra.ExactArgs(2),
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("config set") },
		},
	)
	return cmd
}
