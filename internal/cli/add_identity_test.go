package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/spf13/cobra"
)

func TestResolveIdentity_FromClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	cc := claudeconfig.NewAt(filepath.Join(dir, ".claude.json"))
	ctx := context.Background()
	if err := cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{
		EmailAddress:     "me@example.com",
		OrganizationUUID: "ORG-1",
		OrganizationName: "Acme",
	}); err != nil {
		t.Fatal(err)
	}

	got := resolveIdentity(ctx, &cobra.Command{}, cc, nil, "flag@example.com")
	// claude.json wins over the --email flag.
	if got.Email != "me@example.com" || got.OrganizationUUID != "ORG-1" || got.OrganizationName != "Acme" {
		t.Errorf("identity = %+v, want me@example.com/ORG-1/Acme", got)
	}
}

func TestResolveIdentity_FallsBackToEmailFlag(t *testing.T) {
	dir := t.TempDir()
	cc := claudeconfig.NewAt(filepath.Join(dir, ".claude.json")) // file absent
	got := resolveIdentity(context.Background(), &cobra.Command{}, cc, nil, "flag@example.com")
	if got.Email != "flag@example.com" {
		t.Errorf("email = %q, want flag fallback", got.Email)
	}
	if got.OrganizationUUID != "" {
		t.Errorf("org should be empty when claude.json absent, got %q", got.OrganizationUUID)
	}
}

func TestResolveIdentity_ConfigErrorUsesFlag(t *testing.T) {
	got := resolveIdentity(context.Background(), &cobra.Command{}, nil, errors.New("no home"), "flag@example.com")
	if got.Email != "flag@example.com" {
		t.Errorf("email = %q, want flag fallback on cc error", got.Email)
	}
}
