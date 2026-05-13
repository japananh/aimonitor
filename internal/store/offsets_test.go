package store

import (
	"context"
	"errors"
	"testing"
)

func TestOffsets_RoundTrip(t *testing.T) {
	s := openTestStore(t)

	ctx := context.Background()
	path := "/Users/nana/.claude/projects/encoded/session-1.jsonl"

	// Initial: ErrOffsetNotFound.
	if _, err := s.GetOffset(ctx, path); !errors.Is(err, ErrOffsetNotFound) {
		t.Fatalf("GetOffset before Put: want ErrOffsetNotFound, got %v", err)
	}

	// Put then Get.
	o := Offset{Path: path, ByteOffset: 4096, MtimeNs: 1_700_000_000_000_000_000}
	if err := s.PutOffset(ctx, o); err != nil {
		t.Fatalf("PutOffset: %v", err)
	}
	got, err := s.GetOffset(ctx, path)
	if err != nil {
		t.Fatalf("GetOffset: %v", err)
	}
	if got != o {
		t.Fatalf("GetOffset returned %+v, want %+v", got, o)
	}

	// Upsert: overwrite with a later position.
	o2 := Offset{Path: path, ByteOffset: 8192, MtimeNs: 1_700_000_100_000_000_000}
	if err := s.PutOffset(ctx, o2); err != nil {
		t.Fatalf("PutOffset upsert: %v", err)
	}
	got, err = s.GetOffset(ctx, path)
	if err != nil {
		t.Fatalf("GetOffset post-upsert: %v", err)
	}
	if got != o2 {
		t.Fatalf("upsert returned %+v, want %+v", got, o2)
	}

	// Delete then Get returns ErrOffsetNotFound.
	if err := s.DeleteOffset(ctx, path); err != nil {
		t.Fatalf("DeleteOffset: %v", err)
	}
	if _, err := s.GetOffset(ctx, path); !errors.Is(err, ErrOffsetNotFound) {
		t.Fatalf("GetOffset after Delete: want ErrOffsetNotFound, got %v", err)
	}

	// Delete of unknown path is a no-op.
	if err := s.DeleteOffset(ctx, "/nope"); err != nil {
		t.Fatalf("DeleteOffset of unknown path should be nil; got %v", err)
	}
}

func TestPutOffset_Validation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.PutOffset(ctx, Offset{Path: "", ByteOffset: 0}); err == nil {
		t.Error("empty path should error")
	}
	if err := s.PutOffset(ctx, Offset{Path: "/foo", ByteOffset: -1}); err == nil {
		t.Error("negative offset should error")
	}
}

// openTestStore returns an in-memory Store with migrations applied. The
// migrate runs on every Open so test isolation is automatic.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
