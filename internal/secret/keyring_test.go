package secret

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

// TestRoundTrip exercises Get/Set/Delete against the real OS keyring.
//
// Skipped in two situations:
//   - on platforms without an implementation (anything but darwin/linux).
//   - when running under CI: GitHub-hosted runners ship without an
//     unlocked login keychain (macOS) or a running Secret Service provider
//     (ubuntu), so the API would error before we could exercise it.
//     Local runs on a developer machine cover this path.
func TestRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("keyring not implemented on %s", runtime.GOOS)
	}
	if os.Getenv("CI") != "" {
		t.Skip("CI runners lack an unlocked keyring; run locally")
	}
	if testing.Short() {
		t.Skip("skipping keyring round-trip in -short mode")
	}

	k, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	service := "aimonitor-test-" + uuid.NewString()
	account := "ci-roundtrip"
	want := []byte("aimonitor-secret-bytes")

	t.Cleanup(func() { _ = k.Delete(service, account) })

	if err := k.Set(service, account, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := k.Get(service, account)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get returned %q, want %q", got, want)
	}

	// Set overwrites cleanly.
	want2 := []byte("aimonitor-rotated")
	if err := k.Set(service, account, want2); err != nil {
		t.Fatalf("Set (overwrite): %v", err)
	}
	got2, err := k.Get(service, account)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(got2, want2) {
		t.Fatalf("Get after overwrite returned %q, want %q", got2, want2)
	}

	if err := k.Delete(service, account); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := k.Get(service, account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: want ErrNotFound, got %v", err)
	}

	if err := k.Delete(service, account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete after Delete: want ErrNotFound, got %v", err)
	}
}
