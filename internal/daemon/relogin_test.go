package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

func TestMarkRelogin(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a, err := s.CreateAccount(ctx, store.Account{Label: "x", KeyringRef: "r"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Stub the banner so the test never spawns osascript; count the calls.
	var notifs int
	defer func(orig func(string, string)) { reloginNotify = orig }(reloginNotify)
	reloginNotify = func(_, _ string) { notifs++ }

	// A wrapped expired-token error sets the flag (errors.As must unwrap it).
	expired := fmt.Errorf("refresh %q token: %w", "x", &claude.TokenRefreshExpiredError{Status: 400})
	markRelogin(ctx, s, a, expired)
	if got, _ := s.GetAccountByID(ctx, a.ID); !got.NeedsRelogin {
		t.Fatal("expired-token error should set needs_relogin")
	}
	if notifs != 1 {
		t.Errorf("false→true transition should notify once, got %d", notifs)
	}

	// A non-token error (network, 429) must NOT clear an existing flag.
	markRelogin(ctx, s, a, errors.New("network boom"))
	if got, _ := s.GetAccountByID(ctx, a.ID); !got.NeedsRelogin {
		t.Error("unrelated error must leave needs_relogin set")
	}

	// A successful refresh (nil err) clears it.
	markRelogin(ctx, s, a, nil)
	if got, _ := s.GetAccountByID(ctx, a.ID); got.NeedsRelogin {
		t.Error("nil err (success) should clear needs_relogin")
	}
}

// A structurally-broken stash (Claude Code blanked the tokens) and a
// server-rejected access token (usage 401) are re-login conditions too — not
// just a dead refresh token. Each sets the flag and notifies once per
// transition (an already-flagged account re-hitting the error must not re-notify).
func TestMarkRelogin_UnusableAndAuthError(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	var notifs int
	defer func(orig func(string, string)) { reloginNotify = orig }(reloginNotify)
	reloginNotify = func(_, _ string) { notifs++ }

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unusable", &claude.CredentialUnusableError{Label: "be1", Reason: "no tokens"}},
		{"usage-401", &claude.UsageAuthError{Status: 401}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifs = 0
			a, _ := s.CreateAccount(ctx, store.Account{Label: tc.name, KeyringRef: "k-" + tc.name})

			markRelogin(ctx, s, a, tc.err) // a.NeedsRelogin is false → transition
			got, _ := s.GetAccountByID(ctx, a.ID)
			if !got.NeedsRelogin {
				t.Fatalf("%s should set needs_relogin", tc.name)
			}
			if notifs != 1 {
				t.Errorf("first detection should notify once, got %d", notifs)
			}

			// Re-hit with the now-flagged account: no second banner.
			markRelogin(ctx, s, got, tc.err)
			if notifs != 1 {
				t.Errorf("already-flagged account must not re-notify, got %d", notifs)
			}
		})
	}
}
