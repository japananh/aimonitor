package cli

import (
	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Print the recent switch audit log",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("log")
		},
	}
	cmd.Flags().IntVarP(&n, "limit", "n", 20, "number of recent entries to show")
	return cmd
}
