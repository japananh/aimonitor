package daemon

import (
	"io"
	"os"
)

// logW is where the daemon writes its operational log lines (the
// "usage:", "auto-swap:", "watcher:" messages). It defaults to os.Stderr
// so foreground runs and tests behave as before; `aimonitor daemon run`
// under launchd swaps in a size-capped, self-rotating file writer via
// SetLogWriter so the daemon log can never grow the disk without bound.
//
// A package var (not a field threaded through every component) is the
// lightest indirection that lets one call site at startup redirect every
// daemon log line at once.
var logW io.Writer = os.Stderr

// SetLogWriter redirects all subsequent daemon log output. Call once at
// daemon startup, before Run. Passing nil resets to os.Stderr.
func SetLogWriter(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	logW = w
}
