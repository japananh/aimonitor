package cli

import (
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accounts aimonitor knows about with their local-estimate usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("list")
		},
	}
}
