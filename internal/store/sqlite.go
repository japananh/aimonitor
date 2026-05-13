// Package store wraps the SQLite database that aimonitor uses for everything
// that is NOT a secret: account references, per-file JSONL resume offsets,
// usage samples, audit log, settings.
//
// Secrets (OAuth blobs) live in the OS keyring; the only field in this DB
// that points at them is accounts.keyring_ref.
//
// The driver is modernc.org/sqlite — a pure-Go translation of SQLite that
// requires no CGO. This keeps cross-compilation simple and CI fast.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	// Registers the "sqlite" driver with database/sql. Pure-Go, no CGO.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DefaultPath returns the platform-appropriate location for aimonitor.db.
//   - macOS:  ~/Library/Application Support/aimonitor/aimonitor.db
//   - Linux:  $XDG_DATA_HOME/aimonitor/aimonitor.db (default ~/.local/share/aimonitor)
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Application Support", "aimonitor")
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			dir = filepath.Join(xdg, "aimonitor")
		} else {
			dir = filepath.Join(home, ".local", "share", "aimonitor")
		}
	}
	return filepath.Join(dir, "aimonitor.db"), nil
}

// Store wraps a *sql.DB with the migrations applied.
type Store struct {
	DB   *sql.DB
	Path string
}

// Open creates the parent directory if necessary, opens the DB with WAL
// enabled and 0600 permissions on the file, and runs every pending migration.
//
// A path of ":memory:" yields an in-memory DB, used by tests.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store.Open: empty path")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}
	}

	// _journal_mode=WAL and _busy_timeout=5000 are passed via DSN params.
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	// Single writer is enough for aimonitor's workload and avoids "database
	// is locked" surprises with WAL.
	db.SetMaxOpenConns(1)

	if path != ":memory:" {
		// Tighten the on-disk permissions.
		_ = os.Chmod(path, 0o600)
	}

	s := &Store{DB: db, Path: path}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying DB handle.
func (s *Store) Close() error { return s.DB.Close() }

// migrate applies every embedded migration whose version is higher than the
// current schema_version. It is idempotent: re-running Open against the same
// DB is a no-op.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := loadMigrations()
	if err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, m := range files {
		if applied[m.version] {
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.body); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`,
			m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.version, err)
		}
	}
	return nil
}

type migration struct {
	version string
	body    string
}

// loadMigrations reads every .sql file under migrations/ and returns them
// sorted ascending by filename (which is also the version).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, filepath.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{
			version: strings.TrimSuffix(e.Name(), ".sql"),
			body:    string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
