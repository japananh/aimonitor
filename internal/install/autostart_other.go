//go:build !darwin && !linux

// Package install — stubs for platforms we don't ship to. macOS and
// Linux each have their own implementations; everything else returns
// ErrAutostartUnsupported so the package still compiles for
// developers running e.g. Windows or FreeBSD.
package install

// LaunchAgentPath returns ErrAutostartUnsupported on non-darwin.
func LaunchAgentPath() (string, error) { return "", ErrAutostartUnsupported }

// EnableAutostart returns ErrAutostartUnsupported on non-darwin.
func EnableAutostart(_ string) error { return ErrAutostartUnsupported }

// DisableAutostart returns ErrAutostartUnsupported on non-darwin.
func DisableAutostart() error { return ErrAutostartUnsupported }

// IsAutostartEnabled returns false + ErrAutostartUnsupported on non-darwin.
func IsAutostartEnabled() (bool, error) { return false, ErrAutostartUnsupported }
