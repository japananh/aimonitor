package cli

import (
	"github.com/spf13/cobra"
)

func newProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "probe <label>",
		Short: "Issue a server-side rate-limit probe and report true remaining tokens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("probe")
		},
	}
}
