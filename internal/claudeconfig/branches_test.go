package claudeconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests fill the read/write/merge/error branches in claudeconfig.go
// that the existing claudeconfig_test.go leaves uncovered: New, DefaultPath,
// Exists, and the writeRaw CreateTemp / readRaw read-error paths. All
// hermetic (temp dirs + t.Setenv).

func TestNew_BindsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := filepath.Join(home, ".claude.json")
	if s.path != want {
		t.Errorf("New bound to %q, want %q", s.path, want)
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(home, ".claude.json")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestExists(t *testing.T) {
	s, path := tempStore(t)

	if s.Exists() {
		t.Error("Exists should be false before file is created")
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Exists() {
		t.Error("Exists should be true after file is created")
	}
}

func TestWriteRaw_CreateTempError(t *testing.T) {
	// Parent directory does not exist, so os.CreateTemp inside writeRaw
	// fails before any rename — exercising the create-temp error branch.
	s := NewAt(filepath.Join(t.TempDir(), "no-such-dir", ".claude.json"))
	err := s.WriteOAuthAccount(context.Background(), OAuthAccount{EmailAddress: "x@example.com"})
	if err == nil {
		t.Fatal("WriteOAuthAccount into a missing parent dir: want error")
	}
	if !strings.Contains(err.Error(), "create temp") {
		t.Errorf("error %q should mention create temp", err.Error())
	}
}

func TestWriteOAuthAccount_ReadRawError(t *testing.T) {
	// WriteOAuthAccount first reads the existing file. Pointing the store at
	// a directory makes that read fail, exercising the error return before
	// any write happens.
	dir := t.TempDir()
	s := NewAt(dir)
	err := s.WriteOAuthAccount(context.Background(), OAuthAccount{EmailAddress: "x@example.com"})
	if err == nil {
		t.Fatal("WriteOAuthAccount when readRaw fails: want error")
	}
	if !strings.Contains(err.Error(), "read ") {
		t.Errorf("error %q should originate from the read step", err.Error())
	}
}

func TestReadRaw_ReadErrorNotRace(t *testing.T) {
	// Point the store at a directory: os.ReadFile returns a non-ErrNotExist
	// error, which readRaw must surface immediately (not retry as a race).
	dir := t.TempDir()
	s := NewAt(dir) // dir, not a file
	_, err := s.ReadOAuthAccount(context.Background())
	if err == nil {
		t.Fatal("ReadOAuthAccount on a directory: want read error")
	}
	if !strings.Contains(err.Error(), "read ") {
		t.Errorf("error %q should be a read error, not a parse-retry error", err.Error())
	}
}

func TestWriteRaw_RenameError(t *testing.T) {
	// writeRaw creates its temp file in filepath.Dir(s.path), so the parent
	// must exist for CreateTemp to succeed. We make s.path itself a
	// directory: CreateTemp + Write + Chmod + Close all succeed in the
	// parent, but the final os.Rename(tmp, s.path) fails because the
	// destination is an existing directory. This drives writeRaw past the
	// happy-path body to its rename-error branch. Called directly (same
	// package) because WriteOAuthAccount would short-circuit at readRaw for
	// a directory path.
	parent := t.TempDir()
	asDir := filepath.Join(parent, "claude-as-dir")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewAt(asDir)
	err := s.writeRaw(map[string]any{"oauthAccount": map[string]any{"emailAddress": "x@y"}})
	if err == nil {
		t.Fatal("writeRaw renaming over a directory: want error")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("error %q should mention rename", err.Error())
	}
}
