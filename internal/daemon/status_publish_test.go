package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// White-box tests for StatusPublisher.publish and the active-account
// resolvers. publish is called directly (no ticker) so each branch is hit
// deterministically. No t.Parallel where the keyring seam is used.

// newPublisher wires a StatusPublisher over a real store + AutoSwitcher.
func newPublisher(t *testing.T, s *store.Store) (*StatusPublisher, *AutoSwitcher) {
	t.Helper()
	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   config.DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &StatusPublisher{Store: s, Auto: a}, a
}

// readStatus decodes the published daemon-status setting.
func readStatus(ctx context.Context, t *testing.T, s *store.Store) Status {
	t.Helper()
	raw, err := s.GetSetting(ctx, store.SettingsKeyDaemonStatus)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	var st Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	return st
}

// TestPublish_NoLabel: ActiveLabel returns "", so no account is resolved and
// limits/external/unknown fields stay zero. Still publishes a row.
func TestPublish_NoLabel(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	pub, _ := newPublisher(t, s)
	pub.ActiveLabel = func(context.Context) string { return "" }

	pub.publish(ctx)

	st := readStatus(ctx, t, s)
	if st.ActiveLabel != "" {
		t.Errorf("ActiveLabel = %q, want empty", st.ActiveLabel)
	}
	if st.LimitsFetchedAt.IsZero() == false {
		t.Errorf("LimitsFetchedAt should be zero with no active account")
	}
}

// TestPublish_WithLabelAndLimits: a resolved account with a persisted limits
// row publishes those limits into the snapshot.
func TestPublish_WithLabelAndLimits(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "work", KeyringRef: "ref-w"})

	fetched := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	if err := s.PutLimits(ctx, acct.ID, provider.Limits{
		AccountID:   acct.ID,
		FiveHourPct: 42.0,
		SevenDayPct: 7.0,
		FetchedAt:   fetched,
	}); err != nil {
		t.Fatalf("PutLimits: %v", err)
	}

	pub, _ := newPublisher(t, s)
	pub.ActiveLabel = func(context.Context) string { return "work" }
	pub.publish(ctx)

	st := readStatus(ctx, t, s)
	if st.ActiveLabel != "work" {
		t.Errorf("ActiveLabel = %q want work", st.ActiveLabel)
	}
	if st.FiveHourPct != 42.0 || st.SevenDayPct != 7.0 {
		t.Errorf("limits not attached: 5h=%v 7d=%v", st.FiveHourPct, st.SevenDayPct)
	}
	if !st.LimitsFetchedAt.Equal(fetched) {
		t.Errorf("LimitsFetchedAt = %v want %v", st.LimitsFetchedAt, fetched)
	}
}

// TestPublish_LabelButNoLimitsRow: a resolved account WITHOUT a limits row
// (ErrLimitsNotFound) publishes the label but leaves limit fields at zero —
// the "no data yet" state, not an error.
func TestPublish_LabelButNoLimitsRow(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_, _ = s.CreateAccount(ctx, store.Account{Label: "fresh", KeyringRef: "ref-f"})

	pub, _ := newPublisher(t, s)
	pub.ActiveLabel = func(context.Context) string { return "fresh" }
	pub.publish(ctx)

	st := readStatus(ctx, t, s)
	if st.ActiveLabel != "fresh" {
		t.Errorf("ActiveLabel = %q want fresh", st.ActiveLabel)
	}
	if st.FiveHourPct != 0 || !st.LimitsFetchedAt.IsZero() {
		t.Errorf("expected zero limits for a fresh account, got %+v", st)
	}
}

// TestPublish_ExternalWatchAndUnknownEmail: wiring an ExternalSwitchWatcher
// and an UnknownActiveEmail resolver feeds both fields into the snapshot.
func TestPublish_ExternalWatchAndUnknownEmail(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "work", KeyringRef: "ref-w"})
	_ = acct

	pub, _ := newPublisher(t, s)
	pub.ActiveLabel = func(context.Context) string { return "work" }
	// A watcher that reports an external switch every observation.
	ext := &ExternalSwitchWatcher{Store: s, Notify: func(string, string) {}}
	// Prime it so LastExternalAt is set: baseline, then two unattributed
	// observations of a different account.
	ext.Observe(ctx, 1, "work")
	ext.Observe(ctx, 999, "other")
	ext.Observe(ctx, 999, "other")
	pub.ExternalWatch = ext
	pub.UnknownActiveEmail = func(context.Context) string { return "stranger@x.com" }

	pub.publish(ctx)

	st := readStatus(ctx, t, s)
	if st.UnknownActiveEmail != "stranger@x.com" {
		t.Errorf("UnknownActiveEmail = %q", st.UnknownActiveEmail)
	}
	if st.LastExternalSwitchAt.IsZero() {
		t.Errorf("LastExternalSwitchAt should be set after an external switch")
	}
}

// TestPublish_OnActiveChange: OnActiveChange fires exactly on a real switch —
// not on the first sighting (the scheduler's boot fetch covers that), not on a
// repeat of the same account, and not on a transient unresolved tick; it DOES
// fire when the resolved active account's ID changes. This is the trigger that
// kicks the UsageScheduler so a switched-in account refreshes promptly (#52).
func TestPublish_OnActiveChange(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.CreateAccount(ctx, store.Account{Label: "one", KeyringRef: "ref-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, store.Account{Label: "two", KeyringRef: "ref-2"}); err != nil {
		t.Fatal(err)
	}

	pub, _ := newPublisher(t, s)
	kicks := 0
	pub.OnActiveChange = func() { kicks++ }

	label := "one"
	pub.ActiveLabel = func(context.Context) string { return label }

	pub.publish(ctx) // first sighting of "one" → baseline, no fire
	if kicks != 0 {
		t.Fatalf("first sighting must not fire, got %d", kicks)
	}
	pub.publish(ctx) // same account again → no fire
	if kicks != 0 {
		t.Fatalf("repeat of same account must not fire, got %d", kicks)
	}

	label = "two"
	pub.publish(ctx) // switched one→two → fire
	if kicks != 1 {
		t.Fatalf("switch must fire once, got %d", kicks)
	}

	label = "" // transient unresolved tick → no fire, prior ID retained
	pub.publish(ctx)
	if kicks != 1 {
		t.Fatalf("unresolved tick must not fire, got %d", kicks)
	}

	label = "two" // same as last RESOLVED account → no fire
	pub.publish(ctx)
	if kicks != 1 {
		t.Fatalf("returning to the same account after an unresolved tick must not fire, got %d", kicks)
	}

	label = "one"
	pub.publish(ctx) // switched two→one → fire again
	if kicks != 2 {
		t.Fatalf("second switch must fire, got %d", kicks)
	}
}

// TestResolveActiveAccount_ByteMatch: the live blob byte-equals an account's
// stash → that account is authoritative (no claude.json needed).
func TestResolveActiveAccount_ByteMatch(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "match", KeyringRef: "ref-m"})
	blob := stashBlob("sk-x", "rtok-x", time.Now().Add(time.Hour))
	_ = claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob})

	p := &fakeProvider{active: provider.Credential{Bytes: blob}}
	got, found, err := ResolveActiveAccount(ctx, s, p, nil)
	if err != nil || !found {
		t.Fatalf("ResolveActiveAccount: found=%v err=%v", found, err)
	}
	if got.Label != "match" {
		t.Errorf("resolved %q, want match", got.Label)
	}
}

// TestResolveActiveAccount_LiveSlotCacheInvalidation proves the switch-latency
// fix for #92: `aimonitor switch` runs in a SEPARATE process and rewrites the
// live keychain slot without invalidating the long-running daemon's in-memory
// cache, so the daemon keeps resolving the pre-switch account until the cache
// TTL (~5s) expires — the lag the widget shows as a slow "Switching…". Dropping
// the live-slot cache (as resolveActiveLabel now does each tick) makes the new
// active account surface immediately instead of after the TTL.
func TestResolveActiveAccount_LiveSlotCacheInvalidation(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)

	// Two managed accounts, each stash byte-equal to its own live blob.
	blobA := stashBlob("sk-a", "rt-a", time.Now().Add(time.Hour))
	blobB := stashBlob("sk-b", "rt-b", time.Now().Add(time.Hour))
	if _, err := s.CreateAccount(ctx, store.Account{Label: "aaa", KeyringRef: "ref-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, store.Account{Label: "bbb", KeyringRef: "ref-b"}); err != nil {
		t.Fatal(err)
	}
	_ = claude.StashCredential(ctx, "ref-a", provider.Credential{Bytes: blobA})
	_ = claude.StashCredential(ctx, "ref-b", provider.Credential{Bytes: blobB})

	p := claude.New()

	// Live slot starts on A; the first resolve reads it through the keychain
	// and caches the live blob.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blobA); err != nil {
		t.Fatal(err)
	}
	if got, found, err := ResolveActiveAccount(ctx, s, p, nil); err != nil || !found || got.Label != "aaa" {
		t.Fatalf("initial resolve: got=%q found=%v err=%v, want aaa", got.Label, found, err)
	}

	// Simulate a switch by ANOTHER process: rewrite the live slot directly on
	// the keyring, bypassing this process's write path (and its cache drop).
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blobB); err != nil {
		t.Fatal(err)
	}

	// Stale cache still serves A — this is the pre-fix behaviour the daemon saw.
	if got, _, _ := ResolveActiveAccount(ctx, s, p, nil); got.Label != "aaa" {
		t.Fatalf("expected stale cache to still resolve aaa, got %q", got.Label)
	}

	// resolveActiveLabel now drops the live-slot cache before resolving; do the
	// same and the switched-in account surfaces at once, no TTL wait.
	claude.InvalidateActiveCache()
	if got, found, err := ResolveActiveAccount(ctx, s, p, nil); err != nil || !found || got.Label != "bbb" {
		t.Fatalf("after invalidate: got=%q found=%v err=%v, want bbb", got.Label, found, err)
	}
}

// TestResolveActiveAccount_IdentityFallback_NonEmptyLiveSlot: a NON-EMPTY live
// blob byte-matches no stash (a rotated live token) — the byte-match phase
// runs and misses, then claude.json's email matches an account by identity.
// (resolver_test.go covers the empty-slot variant where byte-match is skipped.)
func TestResolveActiveAccount_IdentityFallback_NonEmptyLiveSlot(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)
	// Account known by identity, stash holds a DIFFERENT blob than live.
	acct, _ := s.CreateAccount(ctx, store.Account{
		Label: "ident", Email: "me@x.com", OrganizationUUID: "ORG-1", KeyringRef: "ref-i",
	})
	_ = claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: stashBlob("sk-stash", "rt", time.Now().Add(time.Hour))})

	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "me@x.com", OrganizationUUID: "ORG-1"})

	// Live blob desynced from every stash → byte-match misses, identity wins.
	p := &fakeProvider{active: provider.Credential{Bytes: stashBlob("sk-live-rotated", "rt", time.Now().Add(time.Hour))}}
	got, found, err := ResolveActiveAccount(ctx, s, p, cc)
	if err != nil || !found {
		t.Fatalf("ResolveActiveAccount: found=%v err=%v", found, err)
	}
	if got.Label != "ident" {
		t.Errorf("resolved %q, want ident", got.Label)
	}
}

// TestUnknownActiveEmail_ReturnsLiveEmail: nothing resolves to a managed
// account, the live slot is non-empty, and claude.json names an email →
// UnknownActiveEmail surfaces it for the import prompt.
func TestUnknownActiveEmail_ReturnsLiveEmail(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)
	cc, _ := tempClaudeConfig(t)
	_ = cc.WriteOAuthAccount(ctx, claudeconfig.OAuthAccount{EmailAddress: "stranger@x.com"})

	p := &fakeProvider{active: provider.Credential{Bytes: stashBlob("sk-live", "rt", time.Now().Add(time.Hour))}}
	got := UnknownActiveEmail(ctx, s, p, cc)
	if got != "stranger@x.com" {
		t.Errorf("UnknownActiveEmail = %q, want stranger@x.com", got)
	}
}

// TestUnknownActiveEmail_KnownAccountReturnsEmpty: when the active account IS
// managed (byte-match), there's nothing to import → "".
func TestUnknownActiveEmail_KnownAccountReturnsEmpty(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "known", KeyringRef: "ref-k"})
	blob := stashBlob("sk-known", "rt", time.Now().Add(time.Hour))
	_ = claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob})

	cc, _ := tempClaudeConfig(t)
	p := &fakeProvider{active: provider.Credential{Bytes: blob}}
	if got := UnknownActiveEmail(ctx, s, p, cc); got != "" {
		t.Errorf("UnknownActiveEmail = %q, want empty for a known account", got)
	}
}

// TestUnknownActiveEmail_EmptySlotReturnsEmpty: nothing signed in → nothing to
// import even though no account resolves.
func TestUnknownActiveEmail_EmptySlotReturnsEmpty(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	s := openStore(t)
	cc, _ := tempClaudeConfig(t)
	p := &fakeProvider{active: provider.Credential{}} // empty live slot
	if got := UnknownActiveEmail(ctx, s, p, cc); got != "" {
		t.Errorf("UnknownActiveEmail = %q, want empty for an empty slot", got)
	}
}
