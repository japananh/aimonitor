package claude

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
)

// fakeKeyring is an in-memory secret.Keyring for tests. Real keychain is
// not used: we want the test suite to run in CI without a real keyring
// service (where the round-trip test is skipped).
type fakeKeyring struct {
	store map[string][]byte
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{store: map[string][]byte{}} }

func (f *fakeKeyring) key(s, a string) string { return s + "\x00" + a }

func (f *fakeKeyring) Get(s, a string) ([]byte, error) {
	v, ok := f.store[f.key(s, a)]
	if !ok {
		return nil, secret.ErrNotFound
	}
	// Return a defensive copy so callers can't tamper with stored bytes.
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}
func (f *fakeKeyring) Set(s, a string, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	f.store[f.key(s, a)] = cp
	return nil
}
func (f *fakeKeyring) Delete(s, a string) error {
	if _, ok := f.store[f.key(s, a)]; !ok {
		return secret.ErrNotFound
	}
	delete(f.store, f.key(s, a))
	return nil
}

func newTestKeychainOps(t *testing.T) (*keychainOps, *fakeKeyring) {
	t.Helper()
	fr := newFakeKeyring()
	return &keychainOps{ring: fr, user: "tester"}, fr
}

func TestKeychainOps_RoundTrip(t *testing.T) {
	ops, fr := newTestKeychainOps(t)
	ctx := context.Background()

	// readActive on empty slot returns empty credential (not error).
	got, err := ops.readActive(ctx)
	if err != nil {
		t.Fatalf("readActive empty: %v", err)
	}
	if len(got.Bytes) != 0 {
		t.Fatalf("readActive empty: got %d bytes, want 0", len(got.Bytes))
	}

	// writeActive then readActive.
	want := []byte(`{"claudeAiOauth":{"accessToken":"sk-test"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: want}); err != nil {
		t.Fatalf("writeActive: %v", err)
	}
	got, err = ops.readActive(ctx)
	if err != nil {
		t.Fatalf("readActive: %v", err)
	}
	if string(got.Bytes) != string(want) {
		t.Fatalf("got %q, want %q", got.Bytes, want)
	}

	// writeActive rejects empty bytes.
	if err := ops.writeActive(ctx, provider.Credential{}); err == nil {
		t.Errorf("writeActive empty: want error, got nil")
	}

	// Stash round trip.
	stashBytes := []byte(`{"claudeAiOauth":{"accessToken":"sk-stash"}}`)
	if err := ops.writeStash(ctx, "uuid-1", provider.Credential{Bytes: stashBytes}); err != nil {
		t.Fatalf("writeStash: %v", err)
	}
	stash, err := ops.readStash(ctx, "uuid-1")
	if err != nil {
		t.Fatalf("readStash: %v", err)
	}
	if string(stash.Bytes) != string(stashBytes) {
		t.Fatalf("readStash got %q, want %q", stash.Bytes, stashBytes)
	}

	// Confirm stash + active are namespaced separately.
	if len(fr.store) != 2 {
		t.Fatalf("expected 2 keyring entries, got %d", len(fr.store))
	}

	// deleteStash + idempotent re-delete.
	if err := ops.deleteStash(ctx, "uuid-1"); err != nil {
		t.Fatalf("deleteStash: %v", err)
	}
	if err := ops.deleteStash(ctx, "uuid-1"); err != nil {
		t.Fatalf("deleteStash idempotent: %v", err)
	}

	// readStash after delete returns ErrNotFound.
	if _, err := ops.readStash(ctx, "uuid-1"); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("readStash after delete: want ErrNotFound, got %v", err)
	}
}

func TestKeychainOps_Validation(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	if _, err := ops.readStash(ctx, ""); err == nil {
		t.Error("readStash empty id: want error, got nil")
	}
	if err := ops.writeStash(ctx, "", provider.Credential{Bytes: []byte("x")}); err == nil {
		t.Error("writeStash empty id: want error, got nil")
	}
	if err := ops.deleteStash(ctx, ""); err == nil {
		t.Error("deleteStash empty id: want error, got nil")
	}
}

func TestRunOnboarding_HappyPath_AdoptOverNonEmptySlot(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	// Existing "personal" account is active.
	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-personal"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed writeActive: %v", err)
	}

	// Simulated claude login writes the "work" blob.
	work := []byte(`{"claudeAiOauth":{"accessToken":"sk-work"}}`)
	deps := onboardingDeps{
		keys: ops,
		login: func(ctx context.Context) error {
			return ops.writeActive(ctx, provider.Credential{Bytes: work})
		},
	}

	got, err := runOnboarding(ctx, deps)
	if err != nil {
		t.Fatalf("runOnboarding: %v", err)
	}
	if string(got.Bytes) != string(work) {
		t.Errorf("returned credential = %q, want %q", got.Bytes, work)
	}

	// Active slot must have been restored to "personal".
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("active after onboarding = %q, want %q (restore failed)", active.Bytes, personal)
	}
}

func TestRunOnboarding_HappyPath_FirstClaudeLogin(t *testing.T) {
	// First-time user has no Claude Code slot yet; onboarding leaves the
	// new blob as the active credential (nothing to restore).
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	work := []byte(`{"claudeAiOauth":{"accessToken":"sk-only"}}`)
	deps := onboardingDeps{
		keys: ops,
		login: func(ctx context.Context) error {
			return ops.writeActive(ctx, provider.Credential{Bytes: work})
		},
	}

	got, err := runOnboarding(ctx, deps)
	if err != nil {
		t.Fatalf("runOnboarding: %v", err)
	}
	if string(got.Bytes) != string(work) {
		t.Errorf("returned credential = %q, want %q", got.Bytes, work)
	}

	// Active slot has the new blob (no previous to restore).
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(work) {
		t.Errorf("active after first onboarding = %q, want %q", active.Bytes, work)
	}
}

func TestRunOnboarding_Cancelled_SlotUnchanged(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-personal"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deps := onboardingDeps{
		keys: ops,
		// Simulate user pressing Ctrl-C: claude returns non-zero but
		// hasn't touched the slot.
		login: func(ctx context.Context) error { return errors.New("interrupted") },
	}

	_, err := runOnboarding(ctx, deps)
	if err == nil {
		t.Fatal("expected error for cancelled onboarding, got nil")
	}
	if !strings.Contains(err.Error(), "OAuth not completed") {
		t.Errorf("expected 'OAuth not completed' in error, got %q", err.Error())
	}

	// Personal is still active.
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("personal should still be active; got %q", active.Bytes)
	}
}

func TestRunOnboarding_NonzeroExitButSlotChanged(t *testing.T) {
	// `claude login` might exit non-zero even on success (network blip
	// during a status print, etc.). The byte-diff check is authoritative.
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-p"}}`)
	work := []byte(`{"claudeAiOauth":{"accessToken":"sk-w"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deps := onboardingDeps{
		keys: ops,
		login: func(ctx context.Context) error {
			_ = ops.writeActive(ctx, provider.Credential{Bytes: work})
			return errors.New("post-write hiccup")
		},
	}

	got, err := runOnboarding(ctx, deps)
	if err != nil {
		t.Fatalf("runOnboarding (slot changed despite nonzero exit): %v", err)
	}
	if string(got.Bytes) != string(work) {
		t.Errorf("got %q, want %q", got.Bytes, work)
	}
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("personal should be restored; got %q", active.Bytes)
	}
}
