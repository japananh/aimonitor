package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/japananh/aimonitor/internal/util/filelock"
)

// Switcher is the silent-account-swap engine, shared by the CLI's
// `aimonitor switch` and the daemon's AutoSwap state machine. The two
// call sites have identical mechanics — file lock, just-in-time
// token refresh, write-back to the live keychain slot — so isolating
// them in one place keeps the audit log, refresh logic, and post-swap
// hooks consistent.
//
// Zero value is not usable; construct via NewSwitcher.
type Switcher struct {
	Store    *store.Store
	Provider provider.Provider

	// Refresher exchanges expired access tokens for fresh ones via
	// Anthropic's OAuth token endpoint. Injected so tests can swap a
	// httptest-backed fake without monkey-patching.
	Refresher *claude.TokenRefresher

	// LockPath is the path to the advisory file lock. Defaults to
	// $HOME/.aimonitor-lock when zero.
	LockPath string

	// PostSwapHook (optional) runs after a successful, non-trivial swap.
	// Used by the daemon to fire SIGINT to running `claude` processes
	// so they re-read the freshly-promoted credential on restart.
	// Errors are logged but do not roll back the swap — the credential
	// has already moved. Not fired when from == to (no-op swap).
	PostSwapHook func(ctx context.Context, from, to string)

	// Stderr receives operational messages (e.g. "refreshing X's
	// token...", "warning: post-swap hook failed"). Nil sends them to
	// os.Stderr.
	Stderr io.Writer
}

// NewSwitcher constructs a Switcher with the production refresher,
// default lock path, and no post-swap hook. Callers add hooks by
// assigning fields after construction.
func NewSwitcher(s *store.Store, p provider.Provider) *Switcher {
	return &Switcher{
		Store:     s,
		Provider:  p,
		Refresher: claude.NewTokenRefresher(),
	}
}

// Switch moves the active credential to the account identified by label.
// Steps:
//
//  1. Acquire the file lock so concurrent swap attempts serialize.
//  2. Resolve label → store.Account.
//  3. Read the stashed credential.
//  4. If the stashed access token is expired, refresh it (and persist
//     the rotated tokens back to the stash).
//  5. Promote the (possibly-refreshed) blob to the live keychain slot.
//  6. Update accounts.last_used_at.
//  7. Best-effort fire PostSwapHook.
//
// Returns nil on success. On any error before step 5, the live slot is
// untouched. After step 5 the swap is committed; later errors are
// surfaced but do not undo the swap.
func (s *Switcher) Switch(ctx context.Context, label string) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	acct, err := s.Store.GetAccountByLabel(ctx, label)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			return fmt.Errorf("no account with label %q (run `aimonitor list`)", label)
		}
		return err
	}

	fromLabel := s.currentLabel(ctx)

	stashed, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return fmt.Errorf("read stash for %q: %w", label, err)
	}
	defer stashed.Zero()

	live, err := s.ensureFreshTokens(ctx, acct, stashed)
	if err != nil {
		return err
	}
	defer live.Zero()

	if err := s.Provider.SetActiveCredential(ctx, live); err != nil {
		return fmt.Errorf("write active credential: %w", err)
	}
	if err := s.Store.UpdateAccountLastUsed(ctx, acct.ID, time.Now()); err != nil {
		fmt.Fprintf(s.stderr(), "warning: switch ok but UpdateLastUsed failed: %v\n", err)
	}

	// Fire PostSwapHook only on a real swap. Switching to the
	// already-active account is a no-op from the user's perspective —
	// no running `claude` process needs to be SIGINT'd, no IDE needs
	// reloading. Suppressing the hook here avoids killing processes
	// that don't need killing.
	if s.PostSwapHook != nil && fromLabel != label {
		// Run on a fresh goroutine so a long-running hook (e.g. SIGINT
		// broadcast + sleep) doesn't block the caller.
		go s.PostSwapHook(context.Background(), fromLabel, label)
	}
	return nil
}

// ensureFreshTokens returns a credential whose access token is valid for
// at least the refresh-buffer window. May refresh and rotate.
func (s *Switcher) ensureFreshTokens(ctx context.Context, acct store.Account, stashed provider.Credential) (provider.Credential, error) {
	tokens, err := claude.ParseCredential(stashed)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("parse stashed credential for %q: %w", acct.Label, err)
	}
	if !claude.IsExpired(tokens.ExpiresAt) {
		return stashed, nil
	}
	if tokens.RefreshToken == "" {
		return provider.Credential{}, fmt.Errorf("account %q has no refresh token in its stash; re-add the account with `aimonitor add`", acct.Label)
	}

	fmt.Fprintf(s.stderr(), "refreshing %q's access token...\n", acct.Label)
	fresh, err := s.Refresher.Refresh(ctx, tokens.RefreshToken)
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
	if err := claude.StashCredential(ctx, acct.KeyringRef, rebuilt); err != nil {
		fmt.Fprintf(s.stderr(), "warning: persist rotated tokens for %q failed: %v\n", acct.Label, err)
	}
	return rebuilt, nil
}

// currentLabel is a best-effort lookup of the currently-active account's
// label, used as the from-label in the audit log and post-swap hook.
// Returns "" on any failure — never blocks the actual swap.
func (s *Switcher) currentLabel(ctx context.Context) string {
	live, err := s.Provider.ActiveCredential(ctx)
	if err != nil || len(live.Bytes) == 0 {
		return ""
	}
	defer live.Zero()
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return ""
	}
	for _, a := range accounts {
		stash, err := claude.RetrieveStash(ctx, a.KeyringRef)
		if err != nil {
			continue
		}
		match := bytes.Equal(stash.Bytes, live.Bytes)
		stash.Zero()
		if match {
			return a.Label
		}
	}
	return ""
}

func (s *Switcher) acquireLock() (*filelock.FileLock, error) {
	path := s.LockPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("acquire switch lock: home dir: %w", err)
		}
		path = filepath.Join(home, ".aimonitor-lock")
	}
	return filelock.Acquire(path)
}

func (s *Switcher) stderr() io.Writer {
	if s.Stderr != nil {
		return s.Stderr
	}
	return os.Stderr
}
