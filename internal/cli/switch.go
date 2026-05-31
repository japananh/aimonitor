package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

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
				sw := daemon.NewSwitcher(s, p)
				sw.Stderr = cmd.ErrOrStderr()
				if err := sw.Switch(ctx, label); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Switched to %q. No `/login` needed.\n", label)
				return nil
			})
		},
	}
}
