package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// storePathOverride lets tests pass an in-memory or temp-dir DB without
// touching the user's real ~/Library/Application Support tree. Set via
// AIMONITOR_STORE_PATH or by overriding storeOpener in tests.
var storePathOverride = func() string {
	return os.Getenv("AIMONITOR_STORE_PATH")
}

// openStore returns a Store at AIMONITOR_STORE_PATH (if set) or the
// platform default. The caller MUST defer Close().
func openStore() (*store.Store, error) {
	path := storePathOverride()
	if path == "" {
		var err error
		path, err = store.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("resolve store path: %w", err)
		}
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return s, nil
}

// claudeProvider fetches the registered Claude provider, returning a
// useful error when registration somehow didn't happen (defense in depth
// — the package's init() should always register).
func claudeProvider() (provider.Provider, error) {
	p, err := provider.Lookup(claude.Name)
	if err != nil {
		return nil, fmt.Errorf("claude provider unavailable: %w", err)
	}
	return p, nil
}

// withRuntime is a tiny helper that opens the store, runs fn with it,
// and closes the store even on error. Subcommands use it to keep their
// bodies focused on business logic.
func withRuntime(ctx context.Context, fn func(ctx context.Context, s *store.Store, p provider.Provider) error) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	p, err := claudeProvider()
	if err != nil {
		return err
	}
	return fn(ctx, s, p)
}

// ErrNoActiveAccount is returned by activeAccount() when the
// Claude Code-credentials slot is empty.
var ErrNoActiveAccount = errors.New("no Claude Code credential is currently active")