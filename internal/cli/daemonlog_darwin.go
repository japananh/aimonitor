//go:build darwin

package cli

import (
	"io"
	"os"
	"path/filepath"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// daemonLogWriter returns a size-capped, self-rotating writer for the
// daemon's operational log on macOS. It writes to the same file the
// LaunchAgent's StandardErrorPath points at (~/Library/Logs/aimonitor/
// aimonitor.daemon.err.log), so existing log readers keep working — but
// now the file can never grow without bound: lumberjack rotates at 5 MB
// and keeps 2 compressed backups (~15 MB ceiling total). The daemon's own
// log lines (the "usage:", "auto-swap:" messages) go through this writer;
// rare Go-runtime panics still land in the same file via the LaunchAgent
// fd, and two O_APPEND writers to one file interleave safely.
//
// Returns nil if the home dir can't be resolved, in which case the caller
// leaves the default os.Stderr writer in place.
func daemonLogWriter() io.Writer {
	// Interactive run (`aimonitor daemon run` in a terminal): leave logs on
	// stderr so the developer sees them live. Only redirect to the capped
	// file when stderr is NOT a TTY — i.e. under launchd, whose
	// StandardErrorPath is the regular file we want to own.
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, "Library", "Logs", "aimonitor", "aimonitor.daemon.log")
	// The daemon's slog handler (installed by daemon.SetLogWriter) timestamps
	// each line itself, so this writer just needs to rotate/cap the bytes.
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    5, // megabytes before rotating
		MaxBackups: 2, // keep 2 old files
		MaxAge:     30,
		Compress:   true,
	}
}
