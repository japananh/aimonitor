package claude

import (
	"context"
	"errors"
	"fmt"
	"os/user"

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

// keychainOps is the slice of provider behaviour that needs the OS
// keyring. It exists as its own type so tests can drive a fake.
type keychainOps struct {
	ring secret.Keyring
	// user is the OS-level account name used in keychain entries. Tests
	// override this; production reads from os/user.Current().
	user string
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
	return &keychainOps{ring: ring, user: u.Username}, nil
}

// readActive returns the bytes currently stored in Claude Code's slot.
// Returns provider.Credential with empty bytes (NOT an error) when the
// slot is empty — first-run onboarding needs to distinguish "no slot
// yet" from "real error reading slot."
func (k *keychainOps) readActive(_ context.Context) (provider.Credential, error) {
	data, err := k.ring.Get(ClaudeCodeService, k.user)
	if errors.Is(err, secret.ErrNotFound) {
		return provider.Credential{}, nil
	}
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read %s/%s: %w", ClaudeCodeService, k.user, err)
	}
	return provider.Credential{Bytes: data}, nil
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
	return nil
}

// readStash retrieves the credential blob aimonitor stashed under
// AimonitorServicePrefix+accountID. ErrNotFound surfaces as the secret
// package's sentinel.
func (k *keychainOps) readStash(_ context.Context, accountID string) (provider.Credential, error) {
	if accountID == "" {
		return provider.Credential{}, errors.New("readStash: empty account ID")
	}
	data, err := k.ring.Get(AimonitorServicePrefix+accountID, k.user)
	if err != nil {
		return provider.Credential{}, err
	}
	return provider.Credential{Bytes: data}, nil
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
	if err := k.ring.Set(AimonitorServicePrefix+accountID, k.user, cred.Bytes); err != nil {
		return fmt.Errorf("write stash %s: %w", accountID, err)
	}
	return nil
}

// deleteStash removes an aimonitor-namespaced credential. Idempotent on
// already-deleted entries via the secret package's ErrNotFound semantics
// (caller can wrap with errors.Is).
func (k *keychainOps) deleteStash(_ context.Context, accountID string) error {
	if accountID == "" {
		return errors.New("deleteStash: empty account ID")
	}
	err := k.ring.Delete(AimonitorServicePrefix+accountID, k.user)
	if errors.Is(err, secret.ErrNotFound) {
		return nil
	}
	return err
}

// StashCredential writes cred into the aimonitor-namespaced keyring slot
// identified by ref. The CLI uses this after OnboardingFlow returns a
// fresh credential, paired with an INSERT into the accounts table.
func StashCredential(ctx context.Context, ref string, cred provider.Credential) error {
	k, err := newKeychainOps()
	if err != nil {
		return err
	}
	return k.writeStash(ctx, ref, cred)
}

// RetrieveStash reads the credential previously written under ref.
// Returns secret.ErrNotFound when missing.
func RetrieveStash(ctx context.Context, ref string) (provider.Credential, error) {
	k, err := newKeychainOps()
	if err != nil {
		return provider.Credential{}, err
	}
	return k.readStash(ctx, ref)
}

// DeleteStash removes the credential at ref. Idempotent on already-missing.
func DeleteStash(ctx context.Context, ref string) error {
	k, err := newKeychainOps()
	if err != nil {
		return err
	}
	return k.deleteStash(ctx, ref)
}
