package daemon

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// TestSetLogWriter_Format confirms the daemon logger emits logrus's
// TTY/FullTimestamp style — `LEVL[timestamp] message key=val` — so every
// line is dated and reads the way the team reads logs.
func TestSetLogWriter_Format(t *testing.T) {
	var buf bytes.Buffer
	SetLogWriter(&buf)
	defer SetLogWriter(nil)

	logger.Info("usage throttled", "status", 429, "wait", "5m0s")

	got := strings.TrimSpace(buf.String())
	// Leads with INFO[<timestamp>] — the level code then the bracketed time.
	if !strings.HasPrefix(got, "INFO[") {
		t.Errorf("log line should start with INFO[…], got %q", got)
	}
	for _, want := range []string{"] usage throttled", "status=429", "wait=5m0s"} {
		if !strings.Contains(got, want) {
			t.Errorf("log line %q missing %q", got, want)
		}
	}
	// Must NOT carry slog's logfmt scaffolding anymore.
	for _, absent := range []string{"time=", "level=", "msg="} {
		if strings.Contains(got, absent) {
			t.Errorf("log line %q should not contain logfmt %q", got, absent)
		}
	}
}

// TestLogger_ConcurrentWritesNoInterleave is the load-bearing concurrency
// check: many daemon goroutines (scheduler, status publisher, auto-swap,
// switcher) log to the SAME file at once. slog's handler holds a mutex per
// logger, so each line is written atomically. We assert that N concurrent
// logs produce exactly N well-formed lines — none torn or merged.
func TestLogger_ConcurrentWritesNoInterleave(t *testing.T) {
	var buf bytes.Buffer
	SetLogWriter(&buf)
	defer SetLogWriter(nil)

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			logger.Info("tick", "i", i)
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d (torn/merged writes?)", len(lines), n)
	}
	// Every line must be a complete record: starts with the INFO[…] level,
	// carries the message, and ends with its own i= field (proving no two
	// writes interleaved).
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "INFO[") || !strings.Contains(ln, "tick") || !strings.Contains(ln, "i=") {
			t.Fatalf("malformed/interleaved line: %q", ln)
		}
	}
}
