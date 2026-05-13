package cli

import (
	"github.com/spf13/cobra"
)

func newUninstallCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall aimonitor (use --purge to also remove data + Keychain entries)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("uninstall")
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove SQLite DB and aimonitor Keychain entries")
	return cmd
}
