package claude

import (
	"bytes"
	"testing"
)

// cloneBytes is the defensive-copy helper that keeps the cred cache's
// interior bytes from escaping to a caller that might mutate or Zero them.
// A partial check already lives in provider_seam_test.go; these dedicated
// tests pin the full contract: nil passes through, an empty-but-non-nil
// slice stays non-nil, and any non-nil input is copied into a distinct
// backing array so mutations don't alias back to the source.

func TestCloneBytes_NilPassthrough(t *testing.T) {
	if got := cloneBytes(nil); got != nil {
		t.Errorf("cloneBytes(nil) = %v, want nil", got)
	}
}

func TestCloneBytes_EmptyNonNil(t *testing.T) {
	// An empty but non-nil input must not collapse to nil — callers rely on
	// nil vs. empty to distinguish "no slot" from "empty slot".
	got := cloneBytes([]byte{})
	if got == nil {
		t.Fatal("cloneBytes([]byte{}) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("cloneBytes([]byte{}) len = %d, want 0", len(got))
	}
}

func TestCloneBytes_DistinctBackingArray(t *testing.T) {
	b := []byte("secret-token")
	got := cloneBytes(b)

	if !bytes.Equal(got, b) {
		t.Errorf("cloneBytes(b) = %q, want equal contents %q", got, b)
	}

	// Mutating the clone must not touch the original: proves a distinct
	// backing array rather than an aliased slice header.
	got[0] = 'X'
	if b[0] == 'X' {
		t.Error("mutating clone changed original: cloneBytes returned an aliased slice")
	}
}
