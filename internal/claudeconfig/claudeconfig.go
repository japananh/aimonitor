// Package claudeconfig reads and writes ~/.claude.json — the file Claude
// Code uses to record which OAuth account is active (the `oauthAccount`
// object) alongside many unrelated settings.
//
// Why aimonitor touches this file at all: the macOS Keychain entry
// "Claude Code-credentials" holds only the *tokens* (accessToken /
// refreshToken). The *identity* of the active account — email,
// organization — lives in ~/.claude.json's oauthAccount. Swapping the
// keychain tokens without updating oauthAccount leaves Claude Code
// believing it is still signed in as the previous account: wrong
// account in `/status`, project-history scoped to the wrong identity,
// and (on some Claude Code versions) a re-auth prompt on the mismatch.
//
// Both halves must move together. This package owns the claude.json
// half; internal/secret owns the keychain half.
package claudeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// readRetryAttempts / readRetryDelay absorb the race where Claude Code
// rewrites ~/.claude.json non-atomically (the contents land over 2-3
// fsyncs). A read landing mid-write sees truncated JSON; the write
// finishes within a few hundred ms, so a small bounded retry resolves
// it without surfacing a spurious parse error. Mirrors claude-bar's
// claude_config_store.go.
const (
	readRetryAttempts = 3
	readRetryDelay    = 100 * time.Millisecond
)

// OAuthAccount is the identity object Claude Code stores under the
// "oauthAccount" key in ~/.claude.json. Field names (and their JSON
// tags) must match Claude Code's schema exactly — a wrong key name
// silently breaks Claude Code's account recognition.
//
// AccountUUID is intentionally NOT written back on a switch: Claude Code
// repopulates it on next launch from the account it authenticates, and
// carrying a stale value across a switch would pair the new account's
// email with the old account's UUID. Mirrors claude-bar, which patches
// only emailAddress / organizationName / organizationUuid.
type OAuthAccount struct {
	EmailAddress     string `json:"emailAddress"`
	OrganizationName string `json:"organizationName,omitempty"`
	OrganizationUUID string `json:"organizationUuid,omitempty"`
}

// Store is the on-disk adapter for ~/.claude.json.
type Store struct {
	path string
}

// New binds to $HOME/.claude.json.
func New() (*Store, error) {
	p, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return &Store{path: p}, nil
}

// NewAt binds to an explicit path (tests).
func NewAt(path string) *Store { return &Store{path: path} }

// DefaultPath returns $HOME/.claude.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// Exists reports whether the file is on disk.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// ReadOAuthAccount returns the parsed oauthAccount object, or (nil, nil)
// when the file is absent or has no oauthAccount key. A malformed file
// is retried briefly (the non-atomic-write race) before erroring.
func (s *Store) ReadOAuthAccount(ctx context.Context) (*OAuthAccount, error) {
	raw, err := s.readRaw(ctx)
	if err != nil {
		return nil, err
	}
	oa, ok := raw["oauthAccount"]
	if !ok {
		return nil, nil
	}
	// Re-marshal the sub-object so we decode only the fields we care
	// about, ignoring everything else Claude Code stores there.
	b, err := json.Marshal(oa)
	if err != nil {
		return nil, fmt.Errorf("re-marshal oauthAccount: %w", err)
	}
	var acct OAuthAccount
	if err := json.Unmarshal(b, &acct); err != nil {
		return nil, fmt.Errorf("decode oauthAccount: %w", err)
	}
	return &acct, nil
}

// WriteOAuthAccount replaces the oauthAccount object in ~/.claude.json
// with acct, preserving every other field in the file. The write is
// atomic (temp file + rename) with 0600 perms.
//
// When the file does not exist yet it is created with just the
// oauthAccount key. (Claude Code fills in the rest on next launch.)
func (s *Store) WriteOAuthAccount(ctx context.Context, acct OAuthAccount) error {
	raw, err := s.readRaw(ctx)
	if err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["oauthAccount"] = acct
	return s.writeRaw(raw)
}

// readRaw loads the whole file as a generic map, with bounded retry on
// JSON parse errors. Returns an empty map (not nil) when the file is
// absent so callers can treat "no file" and "empty file" uniformly.
func (s *Store) readRaw(ctx context.Context) (map[string]any, error) {
	var lastErr error
	for range readRetryAttempts {
		data, err := os.ReadFile(s.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return map[string]any{}, nil
			}
			// A non-parse read error (permission, IO) is not the race —
			// surface it immediately.
			return nil, fmt.Errorf("read %s: %w", s.path, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			// Parse failure is the non-atomic-write signature: retry.
			lastErr = err
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(readRetryDelay):
			}
			continue
		}
		if raw == nil {
			raw = map[string]any{}
		}
		return raw, nil
	}
	return nil, fmt.Errorf("parse %s after %d attempts: %w", s.path, readRetryAttempts, lastErr)
}

// writeRaw marshals the map and atomically replaces the file. The temp
// file is created in the same directory so the rename is atomic on the
// same filesystem.
func (s *Store) writeRaw(raw map[string]any) error {
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".claude-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	return nil
}
