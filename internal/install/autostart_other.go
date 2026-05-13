//go:build !darwin

// Package install — non-darwin stubs. Linux gets a real systemd-unit
// writer in Phase 5; for now we no-op so the package compiles on the
// Linux CI runner without a build-tag scope mismatch.
package install

import (
	"errors"
	"runtime"
)

// ErrAutostartUnsupported is returned on platforms where we haven't
// shipped an autostart helper yet.
var ErrAutostartUnsupported = errors.New("autostart not supported on " + runtime.GOOS)

// LaunchAgentPath returns ErrAutostartUnsupported on non-darwin.
func LaunchAgentPath() (string, error) { return "", ErrAutostartUnsupported }

// EnableAutostart returns ErrAutostartUnsupported on non-darwin.
func EnableAutostart(_ string) error { return ErrAutostartUnsupported }

// DisableAutostart returns ErrAutostartUnsupported on non-darwin.
func DisableAutostart() error { return ErrAutostartUnsupported }

// IsAutostartEnabled returns false + ErrAutostartUnsupported on non-darwin.
func IsAutostartEnabled() (bool, error) { return false, ErrAutostartUnsupported }
