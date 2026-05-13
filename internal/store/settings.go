package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSettingNotFound is returned by GetSetting when the key has never
// been written.
var ErrSettingNotFound = errors.New("store: setting not found")

// Setting keys used by aimonitor. Centralised so producers and consumers
// (daemon publisher + widget reader, CLI + tests) can't drift apart.
const (
	// SettingsKeyDaemonStatus stores a JSON snapshot of the live daemon
	// state. Written by the daemon every few seconds; read by the menu
	// bar widget. See daemon.Status for the schema.
	SettingsKeyDaemonStatus = "daemon_status"

	// SettingsKeyBudget stores the observed-max session budget across
	// daemon restarts. Without persistence, every restart resets the
	// auto-switch reference window.
	SettingsKeyBudget = "observed_session_budget"
)

// GetSetting returns the value for key, or ErrSettingNotFound. Empty
// strings count as "set" — callers that want a non-empty value must
// check themselves.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrSettingNotFound
		}
		return "", fmt.Errorf("read setting %q: %w", key, err)
	}
	return v, nil
}

// PutSetting upserts (key, value). Empty values are allowed.
func (s *Store) PutSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("PutSetting: key required")
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("write setting %q: %w", key, err)
	}
	return nil
}
