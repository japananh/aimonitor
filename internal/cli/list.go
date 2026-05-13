// Package cli implements the aimonitor CLI subcommands.
package cli

import (
	"bytes"
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accounts aimonitor knows about with their local-estimate usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				accounts, err := s.ListAccounts(ctx)
				if err != nil {
					return err
				}
				if len(accounts) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No accounts configured. Run `aimonitor add` to onboard one.")
					return nil
				}

				active, _ := p.ActiveCredential(ctx) // ignore: empty slot is fine
				out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(out, "LABEL\tEMAIL\tACTIVE\tLAST USED\tCREATED")
				for _, a := range accounts {
					isActive := ""
					if stashMatches(ctx, a.KeyringRef, active.Bytes) {
						isActive = "✓"
					}
					last := "-"
					if !a.LastUsedAt.IsZero() {
						last = a.LastUsedAt.Format("2006-01-02 15:04")
					}
					fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n",
						a.Label, dash(a.Email), isActive, last,
						a.CreatedAt.Format("2006-01-02"))
				}
				return out.Flush()
			})
		},
	}
}

// stashMatches returns true when the keyring entry under ref has the
// same bytes as active. Used to mark the row whose stash equals the
// currently-installed Claude Code-credentials blob.
func stashMatches(ctx context.Context, ref string, active []byte) bool {
	if len(active) == 0 {
		return false
	}
	stash, err := claude.RetrieveStash(ctx, ref)
	if err != nil {
		return false
	}
	return bytes.Equal(stash.Bytes, active)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
