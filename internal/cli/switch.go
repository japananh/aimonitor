package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <label>",
		Short: "Swap the active credential in the Claude Code-credentials slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				acct, err := s.GetAccountByLabel(ctx, label)
				if err != nil {
					if errors.Is(err, store.ErrAccountNotFound) {
						return fmt.Errorf("no account with label %q (run `aimonitor list`)", label)
					}
					return err
				}

				cred, err := claude.RetrieveStash(ctx, acct.KeyringRef)
				if err != nil {
					return fmt.Errorf("read stash for %q: %w", label, err)
				}
				defer cred.Zero()

				if err := p.SetActiveCredential(ctx, cred); err != nil {
					return fmt.Errorf("write active credential: %w", err)
				}
				if err := s.UpdateAccountLastUsed(ctx, acct.ID, time.Now()); err != nil {
					// Non-fatal — the switch already succeeded.
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: switch ok but UpdateLastUsed failed: %v\n", err)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "Switched to %q. The next `claude` launch will use it.\n", label)
				return nil
			})
		},
	}
}
