package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <label>",
		Aliases: []string{"rm"},
		Short:   "Remove a single account (its stash + registry row)",
		Long: `Remove one account: delete its aimonitor-namespaced keyring stash and
its registry row. The shared Claude Code-credentials slot is never
touched, so your active Claude Code login keeps working.

Refuses to remove the account that is currently active — switch to a
different account first (the active account's tokens live in the shared
slot, so removing its stash would strand you with no backup to switch
back to).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runRemove(ctx, cmd, s, p, label, yes)
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func runRemove(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, label string, yes bool) error {
	acct, err := s.GetAccountByLabel(ctx, label)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			return fmt.Errorf("no account with label %q (run `aimonitor list`)", label)
		}
		return err
	}

	// Refuse to remove the active account. Active = the account whose
	// stash byte-matches the live Claude Code-credentials slot. We err on
	// the side of refusing: if the live read fails we can't prove it's
	// inactive, so we ask the user to switch away first.
	active, err := isActiveAccount(ctx, p, acct)
	if err != nil {
		return fmt.Errorf("determine active account: %w", err)
	}
	if active {
		return fmt.Errorf("%q is the active account — `aimonitor switch <other>` first, then remove it", label)
	}

	if !yes {
		ans, err := promptLine(cmd, fmt.Sprintf("Remove account %q and its stored credential? [y/N]: ", label))
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("aborted")
		}
	}

	// Delete the stash first: if it fails we keep the registry row so the
	// account is still listed and retryable. A deleted row with a leaked
	// stash would be the worse, invisible outcome.
	if err := claude.DeleteStash(ctx, acct.KeyringRef); err != nil {
		return fmt.Errorf("delete keyring stash for %q: %w", label, err)
	}
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		return fmt.Errorf("delete account row for %q: %w", label, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Removed %q.\n", label)
	return nil
}

// isActiveAccount reports whether acct's stash matches the live
// Claude Code-credentials blob. An empty live slot means no account is
// active, so acct is not active.
func isActiveAccount(ctx context.Context, p provider.Provider, acct store.Account) (bool, error) {
	live, err := p.ActiveCredential(ctx)
	if err != nil {
		return false, err
	}
	defer live.Zero()
	if len(live.Bytes) == 0 {
		return false, nil
	}
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		// No stash (or unreadable) → can't match → treat as not active.
		return false, nil
	}
	defer stash.Zero()
	return bytes.Equal(stash.Bytes, live.Bytes), nil
}
