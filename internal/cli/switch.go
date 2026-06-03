package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

func newSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <label>",
		Short: "Swap the active Claude credential to <label> — silently, no /login needed",
		Long: `Swap the active Claude credential (Claude Code-credentials slot) to the
account labelled <label>. The OAuth access token is refreshed silently
via Anthropic's token endpoint before the swap, so the next 'claude'
invocation works immediately without needing 'claude /login'.

A file lock at ~/.aimonitor-lock serialises concurrent switches.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				// Resolve the outgoing account first (best-effort) so the
				// audit row can record where we switched FROM.
				fromLabel := ""
				cc, _ := claudeconfig.New()
				if outgoing, found, err := daemon.ResolveActiveAccount(ctx, s, p, cc); err == nil && found {
					fromLabel = outgoing.Label
				}

				sw := daemon.NewSwitcher(s, p)
				sw.Stderr = cmd.ErrOrStderr()
				if err := sw.Switch(ctx, label); err != nil {
					return err
				}

				// Audit the manual switch. Load-bearing beyond bookkeeping:
				// the daemon's external-switch watcher attributes observed
				// active-account changes by looking for a recent audit row —
				// without this, every Switch-button press would be flagged
				// as another app's doing. Best-effort: an audit failure must
				// not fail a switch that already happened.
				if err := s.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
					FromLabel: fromLabel,
					ToLabel:   label,
					Trigger:   store.TriggerManual,
					Reason:    "aimonitor switch (CLI/widget)",
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: record switch audit: %v\n", err)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "Switched to %q. No `/login` needed.\n", label)
				return nil
			})
		},
	}
}
