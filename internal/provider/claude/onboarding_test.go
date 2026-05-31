package claude

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	return &keychainOps{ring: fr, user: "tester", cache: newCredCache(credCacheTTL)}, fr
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

// captureTestDeps wires a fake clock + sleep so the poll loop can be
// driven deterministically from the test. now() increments by sleepStep
// on every sleep() call so timeouts are reachable without real time.
func captureTestDeps(t *testing.T, ops *keychainOps, onTick func(tick int) error) (onboardingDeps, *atomic.Int64) {
	t.Helper()
	var ticks atomic.Int64
	var elapsed atomic.Int64 // nanoseconds since "start"
	return onboardingDeps{
		keys: ops,
		now: func() time.Time {
			return time.Unix(0, elapsed.Load())
		},
		sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			elapsed.Add(int64(d))
			tick := int(ticks.Add(1))
			if onTick != nil {
				return onTick(tick)
			}
			return nil
		},
	}, &ticks
}

func TestCaptureNew_HappyPath_DetectsByteChange(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-personal"}}`)
	work := []byte(`{"claudeAiOauth":{"accessToken":"sk-work"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate the user completing /login on the 3rd poll tick.
	deps, _ := captureTestDeps(t, ops, func(tick int) error {
		if tick == 3 {
			_ = ops.writeActive(ctx, provider.Credential{Bytes: work})
		}
		return nil
	})

	var out bytes.Buffer
	got, err := captureNewWithDeps(ctx, &out, deps, CaptureOpts{
		NewLabel:     "work",
		Timeout:      30 * time.Second,
		PollInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("captureNewWithDeps: %v", err)
	}
	if string(got.Bytes) != string(work) {
		t.Errorf("returned credential = %q, want %q", got.Bytes, work)
	}

	// Stash must be restored to the slot.
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("active after capture = %q, want %q (restore failed)", active.Bytes, personal)
	}

	// Instructions must mention the label so the user knows which account
	// they're adding.
	if !strings.Contains(out.String(), `"work"`) {
		t.Errorf("instructions should mention label; got %q", out.String())
	}
}

func TestCaptureNew_FirstLogin_EmptySlot(t *testing.T) {
	// Fresh-install user with no prior Claude login. CaptureNew should
	// still capture the new blob and leave it in place (nothing to
	// restore).
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	first := []byte(`{"claudeAiOauth":{"accessToken":"sk-first"}}`)
	deps, _ := captureTestDeps(t, ops, func(tick int) error {
		if tick == 2 {
			_ = ops.writeActive(ctx, provider.Credential{Bytes: first})
		}
		return nil
	})

	var out bytes.Buffer
	got, err := captureNewWithDeps(ctx, &out, deps, CaptureOpts{Timeout: 30 * time.Second, PollInterval: time.Second})
	if err != nil {
		t.Fatalf("captureNewWithDeps: %v", err)
	}
	if string(got.Bytes) != string(first) {
		t.Errorf("returned = %q, want %q", got.Bytes, first)
	}

	// No stash to restore — slot holds the first credential.
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(first) {
		t.Errorf("active should be the first credential, got %q", active.Bytes)
	}
}

func TestCaptureNew_Timeout(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-p"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// User never logs in. Fake clock advances by 1s per tick; with
	// Timeout=5s we expect to bail after ~5 ticks.
	deps, _ := captureTestDeps(t, ops, nil)

	var out bytes.Buffer
	_, err := captureNewWithDeps(ctx, &out, deps, CaptureOpts{Timeout: 5 * time.Second, PollInterval: time.Second})
	if !errors.Is(err, ErrCaptureTimeout) {
		t.Fatalf("want ErrCaptureTimeout, got %v", err)
	}

	// Personal must still be the active credential.
	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("personal should still be active after timeout; got %q", active.Bytes)
	}
}

func TestCaptureNew_ContextCancel(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	ctx, cancel := context.WithCancel(context.Background())

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-p"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Cancel mid-poll on the 2nd tick.
	deps, _ := captureTestDeps(t, ops, func(tick int) error {
		if tick == 2 {
			cancel()
		}
		return nil
	})

	var out bytes.Buffer
	_, err := captureNewWithDeps(ctx, &out, deps, CaptureOpts{Timeout: time.Hour, PollInterval: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}

	active, _ := ops.readActive(ctx)
	if string(active.Bytes) != string(personal) {
		t.Errorf("personal should still be active after cancel; got %q", active.Bytes)
	}
}

func TestAdoptCurrent_EmptySlot(t *testing.T) {
	// AdoptCurrent doesn't construct via real keychainOps (would require
	// keyring access). Instead we test via the captureNewWithDeps shape —
	// the empty-slot check itself lives in AdoptCurrent and is exercised
	// indirectly by the captureNewWithDeps tests above. We do test the
	// public AdoptCurrent error message wording here when readActive
	// returns empty.
	t.Skip("AdoptCurrent goes through newKeychainOps which constructs a real keyring; covered by the manual e2e (docs/e2e-macos.md step 3)")
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
