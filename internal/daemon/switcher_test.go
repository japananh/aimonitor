package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

func tempClaudeConfig(t *testing.T) (*claudeconfig.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".claude.json")
	return claudeconfig.NewAt(path), path
}

func TestSwitcher_PatchClaudeConfig_WritesTargetIdentity(t *testing.T) {
	cc, path := tempClaudeConfig(t)
	ctx := context.Background()
	// Seed claude.json as the outgoing account, with sibling fields.
	if err := cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "old@example.com", OrganizationUUID: "ORG-OLD"}); err != nil {
		t.Fatal(err)
	}

	sw := &Switcher{ClaudeConfig: cc}
	target := store.Account{Label: "work", Email: "new@example.com", OrganizationUUID: "ORG-NEW", OrganizationName: "Acme"}
	if err := sw.patchClaudeConfig(ctx, target); err != nil {
		t.Fatalf("patchClaudeConfig: %v", err)
	}

	got, err := cc.ReadOAuthAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailAddress != "new@example.com" || got.OrganizationUUID != "ORG-NEW" || got.OrganizationName != "Acme" {
		t.Errorf("claude.json not patched to target: %+v", got)
	}
	_ = path
}

func TestSwitcher_PatchClaudeConfig_NoIdentityIsNoop(t *testing.T) {
	cc, _ := tempClaudeConfig(t)
	ctx := context.Background()
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "stays@example.com"})

	sw := &Switcher{ClaudeConfig: cc}
	// Target has no captured identity (legacy row) → must NOT clobber claude.json.
	if err := sw.patchClaudeConfig(ctx, store.Account{Label: "legacy"}); err != nil {
		t.Fatalf("patchClaudeConfig: %v", err)
	}
	got, _ := cc.ReadOAuthAccount(ctx)
	if got == nil || got.EmailAddress != "stays@example.com" {
		t.Errorf("empty-identity target should leave claude.json untouched, got %+v", got)
	}
}

func TestSwitcher_PatchClaudeConfig_NilConfigIsNoop(t *testing.T) {
	sw := &Switcher{ClaudeConfig: nil}
	if err := sw.patchClaudeConfig(context.Background(), store.Account{Email: "x@example.com"}); err != nil {
		t.Errorf("nil ClaudeConfig should be a silent no-op, got %v", err)
	}
}

func TestSwitcher_SnapshotOutgoing_BackfillsIdentity(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	cc, _ := tempClaudeConfig(t)
	// claude.json describes the outgoing (currently-active) account.
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{
		EmailAddress: "outgoing@example.com", OrganizationUUID: "ORG-O", OrganizationName: "Outgoing Inc",
	})

	// Legacy outgoing row with no identity yet.
	outgoing, _ := s.CreateAccount(ctx, store.Account{Label: "legacy", KeyringRef: "ref-x"})

	sw := &Switcher{Store: s, Provider: &fakeProvider{}, ClaudeConfig: cc}
	// prevLive empty → skips the keychain stash write, exercises only the
	// identity-backfill path (no real keychain needed).
	sw.snapshotOutgoing(ctx, outgoing, provider.Credential{})

	got, _ := s.GetAccountByID(ctx, outgoing.ID)
	if got.Email != "outgoing@example.com" || got.OrganizationUUID != "ORG-O" {
		t.Errorf("outgoing identity not backfilled from claude.json: %+v", got)
	}
}

func TestSwitcher_SnapshotOutgoing_DoesNotOverwriteExistingIdentity(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "stale@example.com", OrganizationUUID: "ORG-STALE"})

	// Outgoing already HAS identity — backfill must not clobber it with
	// whatever claude.json happens to say.
	outgoing, _ := s.CreateAccount(ctx, store.Account{
		Label: "known", Email: "real@example.com", OrganizationUUID: "ORG-REAL", KeyringRef: "ref-y",
	})

	sw := &Switcher{Store: s, Provider: &fakeProvider{}, ClaudeConfig: cc}
	sw.snapshotOutgoing(ctx, outgoing, provider.Credential{})

	got, _ := s.GetAccountByID(ctx, outgoing.ID)
	if got.Email != "real@example.com" {
		t.Errorf("backfill clobbered existing identity: %+v", got)
	}
}
