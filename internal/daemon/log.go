package daemon

import (
	"io"
	"log/slog"
	"os"
)

// logger is the daemon's structured logger. It defaults to a handler on
// stderr (foreground runs and tests); `aimonitor daemon run` swaps in a
// file-backed, size-capped writer via SetLogWriter at startup.
//
// The handler renders logrus's TTY/FullTimestamp style —
// `INFO[2026-06-08T01:23:45+07:00] message                       key=val` —
// so every line is dated and reads the way the team reads logs. See
// logrusHandler.
var logger = slog.New(newLogrusHandler(os.Stderr))

// SetLogWriter routes all subsequent daemon logs to w. Call once at daemon
// startup, before Run. Passing nil resets to stderr.
func SetLogWriter(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	logger = slog.New(newLogrusHandler(w))
}

// loggerOver returns a logger writing to w, or the package logger when w is
// nil. Components keep an optional Stderr writer for test injection; this
// bridges that to slog without each component caching its own handler.
func loggerOver(w io.Writer) *slog.Logger {
	if w == nil {
		return logger
	}
	return slog.New(newLogrusHandler(w))
}
