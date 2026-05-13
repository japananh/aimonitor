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
)

// ErrNotFound is returned when the requested (service, account) pair does
// not exist in the keyring.
var ErrNotFound = errors.New("keyring: entry not found")

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
	return defaultKeyring()
}
