package filelock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Acquire grants the lock and Release frees it: a fresh Acquire on a path no
// one holds must succeed, and after Release the same path must be acquirable
// again (proving Release actually drops the kernel lock, not just closes a
// dangling handle).
func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if l == nil {
		t.Fatal("Acquire returned nil lock with no error")
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Released → a second Acquire must succeed immediately.
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// Release is idempotent: a nil receiver and repeated calls are safe no-ops,
// so callers can defer Release on error paths without a double-release crash.
func TestReleaseIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("second Release should be a no-op, got %v", err)
	}
	var nilLock *FileLock
	if err := nilLock.Release(); err != nil {
		t.Errorf("nil-receiver Release should be a no-op, got %v", err)
	}
}

// While the lock is held, TryAcquire on the same path must report ErrLocked
// rather than block — the deterministic anchor for "the second caller is
// serialized." flock(2) is per-open-file-descriptor, so two separate Acquire
// calls in this same process contend exactly as two processes would.
func TestTryAcquireWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	held, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	if _, err := TryAcquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("TryAcquire while held = %v, want ErrLocked", err)
	}
}

// A blocking Acquire serializes: a second waiter must not proceed until the
// first holder releases, then it acquires promptly. The goroutine + timeout
// keeps the test from hanging CI if serialization is broken (the waiter would
// return early and the "still blocked" assertion would catch it instead).
func TestBlockingAcquireSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	acquired := make(chan *FileLock, 1)
	go func() {
		// This must block until `first` is released below.
		l, aerr := Acquire(path)
		if aerr != nil {
			acquired <- nil
			return
		}
		acquired <- l
	}()

	// The waiter must still be blocked while `first` holds the lock.
	select {
	case <-acquired:
		t.Fatal("second Acquire returned while the lock was still held")
	case <-time.After(150 * time.Millisecond):
		// Expected: still waiting.
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Now the waiter must obtain the lock promptly.
	select {
	case l := <-acquired:
		if l == nil {
			t.Fatal("blocked Acquire failed after the holder released")
		}
		if err := l.Release(); err != nil {
			t.Errorf("Release of unblocked lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not unblock within 2s after Release")
	}
}
