package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// TestRefreshAccountUsage_ExpiredStashNoRefreshToken: an expired stash with no
// refresh token can't be rotated. ensureFreshStash returns the "re-add it"
// error WITHOUT a network call, and no usage is fetched. Deterministic; uses
// the keyring seam and a pinned $HOME for the lock path.
func TestRefreshAccountUsage_ExpiredStashNoRefreshToken(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()
	t.Setenv("HOME", t.TempDir()) // ensureFreshStash → defaultLockPath has no seam

	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "norefresh", KeyringRef: "ref-nr"})
	// Expired access token, EMPTY refresh token.
	blob := stashBlob("sk-stale", "", time.Now().Add(-time.Hour))
	if err := claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("seed stash: %v", err)
	}

	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("usage endpoint must not be reached without a refresh token")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer usage.Close()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("refresh endpoint must not be reached without a refresh token")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer refresh.Close()

	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	_, err := RefreshAccountUsage(ctx, st, fetcher, refresher, acct)
	if err == nil {
		t.Fatal("expected an error for an expired stash with no refresh token")
	}
	if !strings.Contains(err.Error(), "no refresh token") {
		t.Errorf("error = %v, want a 'no refresh token' message", err)
	}
}

// Tests for cooldown.go: recordThrottle / clearThrottle / markRelogin. These
// also exercise clampDuration (shared with the usage scheduler) for free via
// the Retry-After clamp path.

// TestRecordThrottle_NonThrottleIsNoop: a plain error is not a 429, so no
// cooldown is set and the function reports false.
func TestRecordThrottle_NonThrottleIsNoop(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	if recordThrottle(ctx, s, acct, errors.New("boom")) {
		t.Errorf("non-throttle error must not set a cooldown")
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	if !got.CooldownUntil.IsZero() {
		t.Errorf("cooldown unexpectedly set: %v", got.CooldownUntil)
	}
}

// TestRecordThrottle_DefaultDuration: a 429 with no usable Retry-After parks
// the account for the conservative default (15m), inside [min,max].
func TestRecordThrottle_DefaultDuration(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	err := &claude.UsageThrottledError{Status: 429} // no RetryAfter
	if !recordThrottle(ctx, s, acct, err) {
		t.Fatalf("a 429 should set a cooldown")
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	if got.CooldownUntil.IsZero() {
		t.Fatalf("cooldown not persisted")
	}
	d := time.Until(got.CooldownUntil)
	if d < cooldownDefault-time.Minute || d > cooldownDefault+time.Minute {
		t.Errorf("cooldown %v, want ~%v (default)", d, cooldownDefault)
	}
}

// TestRecordThrottle_RetryAfterClampedToMax: an absurd Retry-After is clamped
// to cooldownMax — proving clampDuration's upper bound.
func TestRecordThrottle_RetryAfterClampedToMax(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	err := &claude.UsageThrottledError{Status: 429, RetryAfter: 24 * time.Hour}
	if !recordThrottle(ctx, s, acct, err) {
		t.Fatalf("a 429 should set a cooldown")
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	d := time.Until(got.CooldownUntil)
	if d > cooldownMax+time.Minute {
		t.Errorf("cooldown %v exceeds clamp max %v", d, cooldownMax)
	}
	if d < cooldownMax-time.Minute {
		t.Errorf("cooldown %v below clamp max %v", d, cooldownMax)
	}
}

// TestRecordThrottle_RetryAfterClampedToMin: a tiny Retry-After is raised to
// cooldownMin — clampDuration's lower bound.
func TestRecordThrottle_RetryAfterClampedToMin(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	err := &claude.UsageThrottledError{Status: 429, RetryAfter: time.Second}
	if !recordThrottle(ctx, s, acct, err) {
		t.Fatalf("a 429 should set a cooldown")
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	d := time.Until(got.CooldownUntil)
	if d < cooldownMin-time.Second {
		t.Errorf("cooldown %v below clamp min %v", d, cooldownMin)
	}
}

// TestClearThrottle_LiftsCooldown: clearThrottle removes a currently-cooling
// account's cooldown.
func TestClearThrottle_LiftsCooldown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	if err := s.SetCooldown(ctx, acct.ID, time.Now().Add(time.Hour), "429"); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}

	clearThrottle(ctx, s, acct)

	got, _ := s.GetAccountByID(ctx, acct.ID)
	if !got.CooldownUntil.IsZero() {
		t.Errorf("cooldown not cleared: %v", got.CooldownUntil)
	}
}

// TestMarkRelogin_SetsFlagOnDeadRefresh: a TokenRefreshExpiredError flags the
// account NeedsRelogin.
func TestMarkRelogin_SetsFlagOnDeadRefresh(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	markRelogin(ctx, s, acct, &claude.TokenRefreshExpiredError{Status: 401})

	got, _ := s.GetAccountByID(ctx, acct.ID)
	if !got.NeedsRelogin {
		t.Errorf("NeedsRelogin should be set after a dead refresh token")
	}
}

// TestMarkRelogin_ClearsFlagOnSuccess: a nil error clears a previously-set
// NeedsRelogin flag.
func TestMarkRelogin_ClearsFlagOnSuccess(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	if err := s.SetNeedsRelogin(ctx, acct.ID, true); err != nil {
		t.Fatalf("SetNeedsRelogin: %v", err)
	}

	markRelogin(ctx, s, acct, nil)

	got, _ := s.GetAccountByID(ctx, acct.ID)
	if got.NeedsRelogin {
		t.Errorf("NeedsRelogin should be cleared after a successful refresh")
	}
}

// TestMarkRelogin_OtherErrorLeavesFlagUntouched: a network/429 error neither
// sets nor clears the flag.
func TestMarkRelogin_OtherErrorLeavesFlagUntouched(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	if err := s.SetNeedsRelogin(ctx, acct.ID, true); err != nil {
		t.Fatalf("SetNeedsRelogin: %v", err)
	}

	markRelogin(ctx, s, acct, errors.New("transient network error"))

	got, _ := s.GetAccountByID(ctx, acct.ID)
	if !got.NeedsRelogin {
		t.Errorf("an unrelated error must leave NeedsRelogin untouched (was true)")
	}
}
