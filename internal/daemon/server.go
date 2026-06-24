package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
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

// Run starts the watcher + auto-switcher + status publisher and blocks
// until ctx is cancelled. Watcher errors during operation are reported
// via the OnError callback (logged to stderr).
//
// Concurrency: the status publisher runs in a goroutine; watcher.Run is
// the foreground loop. When ctx cancels, both unwind: the publisher's
// ticker loop returns, and the watcher's event loop exits.
func (s *Server) Run(ctx context.Context) error {
	// A startup line marks each daemon (re)start in the log — useful for
	// "when did it last restart?" when reading a bug report, and it gives
	// the timestamped log writer an immediate line to emit on every boot
	// (the steady-state daemon is otherwise silent on success).
	logger.Info("daemon started", "pid", os.Getpid())
	defer logger.Info("daemon stopped", "pid", os.Getpid())

	auto, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s.store,
		Provider: s.provider,
		Config:   s.cfg,
	})
	if err != nil {
		return fmt.Errorf("auto-switcher: %w", err)
	}
	s.auto = auto

	// SampleRecorder persists each usage line to usage_samples, attributed
	// to the account active at write time, powering the per-account token
	// breakdown. It runs alongside auto.OnSample (which keeps its in-memory
	// session-bar estimate); both consume the same watcher callback.
	cc, _ := claudeconfig.New() // nil when home is unresolvable → byte-match only
	recorder := NewSampleRecorder(
		s.store,
		func(ctx context.Context) (store.Account, bool, error) {
			return ResolveActiveAccount(ctx, s.store, s.provider, cc)
		},
		func(err error) { logger.Error("sample recorder error", "err", err) },
	)

	w, err := NewWatcher(WatcherConfig{
		Root:  s.root,
		Store: s.store,
		OnSample: func(ev SampleEvent) {
			auto.OnSample(ev)
			recorder.Record(ctx, ev)
		},
		OnError: func(err error) {
			logger.Error("watcher error", "err", err)
		},
	})
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	s.watcher = w

	pub := &StatusPublisher{
		Store:       s.store,
		Auto:        auto,
		Interval:    2 * time.Second,
		ActiveLabel: resolveActiveLabel(s),
		// Detect active-account changes the daemon didn't perform (another
		// credential manager rewrote the live slot): notify + audit them.
		ExternalWatch: &ExternalSwitchWatcher{Store: s.store},
		// Surface a live account aimonitor doesn't manage so the widget can
		// offer to import it.
		UnknownActiveEmail: resolveUnknownActiveEmail(s),
	}
	go func() { _ = pub.Run(ctx) }()

	// DailySummaryNotifier posts a once-a-day recap of the previous day's
	// token usage (from usage_samples). Provider-agnostic — it only reads the
	// samples the watcher records — so it runs regardless of provider.
	summary := &DailySummaryNotifier{Store: s.store}
	go func() { _ = summary.Run(ctx) }()

	// UsageScheduler is Claude-specific in v1 (only Claude has an OAuth
	// usage endpoint we know about). When v2 adds a second provider the
	// scheduler will move behind a Provider interface method.
	//
	// We construct the chain: Switcher -> AutoSwapper -> UsageScheduler.
	// Each component is stateless w.r.t. the others beyond the
	// function-pointer wires established here.
	//
	// Deliberately NO post-swap session kill: running `claude` sessions
	// re-read the keychain credential mid-session and adopt the swapped-in
	// account on their own (verified live 2026-06-03), so the old SIGINT
	// sweep only interrupted work for no benefit. See Switcher.Switch.
	if _, ok := s.provider.(*claude.Provider); ok {
		switcher := NewSwitcher(s.store, s.provider)
		fetcher := claude.NewUsageFetcher()

		autoSwap := &AutoSwapper{
			Store:    s.store,
			Provider: s.provider,
			Switcher: switcher,
			// Just-in-time candidate refresh at decision time (non-active
			// accounts only; RefreshAccountUsage rotates an expired token
			// under the switch lock).
			RefreshUsage: func(ctx context.Context, acct store.Account) (provider.Limits, error) {
				return RefreshAccountUsage(ctx, s.store, fetcher, switcher.Refresher, acct)
			},
		}

		// Threshold notifier: warns the user as the active account nears its
		// limit when auto-swap is OFF (auto-swap posts its own banners when ON).
		notifier := &ThresholdNotifier{Store: s.store}

		usage := &UsageScheduler{
			Store:         s.store,
			Provider:      s.provider,
			Fetcher:       fetcher,
			RefreshActive: switcher.RefreshActive,
			ResolveActive: func(ctx context.Context) (store.Account, bool, error) {
				return ResolveActiveAccount(ctx, s.store, s.provider, switcher.ClaudeConfig)
			},
			AfterFetch: func(ctx context.Context, label string) {
				if _, err := autoSwap.MaybeSwap(ctx, label); err != nil {
					logger.Error("auto-swap loop error", "err", err)
				}
				notifier.Evaluate(ctx, label)
			},
			// Keep polling at the speed-up cadence while a swap is armed so its
			// grace deadline fires within ~one interval, not a full baseline
			// one (a swap can arm below SpeedupAtPct).
			SwapPending: autoSwap.HasPending,
		}
		go func() { _ = usage.Run(ctx) }()
	}

	return w.Run(ctx)
}
