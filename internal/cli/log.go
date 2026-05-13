package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Print the recent switch audit log",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				rows, err := s.ListSwitchAudit(ctx, n)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No switch events yet.")
					return nil
				}
				out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(out, "WHEN\tFROM → TO\tTRIGGER\tFROM REM.\tTO REM.\tREASON")
				for _, r := range rows {
					from := r.FromLabel
					if from == "" {
						from = "-"
					}
					fromRem := "-"
					if r.FromProbedRemaining > 0 {
						fromRem = fmt.Sprintf("%d", r.FromProbedRemaining)
					}
					toRem := "-"
					if r.ToProbedRemaining > 0 {
						toRem = fmt.Sprintf("%d", r.ToProbedRemaining)
					}
					reason := r.Reason
					if reason == "" {
						reason = "-"
					}
					fmt.Fprintf(out, "%s\t%s → %s\t%s\t%s\t%s\t%s\n",
						r.Ts.Format("2006-01-02 15:04:05"),
						from, r.ToLabel, r.Trigger, fromRem, toRem, reason)
				}
				return out.Flush()
			})
		},
	}
	cmd.Flags().IntVarP(&n, "limit", "n", 20, "number of recent entries to show")
	return cmd
}
