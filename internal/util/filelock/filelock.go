// Package filelock provides a minimal Unix flock(2) wrapper for
// serializing access to multi-step operations like account switching.
//
// The implementation is darwin/linux only. aimonitor targets exactly
// those two platforms (see internal/secret/keyring_other.go for the
// matching keyring story), so a syscall.Flock-only build is fine.
package filelock

import (
	"fmt"
	"os"
	"syscall"
)

// FileLock is an acquired advisory file lock. Call Release to drop it.
// Lock acquisition is exclusive (LOCK_EX) and blocking — concurrent
// callers wait until the holder releases.
type FileLock struct {
	f *os.File
}

// Acquire opens path (creating it if needed) and takes an exclusive
// flock. Blocks until the lock is granted. The returned FileLock keeps
// the underlying *os.File open until Release; closing the file is what
// drops the lock from the kernel's perspective.
//
// path is created with mode 0600 so even on a multi-user box only the
// current user can race for the lock.
func Acquire(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("filelock: flock %s: %w", path, err)
	}
	return &FileLock{f: f}, nil
}

// TryAcquire is like Acquire but returns ErrLocked instead of blocking
// when another holder has the lock. Useful for "skip if busy" paths.
func TryAcquire(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("filelock: flock %s: %w", path, err)
	}
	return &FileLock{f: f}, nil
}

// Release drops the lock and closes the underlying file. Safe to call
// multiple times (subsequent calls are no-ops). Idempotent so callers
// can defer it without worrying about double-release on error paths.
func (l *FileLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}

// ErrLocked is returned by TryAcquire when the lock is held by another
// process. Distinct from a real I/O error so callers can branch on it.
var ErrLocked = fmt.Errorf("filelock: already held by another process")
