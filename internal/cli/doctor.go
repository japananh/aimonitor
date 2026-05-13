package cli

import (
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run a battery of health checks (daemon, JSONL parser, keyring, DB, probes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("doctor")
		},
	}
}
