package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/store"
)

// When the live blob byte-matches no stash — the state after Claude Code
// (or another tool) rotates the live token out from under aimonitor —
// resolveActiveAccount must fall back to ~/.claude.json identity rather
// than returning "no active account" (the bug that left usage blank).
//
// The live slot is empty here so the byte-match phase is skipped entirely
// (no real keyring access), isolating the identity fallback: claude.json
// names the account by email+org, and GetAccountByIdentity resolves it.
func TestResolveActiveAccount_IdentityFallback(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.CreateAccount(ctx, store.Account{
		Label:            "default",
		KeyringRef:       "ref-never-stashed",
		Email:            "dev@example.com",
		OrganizationUUID: "org-123",
		OrganizationName: "Acme",
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "claude.json")
	if err := os.WriteFile(cfgPath, []byte(
		`{"oauthAccount":{"emailAddress":"dev@example.com","organizationUuid":"org-123","organizationName":"Acme"}}`,
	), 0o600); err != nil {
		t.Fatalf("write claude.json: %v", err)
	}

	acct, found, err := ResolveActiveAccount(ctx, st, &fakeProvider{}, claudeconfig.NewAt(cfgPath))
	if err != nil {
		t.Fatalf("resolveActiveAccount: %v", err)
	}
	if !found || acct.Label != "default" {
		t.Fatalf("identity fallback: found=%v label=%q, want true/default", found, acct.Label)
	}
}

// With no identity source (nil claudeconfig) and an empty live slot,
// resolveActiveAccount resolves nothing rather than erroring — the
// fresh-install / unreadable-config case the StatusPublisher renders as "—".
func TestResolveActiveAccount_NoneResolves(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	acct, found, err := ResolveActiveAccount(ctx, st, &fakeProvider{}, nil)
	if err != nil {
		t.Fatalf("resolveActiveAccount: %v", err)
	}
	if found {
		t.Fatalf("expected no active account, got %q", acct.Label)
	}
}
