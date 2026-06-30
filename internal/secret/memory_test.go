package secret

import (
	"bytes"
	"errors"
	"testing"
)

// These tests cover MemoryKeyring (memory.go), the in-memory fake used by
// other packages' tests. They are hermetic — no real OS keychain is touched,
// so no CI-skip and no t.Parallel (the type is shared but each test uses its
// own instance).

func TestMemoryKeyring_GetSetDeleteRoundTrip(t *testing.T) {
	k := NewMemoryKeyring()
	svc, acct := "svc", "acct"
	want := []byte("token-bytes")

	if err := k.Set(svc, acct, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := k.Get(svc, acct)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}

	if err := k.Delete(svc, acct); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestMemoryKeyring_GetMissing(t *testing.T) {
	k := NewMemoryKeyring()
	if _, err := k.Get("nope", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}
}

func TestMemoryKeyring_GetAfterDelete(t *testing.T) {
	k := NewMemoryKeyring()
	if err := k.Set("s", "a", []byte("x")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := k.Delete("s", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get("s", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: want ErrNotFound, got %v", err)
	}
}

func TestMemoryKeyring_DeleteMissing(t *testing.T) {
	k := NewMemoryKeyring()
	if err := k.Delete("s", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing: want ErrNotFound, got %v", err)
	}
}

func TestMemoryKeyring_Overwrite(t *testing.T) {
	k := NewMemoryKeyring()
	if err := k.Set("s", "a", []byte("first")); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := k.Set("s", "a", []byte("second")); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := k.Get("s", "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("after overwrite Get = %q, want %q", got, "second")
	}
}

func TestMemoryKeyring_NamespacedKeys(t *testing.T) {
	// Distinct (service, account) pairs must not collide even when the
	// concatenation might otherwise be ambiguous.
	k := NewMemoryKeyring()
	if err := k.Set("ab", "c", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := k.Set("a", "bc", []byte("2")); err != nil {
		t.Fatal(err)
	}
	g1, _ := k.Get("ab", "c")
	g2, _ := k.Get("a", "bc")
	if string(g1) != "1" || string(g2) != "2" {
		t.Fatalf("key collision: ab/c=%q a/bc=%q", g1, g2)
	}
}

func TestMemoryKeyring_SetCopiesInput(t *testing.T) {
	// Set must store a copy: mutating the caller's slice afterward must not
	// change what's stored.
	k := NewMemoryKeyring()
	in := []byte("orig")
	if err := k.Set("s", "a", in); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X' // mutate caller's slice after handing off
	got, _ := k.Get("s", "a")
	if string(got) != "orig" {
		t.Fatalf("Set did not copy input: stored %q after caller mutation", got)
	}
}

func TestMemoryKeyring_GetReturnsCopy(t *testing.T) {
	// Get must return a copy: mutating the returned slice must not change
	// what's stored, nor a subsequent Get.
	k := NewMemoryKeyring()
	if err := k.Set("s", "a", []byte("orig")); err != nil {
		t.Fatal(err)
	}
	got1, _ := k.Get("s", "a")
	got1[0] = 'X' // mutate the returned copy
	got2, _ := k.Get("s", "a")
	if string(got2) != "orig" {
		t.Fatalf("Get returned interior slice: second Get = %q after mutation", got2)
	}
}
