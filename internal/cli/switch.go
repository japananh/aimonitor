package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/japananh/aimonitor/internal/util/filelock"
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
				return runSwitch(ctx, cmd, s, p, label)
			})
		},
	}
}

// runSwitch performs the silent account swap:
//
//  1. Acquire ~/.aimonitor-lock so two concurrent `aimonitor switch`
//     invocations don't race on the live keychain slot.
//  2. Look up the target account.
//  3. Read the target's stashed credential blob.
//  4. If the stashed access token is near or past expiry, refresh it via
//     Anthropic's OAuth token endpoint. Persist the rotated tokens back
//     to the stash so future switches start from fresh state.
//  5. Write the (possibly-refreshed) blob to the live slot.
//  6. Update accounts.last_used_at and announce success.
//
// On any failure before step 5, the live slot is untouched — the user
// remains on whatever account they were on. Failures from step 5 onward
// are surfaced loudly so the user can recover manually.
func runSwitch(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, label string) error {
	lock, err := acquireSwitchLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	acct, err := s.GetAccountByLabel(ctx, label)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			return fmt.Errorf("no account with label %q (run `aimonitor list`)", label)
		}
		return err
	}

	stashed, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return fmt.Errorf("read stash for %q: %w", label, err)
	}
	defer stashed.Zero()

	live, err := ensureFreshTokens(ctx, cmd, acct, stashed)
	if err != nil {
		return err
	}
	// live may alias stashed (no refresh needed) or be a fresh blob from
	// ReplaceTokens. Either way, zero the bytes when we're done.
	defer live.Zero()

	if err := p.SetActiveCredential(ctx, live); err != nil {
		return fmt.Errorf("write active credential: %w", err)
	}
	if err := s.UpdateAccountLastUsed(ctx, acct.ID, time.Now()); err != nil {
		// Non-fatal — the switch already succeeded.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: switch ok but UpdateLastUsed failed: %v\n", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Switched to %q. No `/login` needed.\n", label)
	return nil
}

// ensureFreshTokens returns a credential blob whose access token is
// guaranteed to be valid for at least the refresh-buffer window. If
// the stashed blob is already fresh, it is returned unchanged. If
// stale, a token-refresh round-trip happens and the rotated tokens
// are persisted back to the stash before being returned.
//
// Persisting the rotated tokens BEFORE writing to the live slot is
// load-bearing: if SetActiveCredential later fails for some reason,
// the stash holds the most recent valid state and the next switch
// attempt won't need to refresh again.
func ensureFreshTokens(ctx context.Context, cmd *cobra.Command, acct store.Account, stashed provider.Credential) (provider.Credential, error) {
	tokens, err := claude.ParseCredential(stashed)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("parse stashed credential for %q: %w", acct.Label, err)
	}
	if !claude.IsExpired(tokens.ExpiresAt) {
		// Common case: token is still fresh. Just promote the existing
		// blob — no network round-trip needed.
		return stashed, nil
	}
	if tokens.RefreshToken == "" {
		return provider.Credential{}, fmt.Errorf("account %q has no refresh token in its stash; re-add the account with `aimonitor add` to enable silent switching", acct.Label)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "refreshing %q's access token...\n", acct.Label)
	fresh, err := claude.NewTokenRefresher().Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		if claude.IsRefreshExpired(err) {
			return provider.Credential{}, fmt.Errorf("%q's refresh token has expired or been revoked; re-add the account: `aimonitor add`", acct.Label)
		}
		return provider.Credential{}, fmt.Errorf("refresh %q's token: %w", acct.Label, err)
	}

	rebuilt, err := claude.ReplaceTokens(stashed, fresh)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("rebuild credential for %q: %w", acct.Label, err)
	}
	// Persist the rotated tokens back to the stash. Failure here is
	// recoverable (next switch will just refresh again), but worth
	// surfacing because losing rotation means the next refresh attempt
	// uses an older refresh_token that may or may not still be valid.
	if err := claude.StashCredential(ctx, acct.KeyringRef, rebuilt); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: persist rotated tokens for %q failed: %v\n", acct.Label, err)
	}
	return rebuilt, nil
}

// acquireSwitchLock takes the ~/.aimonitor-lock advisory lock. The
// lock file lives in the user's home directory — a stable location
// independent of TMPDIR cleanups (macOS purges /tmp on reboot, which
// would harmlessly recreate the lock; using $HOME just sidesteps
// the question entirely).
func acquireSwitchLock() (*filelock.FileLock, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("acquire switch lock: home dir: %w", err)
	}
	return filelock.Acquire(filepath.Join(home, ".aimonitor-lock"))
}
