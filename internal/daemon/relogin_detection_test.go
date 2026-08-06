package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// The BE 1 repro: Claude Code blanked the tokens on expiry, leaving a
// claudeAiOauth blob with empty accessToken and refreshToken. ensureFreshStash
// must classify that as a re-login condition (CredentialUnusableError) — not a
// generic error that sails past and only fails later at the usage fetch, where
// it wouldn't flip needs_relogin.
func TestEnsureFreshStash_EmptyTokensNeedsRelogin(t *testing.T) {
	ctx := context.Background()
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ref := "be1-ref"
	// expiresAt:0 makes the access token look non-expired (zero time), so the
	// bug is precisely that it slips through — the empty-token guard must catch it.
	blob := []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"scopes":["user:inference"]}}`)
	if err := claude.StashCredential(ctx, ref, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("StashCredential: %v", err)
	}

	acct := store.Account{Label: "BE 1", KeyringRef: ref}
	_, err := ensureFreshStash(ctx, claude.NewTokenRefresher(), acct)
	if err == nil {
		t.Fatal("expected an error for an empty-token stash")
	}
	if !claude.IsCredentialUnusable(err) {
		t.Errorf("want CredentialUnusableError, got %T: %v", err, err)
	}
	if !claude.RequiresRelogin(err) {
		t.Error("empty-token stash error must satisfy RequiresRelogin")
	}
}

// Bug 2: an account flagged needs_relogin must never be chosen as an auto-swap
// target — switching to it would write a dead credential into the live slot and
// break the running claude session. Here the flagged account has the BEST
// headroom (5%) yet the swap must land on the warmer usable account (40%).
func TestAutoSwap_SkipsNeedsReloginCandidate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	dead, _ := s.CreateAccount(ctx, store.Account{Label: "dead", KeyringRef: "ref-d"})
	warm, _ := s.CreateAccount(ctx, store.Account{Label: "warm", KeyringRef: "ref-w"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95, FetchedAt: time.Now()})
	_ = s.PutLimits(ctx, dead.ID, provider.Limits{FiveHourPct: 5, FetchedAt: time.Now()})  // best headroom…
	_ = s.PutLimits(ctx, warm.ID, provider.Limits{FiveHourPct: 40, FetchedAt: time.Now()}) // …but only this one is usable
	if err := s.SetNeedsRelogin(ctx, dead.ID, true); err != nil {
		t.Fatal(err)
	}
	immediateSwap(t, s)

	a, fsw, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if !swapped {
		t.Fatalf("expected a swap (active 95%% over threshold)")
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "warm" {
		t.Errorf("switched to %v, want [warm] — needs_relogin candidate must be excluded", fsw.switched)
	}
}
