package cli

import (
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current account's session-window usage (local estimate)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("status")
		},
	}
}
