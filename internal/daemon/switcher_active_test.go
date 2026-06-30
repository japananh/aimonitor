package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// These tests exercise the ACTIVE-account refresh path: RefreshActiveUsage →
// Switcher.RefreshActive → claude.ReadActiveFresh/SetActiveCredential, all
// routed through the in-memory keyring seam so the real OS Keychain and
// network are never touched.
//
// No t.Parallel: the keyring seam (opsOverride) and its credCache are process
// globals. The advisory file lock is pinned to a per-test temp path via
// sw.LockPath (the production seam) rather than $HOME, keeping it hermetic.

// seedLiveSlot writes a credential blob into Claude Code's live slot through
// the test keyring. Must be called BEFORE the first read so the seam's fresh
// cache misses to the ring.
func seedLiveSlot(t *testing.T, ring secret.Keyring, blob []byte) {
	t.Helper()
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blob); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
}

// activeSwitcher builds a real Switcher wired to a httptest token refresher,
// a temp claude.json, and a per-test advisory lock path (hermetic — never the
// real $HOME/.aimonitor-lock).
func activeSwitcher(t *testing.T, s *store.Store, refresher *claude.TokenRefresher) *Switcher {
	t.Helper()
	cc, _ := tempClaudeConfig(t)
	return &Switcher{
		Store:        s,
		Provider:     claude.New(),
		Refresher:    refresher,
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}
}

// TestRefreshActiveUsage_ValidLiveToken_NoRefresh: the live slot holds a still
// valid access token, so RefreshActiveUsage fetches+persists usage WITHOUT
// hitting the refresh endpoint, and the active account's stash is healed to
// match the live blob (byte-match resolution recovery).
func TestRefreshActiveUsage_ValidLiveToken_NoRefresh(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-active"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	blob := stashBlob("sk-live-valid", "rtok-live", time.Now().Add(time.Hour))
	seedLiveSlot(t, ring, blob)

	usage := usageServer(t)
	defer usage.Close()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("refresh endpoint must not be hit when the live token is valid")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer refresh.Close()

	sw := activeSwitcher(t, st, &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL})
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}

	limits, err := RefreshActiveUsage(ctx, st, sw, fetcher, acct)
	if err != nil {
		t.Fatalf("RefreshActiveUsage: %v", err)
	}
	if limits.FiveHourPct != 64.0 || limits.SevenDayPct != 12.0 {
		t.Errorf("limits = %+v, want 5h=64 7d=12", limits)
	}
	got, err := st.GetLimits(ctx, acct.ID)
	if err != nil || got.FiveHourPct != 64.0 {
		t.Errorf("persisted limits = %+v err=%v, want 5h=64", got, err)
	}

	// healStash should have mirrored the live blob into the (initially empty)
	// stash, since they drifted.
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("RetrieveStash after heal: %v", err)
	}
	tokens, _ := claude.ParseCredential(stash)
	if tokens.AccessToken != "sk-live-valid" {
		t.Errorf("stash not healed to live blob: %q", tokens.AccessToken)
	}
}

// TestRefreshActiveUsage_ExpiredLiveToken_RefreshesAndMirrors: the live slot
// token is expired, so RefreshActive refreshes it, writes the rotated blob
// back to BOTH the live slot and the active account's stash, then usage is
// fetched with the fresh token.
func TestRefreshActiveUsage_ExpiredLiveToken_RefreshesAndMirrors(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-active"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	blob := stashBlob("sk-stale", "rtok-live", time.Now().Add(-time.Hour))
	seedLiveSlot(t, ring, blob)

	usage := usageServer(t)
	defer usage.Close()
	var refreshCalls int
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["refresh_token"] != "rtok-live" {
			t.Errorf("refresh_token = %q want rtok-live", req["refresh_token"])
		}
		_, _ = w.Write([]byte(`{"access_token":"sk-rotated","refresh_token":"rtok-rotated","expires_in":3600}`))
	}))
	defer refresh.Close()

	sw := activeSwitcher(t, st, &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL})
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}

	limits, err := RefreshActiveUsage(ctx, st, sw, fetcher, acct)
	if err != nil {
		t.Fatalf("RefreshActiveUsage: %v", err)
	}
	if refreshCalls != 1 {
		t.Errorf("refresh hit %d times, want 1", refreshCalls)
	}
	if limits.FiveHourPct != 64.0 {
		t.Errorf("limits = %+v want 5h=64", limits)
	}

	// Rotated token mirrored into the stash.
	stash, err := claude.RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		t.Fatalf("RetrieveStash: %v", err)
	}
	tokens, _ := claude.ParseCredential(stash)
	if tokens.AccessToken != "sk-rotated" || tokens.RefreshToken != "rtok-rotated" {
		t.Errorf("stash not mirrored to rotated tokens: %+v", tokens)
	}
}

// TestRefreshActiveUsage_EmptyLiveSlot: nothing signed in. RefreshActive
// returns an empty credential (no error), and RefreshActiveUsage takes its
// distinct "no live credential" error branch WITHOUT fetching usage.
func TestRefreshActiveUsage_EmptyLiveSlot(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-active"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	// No live slot seeded → empty slot.

	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("usage endpoint must not be hit when the slot is empty")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer usage.Close()

	sw := activeSwitcher(t, st, &claude.TokenRefresher{HTTP: http.DefaultClient, TokenURL: "http://127.0.0.1:0"})
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}

	_, err = RefreshActiveUsage(ctx, st, sw, fetcher, acct)
	if err == nil {
		t.Fatal("expected an error for an empty live slot, got nil")
	}
	if _, gErr := st.GetLimits(ctx, acct.ID); gErr == nil {
		t.Errorf("no usage should have been persisted for an empty slot")
	}
}

// TestRefreshActiveUsage_DeadRefreshToken_FlagsRelogin: expired live token,
// refresh endpoint returns 401, so RefreshActive errors, RefreshActiveUsage
// flags NeedsRelogin and never fetches usage.
func TestRefreshActiveUsage_DeadRefreshToken_FlagsRelogin(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, err := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-active"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	seedLiveSlot(t, ring, stashBlob("sk-stale", "rtok-dead", time.Now().Add(-time.Hour)))

	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("usage endpoint must not be hit when the refresh fails")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer usage.Close()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid_grant", http.StatusUnauthorized)
	}))
	defer refresh.Close()

	sw := activeSwitcher(t, st, &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL})
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}

	_, err = RefreshActiveUsage(ctx, st, sw, fetcher, acct)
	if err == nil {
		t.Fatal("expected an error for a dead refresh token")
	}
	if !claude.IsRefreshExpired(err) {
		t.Errorf("err = %v, want a wrapped TokenRefreshExpiredError", err)
	}
	got, err := st.GetAccountByID(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if !got.NeedsRelogin {
		t.Errorf("account should be flagged NeedsRelogin after a dead refresh token")
	}
}

// TestSwitcher_RefreshActive_Force: force=true refreshes even a non-expired
// live token (recovery from an unexpected 401). The rotated blob lands in both
// the live slot and the active account's stash.
func TestSwitcher_RefreshActive_Force(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "active", Email: "a@x.com", KeyringRef: "ref-a"})

	// Non-expired live token; force should refresh it anyway.
	seedLiveSlot(t, ring, stashBlob("sk-current", "rtok-cur", time.Now().Add(time.Hour)))
	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "a@x.com"})

	var refreshCalls int
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshCalls++
		_, _ = w.Write([]byte(`{"access_token":"sk-forced","refresh_token":"rtok-forced","expires_in":3600}`))
	}))
	defer refresh.Close()

	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}

	cred, err := sw.RefreshActive(ctx, acct, true)
	if err != nil {
		t.Fatalf("RefreshActive force: %v", err)
	}
	defer cred.Zero()
	if refreshCalls != 1 {
		t.Errorf("force should refresh once even on a valid token; got %d calls", refreshCalls)
	}
	tokens, _ := claude.ParseCredential(cred)
	if tokens.AccessToken != "sk-forced" {
		t.Errorf("returned cred = %q, want sk-forced", tokens.AccessToken)
	}
	// Mirrored into the stash (identity matches).
	stash, _ := claude.RetrieveStash(ctx, acct.KeyringRef)
	st2, _ := claude.ParseCredential(stash)
	if st2.AccessToken != "sk-forced" {
		t.Errorf("stash not mirrored to forced token: %q", st2.AccessToken)
	}
}

// TestSwitcher_RefreshActive_IdentityMismatchSkipsMirror: when claude.json's
// identity disagrees with the account attribution, RefreshActive still
// refreshes the live slot but SKIPS the stash mirror (cross-account corruption
// guard). The stash is left at its previous value.
func TestSwitcher_RefreshActive_IdentityMismatchSkipsMirror(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "active", Email: "attributed@x.com", KeyringRef: "ref-a"})
	// Seed the stash with a distinct prior value.
	_ = claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: stashBlob("sk-old-stash", "rt", time.Now().Add(time.Hour))})

	seedLiveSlot(t, ring, stashBlob("sk-live", "rtok-live", time.Now().Add(-time.Hour)))
	// claude.json names a DIFFERENT account → liveIdentityMatches is false.
	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "someone-else@x.com"})

	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"sk-new","refresh_token":"rtok-new","expires_in":3600}`))
	}))
	defer refresh.Close()

	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}

	cred, err := sw.RefreshActive(ctx, acct, false)
	if err != nil {
		t.Fatalf("RefreshActive: %v", err)
	}
	defer cred.Zero()
	// Live slot was refreshed.
	live, _ := claude.ReadActiveFresh(ctx)
	lt, _ := claude.ParseCredential(live)
	if lt.AccessToken != "sk-new" {
		t.Errorf("live slot = %q, want refreshed sk-new", lt.AccessToken)
	}
	// Stash was NOT mirrored (identity mismatch guard) — still the old value.
	stash, _ := claude.RetrieveStash(ctx, acct.KeyringRef)
	stk, _ := claude.ParseCredential(stash)
	if stk.AccessToken != "sk-old-stash" {
		t.Errorf("stash should be untouched on identity mismatch, got %q", stk.AccessToken)
	}
}

// TestSwitcher_Switch_HappyPath: a full switch from A (live) to B. Asserts the
// live slot now holds B's blob and claude.json was patched to B's identity.
func TestSwitcher_Switch_HappyPath(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)

	aBlob := stashBlob("sk-a", "rtok-a", time.Now().Add(time.Hour))
	bBlob := stashBlob("sk-b", "rtok-b", time.Now().Add(time.Hour))

	aAcct, _ := st.CreateAccount(ctx, store.Account{Label: "A", Email: "a@x.com", OrganizationUUID: "ORG-A", KeyringRef: "ref-a"})
	bAcct, _ := st.CreateAccount(ctx, store.Account{Label: "B", Email: "b@x.com", OrganizationUUID: "ORG-B", OrganizationName: "B Inc", KeyringRef: "ref-b"})
	_ = claude.StashCredential(ctx, aAcct.KeyringRef, provider.Credential{Bytes: aBlob})
	_ = claude.StashCredential(ctx, bAcct.KeyringRef, provider.Credential{Bytes: bBlob})

	// Live slot = A (so matchAccount finds the outgoing account = A).
	seedLiveSlot(t, ring, aBlob)

	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "a@x.com", OrganizationUUID: "ORG-A"})
	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: http.DefaultClient, TokenURL: "http://127.0.0.1:0"},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}

	if err := sw.Switch(ctx, "B"); err != nil {
		t.Fatalf("Switch: %v", err)
	}

	// Live slot now holds B's blob.
	live, _ := claude.ReadActiveFresh(ctx)
	tokens, _ := claude.ParseCredential(live)
	if tokens.AccessToken != "sk-b" {
		t.Errorf("live slot access token = %q, want sk-b", tokens.AccessToken)
	}
	// claude.json patched to B's identity.
	oa, _ := cc.ReadOAuthAccount(ctx)
	if oa == nil || oa.EmailAddress != "b@x.com" || oa.OrganizationUUID != "ORG-B" {
		t.Errorf("claude.json not patched to B: %+v", oa)
	}
}

// TestSwitcher_Switch_ExpiredTargetRefreshes: the target account's stash holds
// an expired access token, so Switch refreshes it (ensureFreshTokens) before
// promoting the rotated blob to the live slot.
func TestSwitcher_Switch_ExpiredTargetRefreshes(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)

	aBlob := stashBlob("sk-a", "rtok-a", time.Now().Add(time.Hour))
	// Target B's stash is EXPIRED → triggers a refresh during the switch.
	bBlob := stashBlob("sk-b-stale", "rtok-b", time.Now().Add(-time.Hour))

	aAcct, _ := st.CreateAccount(ctx, store.Account{Label: "A", Email: "a@x.com", KeyringRef: "ref-a"})
	bAcct, _ := st.CreateAccount(ctx, store.Account{Label: "B", Email: "b@x.com", KeyringRef: "ref-b"})
	_ = claude.StashCredential(ctx, aAcct.KeyringRef, provider.Credential{Bytes: aBlob})
	_ = claude.StashCredential(ctx, bAcct.KeyringRef, provider.Credential{Bytes: bBlob})
	seedLiveSlot(t, ring, aBlob)

	var refreshCalls int
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["refresh_token"] != "rtok-b" {
			t.Errorf("refresh_token = %q want rtok-b", req["refresh_token"])
		}
		_, _ = w.Write([]byte(`{"access_token":"sk-b-fresh","refresh_token":"rtok-b2","expires_in":3600}`))
	}))
	defer refresh.Close()

	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "a@x.com"})
	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}

	if err := sw.Switch(ctx, "B"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if refreshCalls != 1 {
		t.Errorf("refresh hit %d times, want 1", refreshCalls)
	}
	live, _ := claude.ReadActiveFresh(ctx)
	tokens, _ := claude.ParseCredential(live)
	if tokens.AccessToken != "sk-b-fresh" {
		t.Errorf("live slot = %q, want the refreshed sk-b-fresh", tokens.AccessToken)
	}
}

// TestSwitcher_Switch_PatchFailureRollsBack: when patching ~/.claude.json
// fails AFTER the live slot was promoted to B, Switch rolls the live slot back
// to the outgoing A blob so tokens and identity never disagree.
func TestSwitcher_Switch_PatchFailureRollsBack(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)

	aBlob := stashBlob("sk-a", "rtok-a", time.Now().Add(time.Hour))
	bBlob := stashBlob("sk-b", "rtok-b", time.Now().Add(time.Hour))
	aAcct, _ := st.CreateAccount(ctx, store.Account{Label: "A", Email: "a@x.com", KeyringRef: "ref-a"})
	bAcct, _ := st.CreateAccount(ctx, store.Account{Label: "B", Email: "b@x.com", KeyringRef: "ref-b"})
	_ = claude.StashCredential(ctx, aAcct.KeyringRef, provider.Credential{Bytes: aBlob})
	_ = claude.StashCredential(ctx, bAcct.KeyringRef, provider.Credential{Bytes: bBlob})
	seedLiveSlot(t, ring, aBlob)

	// claude.json points at a path inside a NON-EXISTENT directory: readRaw
	// treats the missing file as empty (ok), but writeRaw's temp-file create
	// in that dir fails → patchClaudeConfig errors → rollback.
	badPath := filepath.Join(t.TempDir(), "no-such-dir", ".claude.json")
	cc := claudeconfig.NewAt(badPath)
	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: http.DefaultClient, TokenURL: "http://127.0.0.1:0"},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}

	err := sw.Switch(ctx, "B")
	if err == nil {
		t.Fatal("expected Switch to fail when claude.json patch fails")
	}
	// The error must name the claude.json patch — proving we reached step 6
	// (live slot already promoted to B) and rolled back, not merely aborted
	// before promotion.
	if !strings.Contains(err.Error(), "claude.json") {
		t.Errorf("error should mention the claude.json patch failure, got %v", err)
	}
	// Live slot must be rolled back to A.
	live, _ := claude.ReadActiveFresh(ctx)
	tokens, _ := claude.ParseCredential(live)
	if tokens.AccessToken != "sk-a" {
		t.Errorf("live slot = %q, want rolled-back sk-a", tokens.AccessToken)
	}
}

// TestSwitcher_Switch_UnknownLabel: switching to a label with no account row
// returns a friendly error and leaves the live slot untouched.
func TestSwitcher_Switch_UnknownLabel(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	cc, _ := tempClaudeConfig(t)
	sw := &Switcher{
		Store:        st,
		Provider:     claude.New(),
		Refresher:    &claude.TokenRefresher{HTTP: http.DefaultClient, TokenURL: "http://127.0.0.1:0"},
		ClaudeConfig: cc,
		LockPath:     filepath.Join(t.TempDir(), ".aimonitor-lock"),
	}
	if err := sw.Switch(ctx, "nope"); err == nil {
		t.Fatal("expected an error switching to an unknown label")
	}
}
