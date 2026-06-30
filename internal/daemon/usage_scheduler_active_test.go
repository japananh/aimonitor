package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// Tests for UsageScheduler.activeAccount (byte-match against the live slot)
// and activePct (the hotter of the active account's persisted utilizations),
// the default resolvers used when ResolveActive is unset. No t.Parallel: the
// keyring seam is a process global.

// TestScheduler_ActiveAccount_ByteMatch: the live blob byte-equals an
// account's stash → activeAccount resolves it.
func TestScheduler_ActiveAccount_ByteMatch(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	blob := stashBlob("sk-live", "rt", time.Now().Add(time.Hour))
	_ = claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob})

	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{active: provider.Credential{Bytes: blob}},
	}
	got, found, err := u.activeAccount(ctx)
	if err != nil || !found {
		t.Fatalf("activeAccount: found=%v err=%v", found, err)
	}
	if got.Label != "active" {
		t.Errorf("resolved %q want active", got.Label)
	}
}

// TestScheduler_ActiveAccount_EmptySlot: an empty live slot resolves nothing
// without error.
func TestScheduler_ActiveAccount_EmptySlot(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := claude.SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	st := openStore(t)
	_, _ = st.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})

	u := &UsageScheduler{Store: st, Provider: &fakeProvider{active: provider.Credential{}}}
	_, found, err := u.activeAccount(ctx)
	if err != nil {
		t.Fatalf("activeAccount err: %v", err)
	}
	if found {
		t.Errorf("empty slot should resolve nothing")
	}
}

// TestScheduler_Run_FiresInitialTickThenStops: Run with a tiny baseline fires
// at least one tickOnce (via initialDelay, clamped to baseline) then returns
// nil when the context is cancelled. Uses the no-active-account path so no
// HTTP/timer-heavy work runs.
func TestScheduler_Run_FiresInitialTickThenStops(t *testing.T) {
	st := openStore(t)
	var ticks atomic.Int32
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		Fetcher:  &claude.UsageFetcher{},
		Baseline: 5 * time.Millisecond, // initialDelay clamps to this
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			ticks.Add(1)
			return store.Account{}, false, nil // no active → tickOnce returns nil, no HTTP
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- u.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for ticks.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("scheduler never fired a tick")
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestScheduler_ActivePct_UsesHotterWindow: activePct returns the max of the
// active account's 5h/7d utilization. Here 7d (60) is hotter than 5h (30).
func TestScheduler_ActivePct_UsesHotterWindow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	_ = st.PutLimits(ctx, acct.ID, provider.Limits{AccountID: acct.ID, FiveHourPct: 30, SevenDayPct: 60})

	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
	}
	pct, ok := u.activePct(ctx)
	if !ok || pct != 60 {
		t.Errorf("activePct = %v,%v want 60,true (hotter 7d window)", pct, ok)
	}
}

// TestScheduler_ActivePct_NoActive: when nothing resolves, activePct reports
// (0, false) so the scheduler stays on the baseline cadence.
func TestScheduler_ActivePct_NoActive(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return store.Account{}, false, nil
		},
	}
	if pct, ok := u.activePct(ctx); ok || pct != 0 {
		t.Errorf("activePct = %v,%v want 0,false", pct, ok)
	}
}

// TestScheduler_ActivePct_NoLimitsRow: resolved account but no persisted
// limits → (0, false).
func TestScheduler_ActivePct_NoLimitsRow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "fresh", KeyringRef: "ref-f"})
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
	}
	if pct, ok := u.activePct(ctx); ok || pct != 0 {
		t.Errorf("activePct = %v,%v want 0,false for a fresh account", pct, ok)
	}
}

// TestScheduler_ActivePct_ResolveError: a resolver error is swallowed to
// (0, false) — the scheduler never propagates it into a cadence decision.
func TestScheduler_ActivePct_ResolveError(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return store.Account{}, false, errors.New("boom")
		},
	}
	if pct, ok := u.activePct(ctx); ok || pct != 0 {
		t.Errorf("activePct = %v,%v want 0,false on resolver error", pct, ok)
	}
}
