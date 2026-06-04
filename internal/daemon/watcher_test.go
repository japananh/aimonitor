package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

const oneAssistantLine = `{"type":"assistant","timestamp":"2026-05-13T08:00:01Z","message":{"model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"

// collector is a thread-safe accumulator the tests use to read back what
// the watcher emitted while it was running.
type collector struct {
	mu      sync.Mutex
	samples []SampleEvent
	errs    []error
}

func (c *collector) onSample(ev SampleEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, ev)
}

func (c *collector) onError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, err)
}

func (c *collector) sampleCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.samples)
}

// waitFor polls until `cond` returns true, or `timeout` elapses. Returns
// true on success. Used to wait for the watcher's async event delivery
// without relying on hard-coded sleeps.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWatcher_BootstrapProcessesExistingFiles(t *testing.T) {
	root := t.TempDir()
	// Create one project dir with a JSONL file already in place.
	proj := filepath.Join(root, "encoded-cwd")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	jsonlPath := filepath.Join(proj, "session.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(oneAssistantLine), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := &collector{}
	w, err := NewWatcher(WatcherConfig{
		Root:     root,
		Store:    openStore(t),
		OnSample: c.onSample,
		OnError:  c.onError,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if !waitFor(2*time.Second, func() bool { return c.sampleCount() >= 1 }) {
		t.Fatalf("bootstrap did not emit a sample; have %d", c.sampleCount())
	}

	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}

func TestWatcher_PicksUpAppendedLines(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "encoded-cwd")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	jsonlPath := filepath.Join(proj, "session.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(oneAssistantLine), 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	c := &collector{}
	w, err := NewWatcher(WatcherConfig{
		Root:     root,
		Store:    openStore(t),
		OnSample: c.onSample,
		OnError:  c.onError,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if !waitFor(2*time.Second, func() bool { return c.sampleCount() >= 1 }) {
		t.Fatalf("bootstrap did not deliver first sample; have %d", c.sampleCount())
	}

	// Append a second line and confirm the watcher picks it up.
	f, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("reopen for append: %v", err)
	}
	if _, err := f.WriteString(oneAssistantLine); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	if !waitFor(2*time.Second, func() bool { return c.sampleCount() >= 2 }) {
		t.Fatalf("watcher did not pick up appended sample; have %d", c.sampleCount())
	}

	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
}

func TestWatcher_ResumesFromStoredOffset(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "encoded-cwd")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	jsonlPath := filepath.Join(proj, "session.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(oneAssistantLine+oneAssistantLine), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := openStore(t)
	// Pre-seed an offset that's just past the first line.
	firstLen := int64(len(oneAssistantLine))
	if err := s.PutOffset(context.Background(), store.Offset{
		Path:       jsonlPath,
		ByteOffset: firstLen,
		MtimeNs:    time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("seed offset: %v", err)
	}

	c := &collector{}
	w, err := NewWatcher(WatcherConfig{
		Root:     root,
		Store:    s,
		OnSample: c.onSample,
		OnError:  c.onError,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Bootstrap should see one new line (the second one), not two.
	if !waitFor(2*time.Second, func() bool { return c.sampleCount() >= 1 }) {
		t.Fatalf("did not emit any sample; have %d", c.sampleCount())
	}
	// Give the watcher a moment in case it mis-resumed and emits more.
	time.Sleep(150 * time.Millisecond)
	if got := c.sampleCount(); got != 1 {
		t.Errorf("emitted %d samples, want 1 (offset resume failed)", got)
	}

	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
}

func TestWatcher_HandlesMissingRootByWatchingParent(t *testing.T) {
	// Real-world case: user just installed aimonitor but hasn't used
	// Claude Code yet, so ~/.claude/projects doesn't exist. The watcher
	// should not fail to start; it watches the parent and picks up
	// project dirs when they appear.
	parent := t.TempDir()
	missing := filepath.Join(parent, "projects")

	c := &collector{}
	w, err := NewWatcher(WatcherConfig{
		Root:     missing,
		Store:    openStore(t),
		OnSample: c.onSample,
		OnError:  c.onError,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Give the bootstrap a moment to set up.
	time.Sleep(100 * time.Millisecond)

	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned unexpected error for missing root: %v", err)
	}
	// No samples expected — we just want it to not crash.
	if c.sampleCount() != 0 {
		t.Errorf("missing-root case: got %d samples, want 0", c.sampleCount())
	}
}

func TestWatcher_MissingRootAndParentDoesNotKillDaemon(t *testing.T) {
	// Fresh machine: ~/.claude doesn't exist at all (Claude Code never run),
	// so BOTH the watch root and its parent are absent. The watcher must not
	// return a fatal error — doing so tears down the whole daemon (and with
	// it OAuth usage polling), which is exactly the "daemon not running"
	// crash-loop a clean `brew install` hit. The watcher should create the
	// tree (or degrade) and reach its blocking event loop instead.
	base := t.TempDir()
	root := filepath.Join(base, "claude", "projects") // parent "claude" is missing

	c := &collector{}
	w, err := NewWatcher(WatcherConfig{
		Root:     root,
		Store:    openStore(t),
		OnSample: c.onSample,
		OnError:  c.onError,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Bootstrap must reach the blocking loop, not exit early with the old
	// fatal "root does not exist (and parent unavailable)" error.
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-runDone:
		t.Fatalf("Run exited early (daemon would die on a fresh machine): %v", err)
	default:
		// Still running — correct.
	}

	cancel()
	if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}

// Sanity: NewWatcher validates required config.
func TestNewWatcher_Validation(t *testing.T) {
	if _, err := NewWatcher(WatcherConfig{}); err == nil {
		t.Error("missing root + store: want error, got nil")
	}
	if _, err := NewWatcher(WatcherConfig{Root: "/tmp"}); err == nil {
		t.Error("missing store: want error, got nil")
	}
	if _, err := NewWatcher(WatcherConfig{Store: openStore(t)}); err == nil {
		t.Error("missing root: want error, got nil")
	}
}

// Compile-time check: claude.Sample's zero value is usable.
var _ claude.Sample = claude.Sample{}
