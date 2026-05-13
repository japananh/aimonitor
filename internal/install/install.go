// Package install wires platform-specific autostart helpers. Symbols
// in this file (no build tag) are available on every GOOS; the rest
// of the package splits along //go:build darwin / linux / other.
package install

import (
	"errors"
	"runtime"
)

// ErrAutostartUnsupported is the sentinel returned from autostart
// helpers on platforms aimonitor hasn't shipped an implementation for.
// Callers wrap or compare with errors.Is.
var ErrAutostartUnsupported = errors.New("aimonitor: autostart not supported on " + runtime.GOOS)
