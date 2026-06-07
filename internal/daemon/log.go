package daemon

import (
	"io"
	"log/slog"
	"os"
)

// logger is the daemon's structured logger. It defaults to a text handler
// on stderr (foreground runs and tests); `aimonitor daemon run` swaps in a
// file-backed, size-capped handler via SetLogWriter at startup.
//
// slog's TextHandler emits logrus-style lines —
// `time=2026-06-06T18:52:47.550+07:00 level=INFO msg="…" key=val` — with
// millisecond timestamps, so every line is dated and machine-parseable
// without us formatting timestamps by hand.
var logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

// SetLogWriter routes all subsequent daemon logs to w. Call once at daemon
// startup, before Run. Passing nil resets to stderr.
func SetLogWriter(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	logger = slog.New(slog.NewTextHandler(w, nil))
}

// loggerOver returns a logger writing to w, or the package logger when w is
// nil. Components keep an optional Stderr writer for test injection; this
// bridges that to slog without each component caching its own handler.
func loggerOver(w io.Writer) *slog.Logger {
	if w == nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(w, nil))
}
