package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Offset is a per-file resume point for the JSONL watcher. ByteOffset is
// the position in the file just past the last fully-processed JSON line.
// MtimeNs is the file's mtime (nanoseconds since epoch) at the time the
// offset was recorded — used to detect file truncation / replacement.
type Offset struct {
	Path        string
	ByteOffset  int64
	MtimeNs     int64
}

// GetOffset returns the recorded Offset for path, or ErrOffsetNotFound if
// the file has never been seen.
func (s *Store) GetOffset(ctx context.Context, path string) (Offset, error) {
	var o Offset
	err := s.DB.QueryRowContext(ctx,
		`SELECT path, byte_offset, mtime_ns FROM jsonl_offsets WHERE path = ?`, path,
	).Scan(&o.Path, &o.ByteOffset, &o.MtimeNs)
	if errors.Is(err, sql.ErrNoRows) {
		return Offset{}, ErrOffsetNotFound
	}
	if err != nil {
		return Offset{}, fmt.Errorf("read offset for %q: %w", path, err)
	}
	return o, nil
}

// PutOffset writes (or updates) the offset for o.Path. Idempotent.
func (s *Store) PutOffset(ctx context.Context, o Offset) error {
	if o.Path == "" {
		return errors.New("PutOffset: empty path")
	}
	if o.ByteOffset < 0 {
		return fmt.Errorf("PutOffset: negative offset %d", o.ByteOffset)
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO jsonl_offsets (path, byte_offset, mtime_ns) VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET byte_offset = excluded.byte_offset, mtime_ns = excluded.mtime_ns`,
		o.Path, o.ByteOffset, o.MtimeNs,
	)
	if err != nil {
		return fmt.Errorf("write offset for %q: %w", o.Path, err)
	}
	return nil
}

// DeleteOffset removes any offset recorded for path. Returns nil even if
// the file was never tracked — callers don't care.
func (s *Store) DeleteOffset(ctx context.Context, path string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM jsonl_offsets WHERE path = ?`, path); err != nil {
		return fmt.Errorf("delete offset for %q: %w", path, err)
	}
	return nil
}

// ErrOffsetNotFound is returned by GetOffset when a path has no recorded
// resume point.
var ErrOffsetNotFound = errors.New("store: no offset recorded for path")
