package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which account is active right now (by comparing the live keyring blob against known stashes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				active, err := p.ActiveCredential(ctx)
				if err != nil {
					return fmt.Errorf("read active credential: %w", err)
				}
				if len(active.Bytes) == 0 {
					return errors.New("no Claude Code credential is currently active (Claude Code-credentials slot is empty)")
				}
				defer active.Zero()

				match, err := findMatchingAccount(ctx, s, active.Bytes)
				if err != nil {
					return err
				}
				if match == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "Active credential is not one of aimonitor's stashed accounts.")
					fmt.Fprintln(cmd.OutOrStdout(), "Hint: run `aimonitor add` to onboard it.")
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Active account: %s", match.Label)
				if match.Email != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " (%s)", match.Email)
				}
				fmt.Fprintln(cmd.OutOrStdout())
				// Session-usage estimate plumbing lands in Phase 3 (the
				// daemon's per-account aggregator). For now, status just
				// identifies the active account.
				return nil
			})
		},
	}
}

// findMatchingAccount returns the SQLite Account whose keyring stash
// matches activeBytes, or (nil, nil) if no match.
func findMatchingAccount(ctx context.Context, s *store.Store, activeBytes []byte) (*store.Account, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		stash, err := claude.RetrieveStash(ctx, accounts[i].KeyringRef)
		if err != nil {
			continue
		}
		if bytes.Equal(stash.Bytes, activeBytes) {
			return &accounts[i], nil
		}
	}
	return nil, nil
}

// Compile-time signal that the provider import is used.
var _ provider.Provider