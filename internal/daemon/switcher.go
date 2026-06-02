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

	"github.com/japananh/aimonitor/internal/claudeconfig"
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

	// ClaudeConfig reads/writes ~/.claude.json so a switch keeps Claude
	// Code's oauthAccount identity in sync with the swapped tokens. Nil
	// disables claude.json patching (the swap still moves the keychain
	// tokens) — used when the home dir is unresolvable, and overridable
	// in tests to point at a temp file.
	ClaudeConfig *claudeconfig.Store

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
	// claudeconfig.New only fails when the home dir is unresolvable; in
	// that case leave ClaudeConfig nil and the switch degrades to a
	// keychain-only swap (no claude.json patching).
	cc, _ := claudeconfig.New()
	return &Switcher{
		Store:        s,
		Provider:     p,
		Refresher:    claude.NewTokenRefresher(),
		ClaudeConfig: cc,
	}
}

// Switch moves the active credential to the account identified by label.
// Transaction:
//
//  1. Acquire the file lock so concurrent swap attempts serialize.
//  2. Resolve label → store.Account.
//  3. Snapshot the outgoing account: write its current live blob back to
//     its stash (captures any refresh-token rotation Claude Code did
//     while running) and backfill its identity from ~/.claude.json.
//  4. Read the target stash; refresh its access token if expired.
//  5. Promote the (possibly-refreshed) blob to the live keychain slot.
//  6. Patch ~/.claude.json oauthAccount → target identity. Roll the live
//     slot back to the outgoing blob if this fails, so tokens and
//     identity never disagree.
//  7. Update accounts.last_used_at; fire PostSwapHook.
//
// Returns nil on success. On any error before step 5 the live slot is
// untouched. A step-6 failure rolls step 5 back.
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

	// Read the current live blob once: it identifies the outgoing account
	// (byte-match), seeds the snapshot, and is the rollback restore point.
	prevLive, _ := s.Provider.ActiveCredential(ctx) // best-effort
	defer prevLive.Zero()
	outgoing, outgoingFound := s.matchAccount(ctx, prevLive)
	fromLabel := ""
	if outgoingFound {
		fromLabel = outgoing.Label
		if outgoing.ID != acct.ID {
			s.snapshotOutgoing(ctx, outgoing, prevLive)
		}
	}

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

	// Patch ~/.claude.json so Claude Code's active-account identity tracks
	// the tokens we just installed. Roll the live slot back on failure so
	// tokens and identity stay consistent.
	if err := s.patchClaudeConfig(ctx, acct); err != nil {
		if len(prevLive.Bytes) > 0 {
			if rbErr := s.Provider.SetActiveCredential(ctx, prevLive); rbErr != nil {
				return fmt.Errorf("patch claude.json: %w; rollback live: %w", err, rbErr)
			}
		}
		return fmt.Errorf("patch claude.json: %w", err)
	}

	if err := s.Store.UpdateAccountLastUsed(ctx, acct.ID, time.Now()); err != nil {
		fmt.Fprintf(s.stderr(), "warning: switch ok but UpdateLastUsed failed: %v\n", err)
	}

	// Fire PostSwapHook only on a real swap. Switching to the
	// already-active account is a no-op from the user's perspective —
	// no running `claude` process needs to be SIGINT'd. Suppressing the
	// hook here avoids killing processes that don't need killing.
	if s.PostSwapHook != nil && fromLabel != label {
		// Run on a fresh goroutine so a long-running hook (e.g. SIGINT
		// broadcast + sleep) doesn't block the caller.
		go s.PostSwapHook(context.Background(), fromLabel, label)
	}
	return nil
}

// snapshotOutgoing writes the outgoing account's current live blob back
// to its stash and backfills its identity from ~/.claude.json. Both are
// best-effort — a failure here must never block the switch.
//
// Why snapshot: Claude Code rotates the active account's refresh token
// while running. Without re-capturing the live blob before we overwrite
// it, the outgoing account's stash keeps a stale refresh token and a
// later switch BACK to it fails with invalid_grant.
//
// Why backfill identity here: at this instant ~/.claude.json's
// oauthAccount still describes the outgoing account (we haven't patched
// it yet), so it's the natural moment to fill in identity for accounts
// added before identity capture existed.
func (s *Switcher) snapshotOutgoing(ctx context.Context, outgoing store.Account, prevLive provider.Credential) {
	if len(prevLive.Bytes) > 0 {
		if err := claude.StashCredential(ctx, outgoing.KeyringRef, prevLive); err != nil {
			fmt.Fprintf(s.stderr(), "warning: snapshot outgoing %q credential: %v\n", outgoing.Label, err)
		}
	}
	if outgoing.Email == "" && s.ClaudeConfig != nil {
		if oa, err := s.ClaudeConfig.ReadOAuthAccount(ctx); err == nil && oa != nil && oa.EmailAddress != "" {
			_ = s.Store.UpdateAccountIdentity(ctx, outgoing.ID, oa.EmailAddress, oa.OrganizationUUID, oa.OrganizationName)
		}
	}
}

// patchClaudeConfig rewrites ~/.claude.json's oauthAccount to the target
// account's identity. No-op (with a hint) when the target has no captured
// identity yet, or when claude.json handling is disabled — the keychain
// swap still stands; only the identity sync is skipped.
func (s *Switcher) patchClaudeConfig(ctx context.Context, acct store.Account) error {
	if s.ClaudeConfig == nil {
		return nil
	}
	if acct.Email == "" {
		fmt.Fprintf(s.stderr(), "note: %q has no stored identity yet; skipping ~/.claude.json update (re-run `aimonitor add %s` to capture it)\n", acct.Label, acct.Label)
		return nil
	}
	return s.ClaudeConfig.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{
		EmailAddress:     acct.Email,
		OrganizationName: acct.OrganizationName,
		OrganizationUUID: acct.OrganizationUUID,
	})
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

// matchAccount returns the account whose stash byte-equals live, or
// (_, false) when none matches (fresh install, or an externally-rotated
// live blob that no stash mirrors yet). Best-effort: any read error
// yields not-found rather than blocking the switch.
func (s *Switcher) matchAccount(ctx context.Context, live provider.Credential) (store.Account, bool) {
	if len(live.Bytes) == 0 {
		return store.Account{}, false
	}
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return store.Account{}, false
	}
	for _, a := range accounts {
		stash, err := claude.RetrieveStash(ctx, a.KeyringRef)
		if err != nil {
			continue
		}
		match := bytes.Equal(stash.Bytes, live.Bytes)
		stash.Zero()
		if match {
			return a, true
		}
	}
	return store.Account{}, false
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
