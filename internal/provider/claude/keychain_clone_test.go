package claude

import (
	"bytes"
	"testing"
)

// Direct unit test for cloneBytes: the nil passthrough and the
// distinct-backing-array copy semantics its callers rely on (the cache's
// interior bytes must never escape to a caller that could mutate them).
// Names are suffixed _Clone to avoid colliding with the cloneBytes(nil)
// spot-check already living in provider_seam_test.go.

func TestCloneBytes_NilClone(t *testing.T) {
	if got := cloneBytes(nil); got != nil {
		t.Errorf("cloneBytes(nil) = %v, want nil", got)
	}
}

func TestCloneBytes_DistinctBackingArrayClone(t *testing.T) {
	b := []byte("secret-blob")
	got := cloneBytes(b)

	if !bytes.Equal(got, b) {
		t.Fatalf("cloneBytes(%q) = %q, want equal contents", b, got)
	}

	// Mutating the clone must not touch the source.
	got[0] ^= 0xff
	if bytes.Equal(got, b) {
		t.Error("cloneBytes returned a slice sharing the source's backing array")
	}
	if string(b) != "secret-blob" {
		t.Errorf("source mutated after clone: got %q, want %q", b, "secret-blob")
	}
}
