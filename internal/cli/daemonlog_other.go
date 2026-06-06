//go:build !darwin

package cli

import "io"

// daemonLogWriter is a no-op off macOS: on Linux the daemon runs under
// systemd, which captures stdout/stderr into the journal and rotates it
// itself, so aimonitor shouldn't own a log file there. Returning nil keeps
// the daemon's default os.Stderr writer (→ journald).
func daemonLogWriter() io.Writer { return nil }
