//go:build !darwin && !linux

package secret

import (
	"errors"
	"runtime"
)

// On unsupported platforms (Windows, *BSD, etc.) Default returns an error.
// v1.0.0-beta only targets darwin + linux; other platforms wait for v2+.
func defaultKeyring() (Keyring, error) {
	return nil, errors.New("aimonitor: no keyring implementation for " + runtime.GOOS)
}
