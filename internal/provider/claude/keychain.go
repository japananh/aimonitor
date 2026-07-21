package claude

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
)

// ClaudeCodeService is the macOS Keychain service name that Claude Code
// itself reads from and writes to. Documented in claude.ai's CLI source
// and observed empirically on macOS 14+. The account name is the OS
// username (current $USER).
const ClaudeCodeService = "Claude Code-credentials"

// AimonitorServicePrefix namespaces every credential blob aimonitor itself
// stashes for later use, distinguishing them from Claude Code's own slot.
// The full service name is AimonitorServicePrefix + accountID (a UUID).
const AimonitorServicePrefix = "aimonitor-"

// credCacheTTL bounds how long readActive / readStash will serve an
// in-memory copy of a previously-read credential blob without re-shelling
// to /usr/bin/security. Five seconds covers the 2-second daemon poll and
// the multi-account label-resolution sweep that runs on the same tick,
// while keeping post-switch staleness imperceptible (switch invalidates
// the relevant key immediately; the worst case is ~5 s lag for an
// externally-initiated rotation that bypasses this process).
const credCacheTTL = 5 * time.Second

// keychainOps is the slice of provider behaviour that needs the OS
// keyring. It exists as its own type so tests can drive a fake.
type keychainOps struct {
	ring secret.Keyring
	// user is the OS-level account name used in keychain entries. Tests
	// override this; production reads from os/user.Current().
	user string
	// cache amortises the fork+exec cost of /usr/bin/security across the
	// daemon's 2-second status poll. Reads consult the cache; writes
	// invalidate the affected key (or insert the fresh value).
	cache *credCache
}

// newKeychainOps constructs the production keychain backend. Returns an
// error if either the keyring or the OS user lookup fails.
func newKeychainOps() (*keychainOps, error) {
	ring, err := secret.Default()
	if err != nil {
		return nil, fmt.Errorf("init keyring: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("os user: %w", err)
	}
	return &keychainOps{
		ring:  ring,
		user:  u.Username,
		cache: newCredCache(credCacheTTL),
	}, nil
}

// sharedOps returns a process-wide *keychainOps. Constructed on first call;
// reused for all subsequent calls. Required so that the cache amortises
// across both the Provider's lifecycle and the package-level helpers
// (RetrieveStash / StashCredential / DeleteStash) — without a single
// instance every helper would build a fresh cache and the daemon's hot
// poll loop would still fork /usr/bin/security on every tick.
var (
	sharedOpsOnce sync.Once
	sharedOpsVal  *keychainOps
	sharedOpsErr  error

	// opsOverride, when non-nil, replaces the process-wide keychain backend
	// for BOTH the package-level helpers (StashCredential / RetrieveStash /
	// DeleteStash / ReadActiveFresh) and the Provider (whose ops() also routes
	// through sharedOps). It is set ONLY by SetKeyringForTest and is nil in the
	// shipped binary, so production always builds the real backend via
	// secret.Default(). A package global, so tests that set it must not run in
	// parallel.
	opsOverride *keychainOps
)

func sharedOps() (*keychainOps, error) {
	if opsOverride != nil {
		return opsOverride, nil
	}
	sharedOpsOnce.Do(func() {
		sharedOpsVal, sharedOpsErr = newKeychainOps()
	})
	return sharedOpsVal, sharedOpsErr
}

// SetKeyringForTest routes every keychain operation in this package — the
// package-level stash helpers, the live-slot read/write, and the Provider — at
// the given in-memory keyring, so tests in any package can exercise the
// credential flows without touching the real OS Keychain. It returns a restore
// function (which reinstates the previous backend) that the caller MUST defer.
//
// TEST USE ONLY. Production never calls this; the shipped binary leaves
// opsOverride nil and uses secret.Default(). Not safe for t.Parallel.
func SetKeyringForTest(ring secret.Keyring) func() {
	prev := opsOverride
	opsOverride = &keychainOps{
		ring:  ring,
		user:  KeychainUserForTest,
		cache: newCredCache(credCacheTTL),
	}
	return func() { opsOverride = prev }
}

// KeychainUserForTest is the keychain account name SetKeyringForTest uses, so a
// test can seed or read the live slot directly, e.g.
// ring.Set(ClaudeCodeService, KeychainUserForTest, blob). TEST USE ONLY.
const KeychainUserForTest = "aimonitor-test-user"

// readActive returns the bytes currently stored in Claude Code's slot.
// Returns provider.Credential with empty bytes (NOT an error) when the
// slot is empty — first-run onboarding needs to distinguish "no slot
// yet" from "real error reading slot."
func (k *keychainOps) readActive(_ context.Context) (provider.Credential, error) {
	key := cacheKey(ClaudeCodeService, k.user)
	if data, ok := k.cache.get(key); ok {
		return provider.Credential{Bytes: cloneBytes(data)}, nil
	}
	data, err := k.ring.Get(ClaudeCodeService, k.user)
	if errors.Is(err, secret.ErrNotFound) {
		return provider.Credential{}, nil
	}
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read %s/%s: %w", ClaudeCodeService, k.user, err)
	}
	k.cache.put(key, data)
	return provider.Credential{Bytes: cloneBytes(data)}, nil
}

// writeActive overwrites Claude Code's slot. Empty Bytes is rejected —
// callers must use deleteActive for clearing.
func (k *keychainOps) writeActive(_ context.Context, cred provider.Credential) error {
	if len(cred.Bytes) == 0 {
		return errors.New("writeActive: empty credential bytes")
	}
	if err := k.ring.Set(ClaudeCodeService, k.user, cred.Bytes); err != nil {
		return fmt.Errorf("write %s/%s: %w", ClaudeCodeService, k.user, err)
	}
	k.cache.put(cacheKey(ClaudeCodeService, k.user), cred.Bytes)
	return nil
}

// readStash retrieves the credential blob aimonitor stashed under
// AimonitorServicePrefix+accountID. ErrNotFound surfaces as the secret
// package's sentinel.
func (k *keychainOps) readStash(_ context.Context, accountID string) (provider.Credential, error) {
	if accountID == "" {
		return provider.Credential{}, errors.New("readStash: empty account ID")
	}
	service := AimonitorServicePrefix + accountID
	key := cacheKey(service, k.user)
	if data, ok := k.cache.get(key); ok {
		return provider.Credential{Bytes: cloneBytes(data)}, nil
	}
	data, err := k.ring.Get(service, k.user)
	if err != nil {
		return provider.Credential{}, err
	}
	k.cache.put(key, data)
	return provider.Credential{Bytes: cloneBytes(data)}, nil
}

// writeStash saves a credential blob into aimonitor's namespace under
// accountID. Used by onboarding after extracting a fresh blob from
// Claude Code's slot.
func (k *keychainOps) writeStash(_ context.Context, accountID string, cred provider.Credential) error {
	if accountID == "" {
		return errors.New("writeStash: empty account ID")
	}
	if len(cred.Bytes) == 0 {
		return errors.New("writeStash: empty credential bytes")
	}
	service := AimonitorServicePrefix + accountID
	if err := k.ring.Set(service, k.user, cred.Bytes); err != nil {
		return fmt.Errorf("write stash %s: %w", accountID, err)
	}
	k.cache.put(cacheKey(service, k.user), cred.Bytes)
	return nil
}

// deleteStash removes an aimonitor-namespaced credential. Idempotent on
// already-deleted entries via the secret package's ErrNotFound semantics
// (caller can wrap with errors.Is).
func (k *keychainOps) deleteStash(_ context.Context, accountID string) error {
	if accountID == "" {
		return errors.New("deleteStash: empty account ID")
	}
	service := AimonitorServicePrefix + accountID
	err := k.ring.Delete(service, k.user)
	k.cache.invalidate(cacheKey(service, k.user))
	if errors.Is(err, secret.ErrNotFound) {
		return nil
	}
	return err
}

// StashCredential writes cred into the aimonitor-namespaced keyring slot
// identified by ref. The CLI uses this after OnboardingFlow returns a
// fresh credential, paired with an INSERT into the accounts table.
func StashCredential(ctx context.Context, ref string, cred provider.Credential) error {
	k, err := sharedOps()
	if err != nil {
		return err
	}
	return k.writeStash(ctx, ref, cred)
}

// ReadActiveFresh reads Claude Code's live credential slot, bypassing the
// in-memory cache so the caller sees any token another process — Claude
// Code itself, or a second credential manager on the same account — has
// written since our last cached read. Used before a token refresh so we
// don't spend a refresh-endpoint call (and risk a refresh-token rotation
// race) when a still-valid token already sits in the slot.
func ReadActiveFresh(ctx context.Context) (provider.Credential, error) {
	k, err := sharedOps()
	if err != nil {
		return provider.Credential{}, err
	}
	k.cache.invalidate(cacheKey(ClaudeCodeService, k.user))
	return k.readActive(ctx)
}

// InvalidateActiveCache drops any in-memory copy of Claude Code's live
// credential slot so the next read re-shells to the OS keyring. The daemon
// calls this before resolving the active account for its status snapshot: an
// `aimonitor switch` runs in a SEPARATE process and rewrites the live slot
// without touching THIS process's cache, so without the drop the daemon would
// keep serving the pre-switch account for up to credCacheTTL (~5s). That lag is
// what the menu-bar widget waits on before it clears "Switching…", so dropping
// the cache each tick lets a cross-process switch surface on the next 2s poll
// instead of after the full TTL.
func InvalidateActiveCache() {
	k, err := sharedOps()
	if err != nil {
		return
	}
	k.cache.invalidate(cacheKey(ClaudeCodeService, k.user))
}

// RetrieveStash reads the credential previously written under ref.
// Returns secret.ErrNotFound when missing.
func RetrieveStash(ctx context.Context, ref string) (provider.Credential, error) {
	k, err := sharedOps()
	if err != nil {
		return provider.Credential{}, err
	}
	return k.readStash(ctx, ref)
}

// DeleteStash removes the credential at ref. Idempotent on already-missing.
func DeleteStash(ctx context.Context, ref string) error {
	k, err := sharedOps()
	if err != nil {
		return err
	}
	return k.deleteStash(ctx, ref)
}

// cloneBytes returns a fresh slice with the same contents. Used so the
// cache's interior bytes never escape to a caller that might mutate or
// Zero them.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
