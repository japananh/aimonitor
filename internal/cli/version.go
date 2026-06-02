package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/version"
)

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print aimonitor version, commit, and build date",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				out, err := json.Marshal(map[string]string{
					"version":   version.Version,
					"commit":    version.Commit,
					"buildDate": version.BuildDate,
				})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "aimonitor %s (commit %s, built %s)\n",
				version.Version, version.Commit, version.BuildDate)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print version info as JSON")
	return cmd
}
