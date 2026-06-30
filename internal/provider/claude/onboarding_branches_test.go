package claude

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
)

// Covers the hermetically-reachable error branches in onboarding.go via the
// existing captureNewWithDeps / runOnboarding seams plus the ClaudeCLI env
// hook. No real exec, network, or OS keychain.
//
// Deliberately NOT covered (un-hermetic ceilings): CaptureNew and
// AdoptCurrent both construct a real keyring via newKeychainOps/sharedOps,
// and the production login flow shells out to the `claude` CLI.

func TestClaudeCLI_EnvOverride(t *testing.T) {
	t.Setenv("AIMONITOR_CLAUDE_BIN", "/custom/claude")
	if got := ClaudeCLI(); got != "/custom/claude" {
		t.Errorf("ClaudeCLI with env override = %q, want /custom/claude", got)
	}
	t.Setenv("AIMONITOR_CLAUDE_BIN", "")
	if got := ClaudeCLI(); got != "claude" {
		t.Errorf("ClaudeCLI default = %q, want claude", got)
	}
}

// failingReadKeyring wraps an in-memory store but fails Get after the first
// `failAfter` successful reads, simulating a keychain that goes unreadable
// mid-flow (locked during the OAuth dance).
type failingReadKeyring struct {
	store     map[string][]byte
	reads     int
	failAfter int // -1 means never fail
	failWrite bool
}

func newFailingReadKeyring(failAfter int) *failingReadKeyring {
	return &failingReadKeyring{store: map[string][]byte{}, failAfter: failAfter}
}

func (f *failingReadKeyring) key(s, a string) string { return s + "\x00" + a }

func (f *failingReadKeyring) Get(s, a string) ([]byte, error) {
	f.reads++
	if f.failAfter >= 0 && f.reads > f.failAfter {
		return nil, errors.New("simulated read failure")
	}
	v, ok := f.store[f.key(s, a)]
	if !ok {
		return nil, secret.ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (f *failingReadKeyring) Set(s, a string, d []byte) error {
	if f.failWrite {
		return errors.New("simulated write failure")
	}
	cp := make([]byte, len(d))
	copy(cp, d)
	f.store[f.key(s, a)] = cp
	return nil
}

func (f *failingReadKeyring) Delete(s, a string) error {
	if _, ok := f.store[f.key(s, a)]; !ok {
		return secret.ErrNotFound
	}
	delete(f.store, f.key(s, a))
	return nil
}

func opsWith(ring secret.Keyring) *keychainOps {
	return &keychainOps{ring: ring, user: "tester", cache: newCredCache(credCacheTTL)}
}

func TestCaptureNewWithDeps_StashReadError(t *testing.T) {
	// readActive (the stash read) fails on the very first call.
	fr := newFailingReadKeyring(0) // fail every read
	deps := onboardingDeps{
		keys:  opsWith(fr),
		now:   time.Now,
		sleep: func(context.Context, time.Duration) error { return nil },
	}
	var out bytes.Buffer
	_, err := captureNewWithDeps(context.Background(), &out, deps, CaptureOpts{
		Timeout: time.Second, PollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("stash read failure: want error")
	}
}

func TestCaptureNewWithDeps_RestoreFailure(t *testing.T) {
	// Stash a "personal" blob, then have the poll detect a "work" blob, but
	// make the restore writeActive fail — captureNewWithDeps must still
	// return the captured credential alongside the restore-failure error.
	fr := newFailingReadKeyring(-1) // reads never fail
	ops := opsWith(fr)
	ctx := context.Background()

	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-personal"}}`)
	if err := ops.writeActive(ctx, provider.Credential{Bytes: personal}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	work := []byte(`{"claudeAiOauth":{"accessToken":"sk-work"}}`)
	tick := 0
	deps := onboardingDeps{
		keys: ops,
		now:  time.Now,
		sleep: func(context.Context, time.Duration) error {
			tick++
			if tick == 1 {
				// Write the new blob directly into the ring (bypassing the
				// cache so the next readActive sees the change).
				_ = fr.Set(ClaudeCodeService, ops.user, work)
				ops.cache.invalidate(cacheKey(ClaudeCodeService, ops.user))
				// Arm the restore write to fail.
				fr.failWrite = true
			}
			return nil
		},
	}
	var out bytes.Buffer
	got, err := captureNewWithDeps(ctx, &out, deps, CaptureOpts{
		Timeout: 10 * time.Second, PollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("restore failure: want error")
	}
	if string(got.Bytes) != string(work) {
		t.Errorf("should still return captured credential %q, got %q", work, got.Bytes)
	}
}

func TestRunOnboarding_ReReadAfterLoginError(t *testing.T) {
	// Stash read (1st) succeeds; login runs; re-read (2nd) fails. Exercises
	// the re-read-after-login error branch plus the best-effort restore.
	fr := newFailingReadKeyring(1) // fail after the first successful read
	ops := opsWith(fr)
	ctx := context.Background()

	// Seed the ring directly so the first readActive succeeds from the ring
	// (not the cache). We must avoid the cache for the stash read so the
	// counted ring read happens; clear cache before the run.
	personal := []byte(`{"claudeAiOauth":{"accessToken":"sk-p"}}`)
	if err := fr.Set(ClaudeCodeService, ops.user, personal); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deps := onboardingDeps{
		keys: ops,
		login: func(context.Context) error {
			// login "succeeds" but the keychain becomes unreadable. Drop the
			// cached stash so the re-read goes to the ring (read #2), which
			// the failing keyring rejects.
			ops.cache.invalidate(cacheKey(ClaudeCodeService, ops.user))
			return nil
		},
	}
	_, err := runOnboarding(ctx, deps)
	if err == nil {
		t.Fatal("re-read after login failure: want error")
	}
}
