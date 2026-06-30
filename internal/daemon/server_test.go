package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// Server tests cover what is constructible + tickable with a fakeProvider.
// With a non-*claude.Provider the UsageScheduler block in Run is skipped, so
// no real-timer goroutines and no network are spun up — Run wires the watcher,
// status publisher, and daily-summary loops, then blocks on the watcher until
// the context is cancelled. The launchd/signal lifecycle (cmd layer) is out of
// scope here; see the report.

// TestNewServer_RequiresStoreAndProvider: the constructor rejects a config
// missing its required dependencies.
func TestNewServer_RequiresStoreAndProvider(t *testing.T) {
	if _, err := NewServer(ServerConfig{}); err == nil {
		t.Errorf("NewServer with no Store/Provider should error")
	}
	s := openStore(t)
	if _, err := NewServer(ServerConfig{Store: s}); err == nil {
		t.Errorf("NewServer with no Provider should error")
	}
}

// TestNewServer_DefaultsRoot: an empty Root defaults under the home dir's
// .claude/projects; an explicit Root is honored verbatim.
func TestNewServer_DefaultsRoot(t *testing.T) {
	s := openStore(t)
	p := &fakeProvider{probes: map[int64]provider.RateLimit{}}

	custom := t.TempDir()
	srv, err := NewServer(ServerConfig{Store: s, Provider: p, Config: config.DefaultConfig(), Root: custom})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.root != custom {
		t.Errorf("root = %q, want %q", srv.root, custom)
	}

	// Empty Root → home/.claude/projects.
	srv2, err := NewServer(ServerConfig{Store: s, Provider: p, Config: config.DefaultConfig()})
	if err != nil {
		t.Fatalf("NewServer default root: %v", err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".claude", "projects"); srv2.root != want {
		t.Errorf("default root = %q, want %q", srv2.root, want)
	}
}

// TestServer_Run_TicksThenStops: Run with a fakeProvider over a temp root
// publishes at least one status row, then unwinds cleanly when the context is
// cancelled (returns the context error, no panic, no leaked real timers).
func TestServer_Run_TicksThenStops(t *testing.T) {
	s := openStore(t)
	p := &fakeProvider{probes: map[int64]provider.RateLimit{}}
	root := t.TempDir()

	srv, err := NewServer(ServerConfig{Store: s, Provider: p, Config: config.DefaultConfig(), Root: root})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Wait until the status publisher has written its first row (published
	// immediately on Run), proving the goroutines are live, then cancel.
	deadline := time.After(3 * time.Second)
	for {
		if _, err := s.GetSetting(ctx, store.SettingsKeyDaemonStatus); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("status row never published")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
