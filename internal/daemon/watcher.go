// Package daemon hosts aimonitor's long-running background components: the
// Unix-socket JSON-RPC server, the JSONL filesystem watcher, the auto-switch
// engine. In v1.0.0-beta the daemon is co-located in the `aimonitor`
// binary (subcommand: `aimonitor daemon run`) rather than shipping as a
// separate executable.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// SampleEvent pairs a parsed JSONL sample with the file it came from.
// The watcher's emit callback receives these — the daemon's higher
// layers route them to a per-account aggregator.
type SampleEvent struct {
	Path   string
	Sample claude.Sample
}

// WatcherConfig wires the JSONL watcher to its dependencies.
type WatcherConfig struct {
	// Root is the directory tree to monitor. In production this is
	// ~/.claude/projects. Each direct subdirectory is watched.
	Root string

	// Store carries resumable byte-offset state per JSONL file. The
	// watcher reads the offset at scan time and writes a fresh offset
	// after every successful line parse so a daemon restart resumes
	// without re-emitting samples.
	Store *store.Store

	// OnSample fires once per usage-bearing JSONL line.
	OnSample func(SampleEvent)

	// OnError is called for non-fatal errors (e.g. a transient file
	// permission glitch). Optional; if nil, errors are dropped silently.
	OnError func(error)
}

// Watcher tails JSONL transcripts under Root and emits SampleEvents.
//
// Lifecycle:
//   - NewWatcher constructs but does not start the fsnotify backend.
//   - Run does an initial walk + parse, then blocks watching fsnotify
//     events until ctx is cancelled.
type Watcher struct {
	cfg     WatcherConfig
	fsn     *fsnotify.Watcher
	dirsSet map[string]struct{}
}

// NewWatcher validates cfg and constructs a watcher (no I/O yet).
func NewWatcher(cfg WatcherConfig) (*Watcher, error) {
	if cfg.Root == "" {
		return nil, errors.New("WatcherConfig: Root is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("WatcherConfig: Store is required")
	}
	return &Watcher{cfg: cfg, dirsSet: map[string]struct{}{}}, nil
}

// Run blocks until ctx is cancelled (returns ctx.Err() in that case),
// or until the fsnotify backend errors fatally.
//
// On startup it walks Root, watches every directory in the tree, and
// re-processes every JSONL file from its persisted byte offset (so a
// restart catches up on writes that happened while the daemon was off).
func (w *Watcher) Run(ctx context.Context) error {
	fsn, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	w.fsn = fsn
	defer func() { _ = fsn.Close() }()

	if err := w.bootstrap(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-fsn.Events:
			if !ok {
				return errors.New("fsnotify events channel closed unexpectedly")
			}
			w.handleEvent(ctx, ev)
		case err, ok := <-fsn.Errors:
			if !ok {
				return errors.New("fsnotify errors channel closed unexpectedly")
			}
			w.reportError(fmt.Errorf("fsnotify: %w", err))
		}
	}
}

// bootstrap walks Root, adds a watch per directory, and processes every
// existing JSONL file from its stored offset. Run() calls this once.
func (w *Watcher) bootstrap(ctx context.Context) error {
	info, err := os.Stat(w.cfg.Root)
	if errors.Is(err, os.ErrNotExist) {
		// Root not yet present. On a fresh machine this happens two ways:
		//   1. ~/.claude exists but ~/.claude/projects doesn't yet — watch
		//      the parent so the projects dir is picked up when it appears.
		//   2. ~/.claude itself is absent (Claude Code never run here) —
		//      create the tree so there's something to watch immediately;
		//      session dirs/files arrive later via CREATE events.
		// Crucially, the watcher only feeds session-usage samples; it must
		// never take the daemon down, since OAuth polling and switching (the
		// daemon's primary job) don't depend on it. A clean install used to
		// crash-loop here, surfacing "daemon not running" forever.
		parent := filepath.Dir(w.cfg.Root)
		if pinfo, perr := os.Stat(parent); perr == nil && pinfo.IsDir() {
			return w.addDir(parent)
		}
		if mkErr := os.MkdirAll(w.cfg.Root, 0o700); mkErr != nil {
			// Even creation failed (read-only home, odd perms). Degrade to a
			// no-op watch rather than killing the daemon; session-usage
			// tracking resumes after the next restart once the dir exists.
			w.reportError(fmt.Errorf("watcher: root %q absent and uncreatable (%w); session-usage tracking off until restart", w.cfg.Root, mkErr))
			return nil
		}
		return w.addDir(w.cfg.Root)
	}
	if err != nil {
		return fmt.Errorf("watcher: stat root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("watcher: root %q is not a directory", w.cfg.Root)
	}

	return filepath.WalkDir(w.cfg.Root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			w.reportError(fmt.Errorf("walk %q: %w", path, walkErr))
			// Skip but keep walking siblings.
			return nil
		}
		if d.IsDir() {
			return w.addDir(path)
		}
		if strings.HasSuffix(d.Name(), ".jsonl") {
			w.processFile(ctx, path)
		}
		return nil
	})
}

// addDir registers an fsnotify watch on path (idempotent).
func (w *Watcher) addDir(path string) error {
	if _, watched := w.dirsSet[path]; watched {
		return nil
	}
	if err := w.fsn.Add(path); err != nil {
		return fmt.Errorf("watch %q: %w", path, err)
	}
	w.dirsSet[path] = struct{}{}
	return nil
}

// handleEvent routes a single fsnotify event.
func (w *Watcher) handleEvent(ctx context.Context, ev fsnotify.Event) {
	// CREATE on a directory: extend the watch tree.
	if ev.Op.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if err := w.addDir(ev.Name); err != nil {
				w.reportError(err)
			}
			return
		}
	}
	// WRITE or CREATE on a *.jsonl file: re-process from stored offset.
	if (ev.Op.Has(fsnotify.Write) || ev.Op.Has(fsnotify.Create)) &&
		strings.HasSuffix(ev.Name, ".jsonl") {
		w.processFile(ctx, ev.Name)
		return
	}
	// REMOVE on a *.jsonl file: drop its offset row so disk usage stays bounded.
	if ev.Op.Has(fsnotify.Remove) && strings.HasSuffix(ev.Name, ".jsonl") {
		if err := w.cfg.Store.DeleteOffset(ctx, ev.Name); err != nil {
			w.reportError(fmt.Errorf("clear offset for removed %q: %w", ev.Name, err))
		}
	}
}

// processFile opens path, seeks to the stored offset, parses new lines,
// fires OnSample callbacks, and writes back the new offset.
//
// Truncation handling: if the file's current size is smaller than the
// stored offset, we treat the file as rotated and start over from 0.
func (w *Watcher) processFile(ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		w.reportError(fmt.Errorf("open %q: %w", path, err))
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		w.reportError(fmt.Errorf("stat %q: %w", path, err))
		return
	}

	prev, err := w.cfg.Store.GetOffset(ctx, path)
	startAt := int64(0)
	if err == nil {
		startAt = prev.ByteOffset
		// Truncation / rotation: file shrunk below where we last read.
		if info.Size() < startAt {
			startAt = 0
		}
	} else if !errors.Is(err, store.ErrOffsetNotFound) {
		w.reportError(fmt.Errorf("read offset for %q: %w", path, err))
		return
	}

	if startAt > 0 {
		if _, err := f.Seek(startAt, io.SeekStart); err != nil {
			w.reportError(fmt.Errorf("seek %q to %d: %w", path, startAt, err))
			return
		}
	}

	finalOffset := startAt
	consumed, parseErr := claude.ParseReader(f,
		func(s claude.Sample) {
			if w.cfg.OnSample != nil {
				w.cfg.OnSample(SampleEvent{Path: path, Sample: s})
			}
		},
		func(p int64) {
			finalOffset = startAt + p
		},
	)
	if parseErr != nil {
		w.reportError(fmt.Errorf("parse %q: %w", path, parseErr))
		// Even on error we persist whatever we managed to consume so we
		// don't reprocess the same bytes next time.
	}
	if consumed == 0 {
		return
	}

	if err := w.cfg.Store.PutOffset(ctx, store.Offset{
		Path:       path,
		ByteOffset: finalOffset,
		MtimeNs:    info.ModTime().UnixNano(),
	}); err != nil {
		w.reportError(fmt.Errorf("save offset for %q: %w", path, err))
	}
}

func (w *Watcher) reportError(err error) {
	if w.cfg.OnError != nil {
		w.cfg.OnError(err)
	}
}
