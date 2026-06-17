package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/japananh/aimonitor/internal/util/filelock"
)

// RefreshAccountUsage fetches and persists current usage for acct using its
// stashed credential, refreshing (and re-stashing) the stashed access token
// first if it has expired.
//
// This DOES rotate an expired token. Rotation can invalidate another
// credential manager's copy of the same account, so it's for user-initiated
// refreshes (popover open, "Refresh usage") and swap-time candidate refresh
// only — inactive accounts are not polled continuously in the background.
//
// It must NOT be called for the active account: rotating the active stash
// here would desync it from the live keychain slot. Use
// Switcher.RefreshActive (live + stash together, under lock) for the active
// account. Callers are responsible for excluding it.
func RefreshAccountUsage(ctx context.Context, st *store.Store, fetcher *claude.UsageFetcher, refresher *claude.TokenRefresher, acct store.Account) (provider.Limits, error) {
	cred, err := ensureFreshStash(ctx, refresher, acct)
	if err != nil {
		// Flag a dead refresh token so the popover can show "re-login".
		markRelogin(ctx, st, acct, err)
		return provider.Limits{}, err
	}
	defer cred.Zero()

	limits, err := fetcher.FetchLimits(ctx, cred)
	if err != nil {
		return provider.Limits{}, err
	}
	limits.AccountID = acct.ID
	if err := st.PutLimits(ctx, acct.ID, limits); err != nil {
		return provider.Limits{}, fmt.Errorf("persist usage for %q: %w", acct.Label, err)
	}
	markRelogin(ctx, st, acct, nil) // a successful refresh clears any stale flag
	return limits, nil
}

// RefreshActiveUsage fetches and persists usage for the ACTIVE account
// using the live credential via the Switcher — refreshing the token under
// the switch lock if expired, keeping live and stash in sync. Use this,
// NOT RefreshAccountUsage, for the active account: its stash must stay
// byte-equal to the live slot, which only RefreshActive guarantees.
func RefreshActiveUsage(ctx context.Context, st *store.Store, sw *Switcher, fetcher *claude.UsageFetcher, acct store.Account) (provider.Limits, error) {
	cred, err := sw.RefreshActive(ctx, acct, false)
	if err != nil {
		markRelogin(ctx, st, acct, err)
		return provider.Limits{}, err
	}
	defer cred.Zero()
	if len(cred.Bytes) == 0 {
		return provider.Limits{}, fmt.Errorf("active account %q has no live credential", acct.Label)
	}
	limits, err := fetcher.FetchLimits(ctx, cred)
	if err != nil {
		return provider.Limits{}, err
	}
	limits.AccountID = acct.ID
	if err := st.PutLimits(ctx, acct.ID, limits); err != nil {
		return provider.Limits{}, fmt.Errorf("persist usage for %q: %w", acct.Label, err)
	}
	markRelogin(ctx, st, acct, nil)
	return limits, nil
}

// ensureFreshStash returns acct's stashed credential with a non-expired
// access token. If the stash is already valid it's returned as-is (no lock,
// no network). If expired, it refreshes under the shared switch lock and
// re-stashes the rotated blob.
//
// The lock is held only around the refresh + write and released before the
// caller's network usage-fetch, so it cannot deadlock a subsequent Switch
// (which re-acquires the same lock). A re-read under the lock avoids
// double-refreshing when the daemon or a switch rotated the token first.
func ensureFreshStash(ctx context.Context, refresher *claude.TokenRefresher, acct store.Account) (provider.Credential, error) {
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read stash for %q: %w", acct.Label, err)
	}
	tokens, err := claude.ParseCredential(stash)
	if err != nil {
		stash.Zero()
		return provider.Credential{}, fmt.Errorf("parse stash for %q: %w", acct.Label, err)
	}
	if !claude.IsExpired(tokens.ExpiresAt) {
		return stash, nil
	}
	stash.Zero()
	if tokens.RefreshToken == "" {
		return provider.Credential{}, fmt.Errorf("account %q has no refresh token in its stash; re-add it", acct.Label)
	}

	lock, err := filelock.Acquire(defaultLockPath())
	if err != nil {
		return provider.Credential{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read under the lock — another process may have refreshed this
	// account's token between our expiry check and acquiring the lock.
	stash2, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("re-read stash for %q: %w", acct.Label, err)
	}
	t2, err := claude.ParseCredential(stash2)
	if err != nil {
		stash2.Zero()
		return provider.Credential{}, fmt.Errorf("parse stash for %q: %w", acct.Label, err)
	}
	if !claude.IsExpired(t2.ExpiresAt) {
		return stash2, nil // someone else refreshed it; adopt theirs, no rotation
	}

	fresh, err := refresher.Refresh(ctx, t2.RefreshToken)
	if err != nil {
		stash2.Zero()
		return provider.Credential{}, fmt.Errorf("refresh %q token: %w", acct.Label, err)
	}
	rebuilt, err := claude.ReplaceTokens(stash2, fresh)
	stash2.Zero()
	if err != nil {
		return provider.Credential{}, fmt.Errorf("rebuild credential for %q: %w", acct.Label, err)
	}
	if err := claude.StashCredential(ctx, acct.KeyringRef, rebuilt); err != nil {
		// Non-fatal: we still hold the fresh cred to fetch with this round.
		logger.Error("persist refreshed stash failed", "account", acct.Label, "err", err)
	}
	return rebuilt, nil
}

// defaultLockPath mirrors Switcher.acquireLock's default so refreshes and
// switches serialize on the same advisory lock.
func defaultLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".aimonitor-lock")
	}
	return filepath.Join(home, ".aimonitor-lock")
}
