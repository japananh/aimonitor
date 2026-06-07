package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

	// Stderr receives operational messages (e.g. "refreshing X's
	// token...", "warning: post-swap hook failed"). Nil sends them to
	// the daemon log writer.
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
	if outgoingFound && outgoing.ID != acct.ID {
		s.snapshotOutgoing(ctx, outgoing, prevLive)
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
		s.log().Warn("switch ok but UpdateLastUsed failed", "err", err)
	}

	// Deliberately no post-swap action on running `claude` processes.
	// Current Claude Code re-reads the credential from the keychain during
	// a live session, so running sessions adopt the swapped-in account on
	// their own — verified empirically (2026-06-03): two live sessions
	// followed a manual A→B switch without restarting (`/usage` showed B in
	// both). An earlier version SIGINT'd running sessions here, built on
	// the assumption that sessions cache the old token until restart; that
	// assumption is false on current Claude Code, so the kill only
	// interrupted live work for no benefit.
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
			s.log().Warn("snapshot outgoing credential failed", "account", outgoing.Label, "err", err)
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
		s.log().Info("skipping ~/.claude.json update — no stored identity yet (re-run `aimonitor add` to capture it)", "account", acct.Label)
		return nil
	}
	return s.ClaudeConfig.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{
		EmailAddress:     acct.Email,
		OrganizationName: acct.OrganizationName,
		OrganizationUUID: acct.OrganizationUUID,
	})
}

// RefreshActive ensures Claude Code's live credential slot holds a
// non-expired access token, refreshing it via the embedded refresh token
// when needed. It is the background daemon's analogue of the just-in-time
// refresh Switch() does for a swap target, but it operates on the already-
// active account and runs under the same advisory lock so a background
// refresh can never race a user-initiated or auto swap.
//
// acct identifies the active account (resolved by the caller) so the
// rebuilt blob can be mirrored into that account's stash, keeping the
// byte-match the scheduler uses to resolve "active" in sync with the slot.
//
// force=false (the normal path) refreshes only when the live token is at
// or past its expiry buffer; an unexpired token returns (cred, nil) with
// no network call. force=true refreshes unconditionally — used to recover
// from a 401 the expiry check didn't predict (a revoked token, or a blob
// whose expiresAt is absent or wrong).
//
// To avoid spending refresh-endpoint calls (and tripping Anthropic's abuse
// classifiers) when another tool — Claude Code itself, or a second menu-bar
// app — manages the same account, it reads the live slot fresh (cache-
// bypassed) before refreshing, and on a refresh failure re-reads once more:
// if a valid token has appeared meanwhile, it adopts that with no error
// rather than reporting failure for a benign cross-tool rotation race.
//
// Returns the current valid live credential; the caller owns zeroing it.
func (s *Switcher) RefreshActive(ctx context.Context, acct store.Account, force bool) (provider.Credential, error) {
	lock, err := s.acquireLock()
	if err != nil {
		return provider.Credential{}, err
	}
	defer func() { _ = lock.Release() }()

	live, err := claude.ReadActiveFresh(ctx)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read live credential: %w", err)
	}
	if len(live.Bytes) == 0 {
		return live, nil // nothing in the slot yet
	}

	tokens, err := claude.ParseCredential(live)
	if err != nil {
		live.Zero()
		return provider.Credential{}, fmt.Errorf("parse live credential: %w", err)
	}
	if !force && !claude.IsExpired(tokens.ExpiresAt) {
		// Live token is valid and we're not forcing a refresh. If the active
		// account's stash has drifted from the live blob (Claude Code or
		// another tool rotated the live token), re-mirror it so byte-match
		// resolution recovers and the stash keeps a current refresh token
		// for a future switch back. No network call either way.
		s.healStash(ctx, acct, live)
		return live, nil // still valid — no refresh
	}
	if tokens.RefreshToken == "" {
		live.Zero()
		return provider.Credential{}, fmt.Errorf("active account %q has no refresh token in the live slot; re-login via `aimonitor add`", acct.Label)
	}

	s.log().Info("refreshing access token", "account", acct.Label)
	fresh, rerr := s.Refresher.Refresh(ctx, tokens.RefreshToken)
	if rerr != nil {
		// Another tool may have rotated the refresh token between our read
		// and this call. Re-read the live slot: if a valid token is now
		// present, adopt it — the refresh failed only because we lost a
		// benign race, not because the account is broken.
		live.Zero()
		if relive, e := claude.ReadActiveFresh(ctx); e == nil && len(relive.Bytes) > 0 {
			if t2, pe := claude.ParseCredential(relive); pe == nil && !claude.IsExpired(t2.ExpiresAt) {
				return relive, nil
			}
			relive.Zero()
		}
		return provider.Credential{}, rerr
	}

	rebuilt, err := claude.ReplaceTokens(live, fresh)
	live.Zero()
	if err != nil {
		return provider.Credential{}, fmt.Errorf("rebuild live credential: %w", err)
	}

	// Mirror into the active account's stash BEFORE writing the live slot.
	// The scheduler resolves "active" by byte-matching the live blob against
	// each stash; if we wrote live first and the stash write then failed,
	// the two would disagree and resolution would silently break. Writing
	// the stash first means a failure here aborts before we touch the live
	// slot, leaving both at their previous (consistent) value to retry.
	//
	// Same identity gate as healStash: the rebuilt credential descends from
	// whatever was in the live slot, so if acct's attribution is wrong
	// (login race), mirroring would overwrite acct's real credential with
	// another account's. Skip the mirror, keep the live-slot refresh —
	// identity-based resolution recovers; a corrupted stash does not.
	if acct.KeyringRef != "" {
		if !s.liveIdentityMatches(ctx, acct) {
			s.log().Warn("live login mismatch; refreshed live slot but skipping stash mirror to avoid cross-account corruption", "account", acct.Label)
		} else if err := claude.StashCredential(ctx, acct.KeyringRef, rebuilt); err != nil {
			rebuilt.Zero()
			return provider.Credential{}, fmt.Errorf("mirror refreshed tokens to %q stash: %w", acct.Label, err)
		}
	}
	if err := s.Provider.SetActiveCredential(ctx, rebuilt); err != nil {
		rebuilt.Zero()
		return provider.Credential{}, fmt.Errorf("write refreshed live credential: %w", err)
	}
	return rebuilt, nil
}

// healStash re-mirrors the live blob into acct's stash when the two have
// drifted apart — keeping byte-match resolution working and the stash's
// refresh token current for a future switch back. Best-effort: a read or
// write failure is logged and ignored (the live slot is untouched). No-op
// when they already match. Caller holds the switch lock.
//
// The write is gated on liveIdentityMatches: acct is the CALLER's idea of
// who is active, and when that attribution is wrong (a `claude /login`
// race, or resolution lagging a rapid switch), healing would copy another
// account's credential into acct's stash — observed live 2026-06-04, where
// two accounts ended up sharing one credential and the real one was lost.
// A skipped heal is recoverable (stale stash, resolution falls back to
// identity); a wrong heal is corruption.
func (s *Switcher) healStash(ctx context.Context, acct store.Account, live provider.Credential) {
	if acct.KeyringRef == "" || len(live.Bytes) == 0 {
		return
	}
	if stash, err := claude.RetrieveStash(ctx, acct.KeyringRef); err == nil {
		drifted := !bytes.Equal(stash.Bytes, live.Bytes)
		stash.Zero()
		if !drifted {
			return
		}
	}
	if !s.liveIdentityMatches(ctx, acct) {
		s.log().Warn("live login mismatch; skipping stash heal to avoid cross-account corruption", "account", acct.Label)
		return
	}
	if err := claude.StashCredential(ctx, acct.KeyringRef, live); err != nil {
		s.log().Warn("re-sync stash to live blob failed", "account", acct.Label, "err", err)
	}
}

// liveIdentityMatches reports whether ~/.claude.json's oauthAccount — the
// identity Claude Code itself maintains for the live credential — agrees
// with acct. It gates every write of live-slot bytes into an account's
// stash. Returns true (allow) when there is no evidence to refuse on:
// no claude.json handling, no captured identity on the account, or no
// readable oauthAccount. Claude Code rewrites oauthAccount on /login and
// Switch patches it after a swap, so on any settled state the two agree;
// disagreement means the attribution is mid-race and must not be trusted
// for a write.
func (s *Switcher) liveIdentityMatches(ctx context.Context, acct store.Account) bool {
	if s.ClaudeConfig == nil || acct.Email == "" {
		return true
	}
	oa, err := s.ClaudeConfig.ReadOAuthAccount(ctx)
	if err != nil || oa == nil || oa.EmailAddress == "" {
		return true
	}
	return strings.EqualFold(oa.EmailAddress, acct.Email)
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

	s.log().Info("refreshing access token", "account", acct.Label)
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
		s.log().Warn("persist rotated tokens failed", "account", acct.Label, "err", err)
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

func (s *Switcher) log() *slog.Logger {
	return loggerOver(s.Stderr)
}
