package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// Server is the long-running daemon process. It owns the watcher and
// the auto-switcher; the public Run blocks until ctx is cancelled.
//
// State plumbing:
//
//	JSONL appends ─► Watcher ──► SampleEvent ──► AutoSwitcher.OnSample
//	                                                  │
//	                                                  ▼
//	                                            (writes audit + probe rows to SQLite)
//
// CLI commands (`aimonitor list / log / status / doctor`) read SQLite
// directly; no Unix socket needed in v1.
type Server struct {
	store    *store.Store
	provider provider.Provider
	cfg      config.Config
	root     string

	watcher *Watcher
	auto    *AutoSwitcher
}

// ServerConfig wires every dependency from cmd/aimonitor/daemon-run.
type ServerConfig struct {
	Store    *store.Store
	Provider provider.Provider
	Config   config.Config

	// Root overrides the JSONL watch root. Empty defaults to
	// ~/.claude/projects.
	Root string
}

// NewServer constructs but does not start the daemon. Call Run() to
// block on it.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil || cfg.Provider == nil {
		return nil, errors.New("ServerConfig: Store and Provider required")
	}
	root := cfg.Root
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		root = filepath.Join(home, ".claude", "projects")
	}
	return &Server{
		store:    cfg.Store,
		provider: cfg.Provider,
		cfg:      cfg.Config,
		root:     root,
	}, nil
}

// Run starts the watcher + auto-switcher and blocks until ctx is
// cancelled. Watcher errors during operation are reported via the
// OnError callback (logged to stderr).
func (s *Server) Run(ctx context.Context) error {
	auto, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s.store,
		Provider: s.provider,
		Config:   s.cfg,
	})
	if err != nil {
		return fmt.Errorf("auto-switcher: %w", err)
	}
	s.auto = auto

	w, err := NewWatcher(WatcherConfig{
		Root:     s.root,
		Store:    s.store,
		OnSample: auto.OnSample,
		OnError: func(err error) {
			fmt.Fprintf(os.Stderr, "watcher: %v\n", err)
		},
	})
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	s.watcher = w

	return w.Run(ctx)
}
