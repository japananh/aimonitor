package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenInMemoryAppliesSchema(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	tables := []string{
		"accounts", "jsonl_offsets", "usage_samples",
		"probe_results", "switch_audit", "settings",
		"schema_migrations",
	}
	for _, table := range tables {
		var n int
		row := s.DB.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("expected table %q to exist exactly once, found %d", table, n)
		}
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aimonitor.db")

	// First open applies migrations.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second open should be a no-op for migrations.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	var count int
	if err := s2.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected at least one migration applied; got 0")
	}
}

func TestDefaultPathNonEmpty(t *testing.T) {
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got == "" {
		t.Fatalf("DefaultPath returned empty string")
	}
}
