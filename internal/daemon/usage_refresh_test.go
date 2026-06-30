package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// These tests exercise RefreshAccountUsage end-to-end through the TEST-ONLY
// keyring seam (claude.SetKeyringForTest + secret.NewMemoryKeyring), so the
// real OS Keychain is never touched and they run/pass on CI.
//
// No t.Parallel: the seam (opsOverride) and the credCache it owns are process
// globals, and the expired-token path acquires $HOME/.aimonitor-lock.

// stashBlob builds a syntactically-valid Claude OAuth credential blob. A zero
// expiresAt means "no recorded expiry" (treated as not-expired); a non-zero
// expiresAt in the past makes the token look expired so the refresh path runs.
func stashBlob(accessToken, refreshToken string, expiresAt time.Time) []byte {
	oauth := map[string]any{
		"accessToken":  accessToken,
		"refreshToken": refreshToken,
		"subscriptionType": "pro", // an extra field, to prove ReplaceTokens preserves it
	}
	if !expiresAt.IsZero() {
		oauth["expiresAt"] = expiresAt.UnixMilli()
	}
	b, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		panic(err)
	}
	return b
}

// usageServer returns an httptest server that serves a canned usage payload.
// FetchLimits hits "/" (UsageFetcher.BaseURL + UsageEndpoint), so anything that
// is NOT the usage endpoint is treated as "should never be hit" by the caller.
func usageServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"five_hour": {"utilization": 64.0, "resets_at": "2026-05-31T23:00:00Z"},
			"seven_day": {"utilization": 12.0, "resets_at": "2026-06-07T00:00:00Z"}
		}`))
	}))
}

// TestRefreshAccountUsage_ValidStash_NoRefresh covers the common case: the
// stashed access token is still valid, so RefreshAccountUsage fetches usage and
// persists it WITHOUT ever calling the token-refresh endpoint.
func TestRefreshAccountUsage_ValidStash_NoRefresh(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "valid", KeyringRef: "ref-valid"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Token valid for an hour → IsExpired is false → no refresh.
	blob := stashBlob("sk-valid", "rtok-valid", time.Now().Add(time.Hour))
	if err := claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("seed stash: %v", err)
	}

	usage := usageServer(t)
	defer usage.Close()

	// The refresh endpoint must NEVER be hit on the valid-token path: fail
	// inside the handler if it is.
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("refresh endpoint must not be called when the stash token is valid")
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer refresh.Close()

	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	limits, err := RefreshAccountUsage(ctx, st, fetcher, refresher, acct)
	if err != nil {
		t.Fatalf("RefreshAccountUsage: %v", err)
	}
	if limits.FiveHourPct != 64.0 || limits.SevenDayPct != 12.0 {
		t.Errorf("limits = %+v, want 5h=64 7d=12", limits)
	}

	// Persisted via PutLimits.
	got, err := st.GetLimits(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetLimits: %v", err)
	}
	if got.FiveHourPct != 64.0 || got.SevenDayPct != 12.0 {
		t.Errorf("persisted limits = %+v, want 5h=64 7d=12", got)
	}

	// The stash is untouched (no rotation).
	cur, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("RetrieveStash: %v", err)
	}
	tokens, err := claude.ParseCredential(cur)
	if err != nil {
		t.Fatalf("ParseCredential: %v", err)
	}
	if tokens.AccessToken != "sk-valid" {
		t.Errorf("stash access token = %q, want unchanged sk-valid", tokens.AccessToken)
	}
}

// TestRefreshAccountUsage_ExpiredStash_RefreshesAndReStashes covers the rotation
// path: the stashed access token is expired, so RefreshAccountUsage refreshes it
// via the refresh endpoint, RE-STASHES the rotated blob (asserted by reading it
// back through the seam), then fetches + persists usage with the fresh token.
func TestRefreshAccountUsage_ExpiredStash_RefreshesAndReStashes(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()
	// ensureFreshStash acquires $HOME/.aimonitor-lock on the refresh path.
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "expired", KeyringRef: "ref-expired"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Expired token (1h in the past) WITH a refresh token → triggers refresh.
	blob := stashBlob("sk-stale", "rtok-live", time.Now().Add(-time.Hour))
	if err := claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("seed stash: %v", err)
	}

	usage := usageServer(t)
	defer usage.Close()

	var refreshCalls int
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["refresh_token"] != "rtok-live" {
			t.Errorf("refresh request refresh_token = %q, want rtok-live", req["refresh_token"])
		}
		_, _ = w.Write([]byte(`{
			"access_token":  "sk-rotated",
			"refresh_token": "rtok-rotated",
			"expires_in":    3600
		}`))
	}))
	defer refresh.Close()

	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	limits, err := RefreshAccountUsage(ctx, st, fetcher, refresher, acct)
	if err != nil {
		t.Fatalf("RefreshAccountUsage: %v", err)
	}
	if refreshCalls != 1 {
		t.Errorf("refresh endpoint hit %d times, want exactly 1", refreshCalls)
	}
	if limits.FiveHourPct != 64.0 {
		t.Errorf("limits = %+v, want 5h=64", limits)
	}

	// Persisted.
	got, err := st.GetLimits(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetLimits: %v", err)
	}
	if got.FiveHourPct != 64.0 {
		t.Errorf("persisted limits = %+v, want 5h=64", got)
	}

	// The ROTATED token is now in the keyring — read it back through the seam.
	cur, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("RetrieveStash after rotation: %v", err)
	}
	tokens, err := claude.ParseCredential(cur)
	if err != nil {
		t.Fatalf("ParseCredential: %v", err)
	}
	if tokens.AccessToken != "sk-rotated" {
		t.Errorf("re-stashed access token = %q, want sk-rotated", tokens.AccessToken)
	}
	if tokens.RefreshToken != "rtok-rotated" {
		t.Errorf("re-stashed refresh token = %q, want rtok-rotated", tokens.RefreshToken)
	}
	// ReplaceTokens must have preserved the unrelated field.
	var doc map[string]any
	if err := json.Unmarshal(cur.Bytes, &doc); err != nil {
		t.Fatalf("unmarshal re-stashed blob: %v", err)
	}
	if oauth, ok := doc["claudeAiOauth"].(map[string]any); !ok || oauth["subscriptionType"] != "pro" {
		t.Errorf("re-stashed blob dropped subscriptionType: %v", doc)
	}
}

// TestRefreshAccountUsage_DeadRefreshToken_FlagsRelogin covers the dead-refresh
// path: the access token is expired and the refresh endpoint returns 401, so
// RefreshAccountUsage returns an error, flags the account NeedsRelogin in the
// store, and NEVER reaches the usage fetch.
func TestRefreshAccountUsage_DeadRefreshToken_FlagsRelogin(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "dead", KeyringRef: "ref-dead"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	blob := stashBlob("sk-stale", "rtok-dead", time.Now().Add(-time.Hour))
	if err := claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("seed stash: %v", err)
	}

	// Usage endpoint must NEVER be reached: the refresh fails first.
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("usage endpoint must not be reached when the refresh token is dead")
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer usage.Close()

	// Refresh endpoint returns 401 → TokenRefreshExpiredError.
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid_grant", http.StatusUnauthorized)
	}))
	defer refresh.Close()

	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	_, err = RefreshAccountUsage(ctx, st, fetcher, refresher, acct)
	if err == nil {
		t.Fatalf("expected an error for a dead refresh token, got nil")
	}
	// The wrapped TokenRefreshExpiredError must unwrap so markRelogin fires.
	if !claude.IsRefreshExpired(err) {
		t.Errorf("err = %v, want a wrapped TokenRefreshExpiredError", err)
	}

	// The account must now be flagged NeedsRelogin.
	got, err := st.GetAccountByID(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if !got.NeedsRelogin {
		t.Errorf("account should be flagged NeedsRelogin after a dead refresh token")
	}

	// And no usage row was persisted.
	if _, err := st.GetLimits(ctx, acct.ID); err == nil {
		t.Errorf("no usage should have been persisted when the refresh failed")
	}
}
