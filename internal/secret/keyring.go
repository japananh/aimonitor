// Package secret bridges aimonitor to the OS-native secret store: macOS
// Keychain on darwin, libsecret (Secret Service DBus) on linux. The package
// is the only one in aimonitor that handles raw OAuth-token bytes; SQLite
// references entries here by (service, account) rather than carrying the
// bytes itself.
//
// The public interface is deliberately tiny: Get / Set / Delete on a
// (service, account) pair. Listing is not part of the interface because
// (a) the two backends differ in what listing semantics they offer and
// (b) aimonitor's SQLite accounts table already tracks every keyring_ref
// it owns, so callers can iterate that and Get() each entry.
package secret

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when the requested (service, account) pair does
// not exist in the keyring.
var ErrNotFound = errors.New("keyring: entry not found")

// ErrUnavailable marks a read that failed for a reason other than the item
// being absent: the backing tool timed out, was killed, or exited abnormally
// (item locked or mid-rewrite by another process). It is usually transient,
// so callers must not treat it as "item missing".
var ErrUnavailable = errors.New("keyring: read failed (transient)")

// ReadError is the concrete error behind ErrUnavailable. It keeps the cause
// that "(no stderr)" used to hide.
type ReadError struct {
	Service, Account string
	Reason           string // e.g. "timed out after 5s", "exit status 36"
	Stderr           string // trimmed tool stderr, may be empty
}

func (e *ReadError) Error() string {
	msg := fmt.Sprintf("keychain find (%s/%s): %s", e.Service, e.Account, e.Reason)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Is lets errors.Is(err, ErrUnavailable) match a ReadError, so callers can test for a transient read failure without unwrapping. It matches only the ErrUnavailable sentinel itself, by identity.
func (e *ReadError) Is(target error) bool { return target == ErrUnavailable }

// Keyring is the OS-keyring abstraction implemented per platform.
type Keyring interface {
	// Get returns the raw bytes stored under (service, account).
	// Returns ErrNotFound if the pair does not exist.
	Get(service, account string) ([]byte, error)

	// Set stores data under (service, account), overwriting any existing
	// entry. The data is treated as secret material — callers should zero
	// their copy after handing it off.
	Set(service, account string, data []byte) error

	// Delete removes the entry under (service, account). Returns
	// ErrNotFound if it did not exist (delete is otherwise idempotent —
	// callers can wrap with errors.Is(err, ErrNotFound) to treat as ok).
	Delete(service, account string) error
}

// Default returns the platform-appropriate Keyring implementation.
// On darwin this is macOS Keychain; on linux it is libsecret via Secret
// Service DBus. Other platforms return an error.
//
// The platform-specific implementations live in keychain_darwin.go and
// libsecret_linux.go (build-tag gated).
func Default() (Keyring, error) {
	if defaultOverride != nil {
		return defaultOverride, nil
	}
	if resilientDefault != nil {
		return resilientDefault, nil
	}
	return defaultKeyring()
}

// resilientDefault, when non-nil, is what Default() returns. Set by
// EnableResilientDefault, which only the long-running daemon calls; one-shot
// CLI commands keep the plain platform keyring.
var resilientDefault *Resilient

// EnableResilientDefault makes Default() return a Resilient wrapper around
// inner (the platform keyring when nil). Call once at daemon startup, before
// the first keychain use.
func EnableResilientDefault(inner Keyring, hook func(Event)) error {
	if inner == nil {
		var err error
		if inner, err = defaultKeyring(); err != nil {
			return err
		}
	}
	resilientDefault = NewResilient(inner, hook)
	return nil
}

// defaultOverride, when non-nil, is what Default() returns. Set ONLY by
// SetDefaultForTest; nil in the shipped binary, so production always uses the
// platform keyring (defaultKeyring).
var defaultOverride Keyring

// SetDefaultForTest makes Default() return ring (e.g. a *MemoryKeyring) so tests
// in any package can exercise keyring-backed code without the real OS Keychain.
// It returns a restore function that reinstates the previous value; the caller
// MUST defer it. TEST USE ONLY — production never calls this. Not safe for
// t.Parallel (it mutates a package global).
func SetDefaultForTest(ring Keyring) func() {
	prev := defaultOverride
	defaultOverride = ring
	return func() { defaultOverride = prev }
}
