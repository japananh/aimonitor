package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/store"
)

func reloginTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "relogin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// reloginTargets: no args + no --all selects only accounts flagged
// NeedsRelogin; --all selects everything; explicit labels select those.
func TestReloginTargets(t *testing.T) {
	ctx := context.Background()
	s := reloginTestStore(t)

	a, _ := s.CreateAccount(ctx, store.Account{Label: "BE 1", Email: "dev.be.1@gempages.help", KeyringRef: "r1"})
	b, _ := s.CreateAccount(ctx, store.Account{Label: "BE 2", Email: "dev.be.2@gempages.help", KeyringRef: "r2"})
	_, _ = s.CreateAccount(ctx, store.Account{Label: "BE 3", Email: "dev.be.3@gempages.help", KeyringRef: "r3"})
	if err := s.SetNeedsRelogin(ctx, b.ID, true); err != nil {
		t.Fatal(err)
	}

	// Default: only the flagged account.
	got, err := reloginTargets(ctx, s, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "BE 2" {
		t.Fatalf("default targets = %v, want [BE 2]", labels(got))
	}

	// --all: everyone.
	got, err = reloginTargets(ctx, s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("--all targets = %v, want 3", labels(got))
	}

	// Explicit label wins even when not flagged.
	got, err = reloginTargets(ctx, s, []string{"BE 1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("label targets = %v, want [BE 1]", labels(got))
	}

	// Unknown label errors.
	if _, err := reloginTargets(ctx, s, []string{"nope"}, false); err == nil {
		t.Error("unknown label must error")
	}
}

// resolveLinkSource: manual/unset/unknown/slack-without-channel all fall
// back to manual (nil source) with an explanatory note. (The connected-
// Slack branch reads the keyring, so it's exercised via the live command,
// not here.)
func TestResolveLinkSource(t *testing.T) {
	ctx := context.Background()
	s := reloginTestStore(t)

	if src, note := resolveLinkSource(ctx, s, reloginOpts{manual: true}); src != nil || note != "" {
		t.Errorf("--manual: src=%v note=%q, want nil + no note", src, note)
	}
	if src, note := resolveLinkSource(ctx, s, reloginOpts{}); src != nil || note == "" {
		t.Errorf("unset source: src=%v note=%q, want nil + tip", src, note)
	}

	_ = s.PutSetting(ctx, settingsKeyLinkSource, "telegram")
	if src, note := resolveLinkSource(ctx, s, reloginOpts{}); src != nil || !strings.Contains(note, "supported") {
		t.Errorf("unknown source: src=%v note=%q, want nil + unsupported note", src, note)
	}

	_ = s.PutSetting(ctx, settingsKeyLinkSource, "slack")
	if src, note := resolveLinkSource(ctx, s, reloginOpts{}); src != nil || !strings.Contains(note, "slack.channel") {
		t.Errorf("slack without channel: src=%v note=%q, want nil + channel hint", src, note)
	}
}

func labels(as []store.Account) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Label
	}
	return out
}
