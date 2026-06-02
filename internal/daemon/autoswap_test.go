package daemon

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// fakeSwitcher records Switch calls instead of actually moving credentials.
// Lets AutoSwapper tests verify the trigger logic without touching the
// keychain or the OAuth token endpoint.
type fakeSwitcher struct {
	switched []string
	err      error
}

func (f *fakeSwitcher) Switch(_ context.Context, label string) error {
	if f.err != nil {
		return f.err
	}
	f.switched = append(f.switched, label)
	return nil
}

// withAutoSwapStubs returns an AutoSwapper backed by a fakeSwitcher so
// tests can assert which labels were chosen as candidates without
// touching the real keychain or OAuth endpoint.
//
// Returns the fakeSwitcher (for assertions) and the fakeProvider (for
// setting active credential bytes).
func withAutoSwapStubs(t *testing.T, s *store.Store) (*AutoSwapper, *fakeSwitcher, *fakeProvider) {
	t.Helper()
	fp := &fakeProvider{}
	fsw := &fakeSwitcher{}
	a := &AutoSwapper{
		Store:    s,
		Provider: fp,
		Switcher: fsw,
		// No-op notifier so tests never spawn osascript.
		Notify: func(_, _ string) {},
	}
	return a, fsw, fp
}

// immediateSwap disables the grace window for tests that assert the swap
// fires on the first MaybeSwap call.
func immediateSwap(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.PutSetting(context.Background(), SettingsKeyAutoSwapGrace, "0"); err != nil {
		t.Fatal(err)
	}
}

func TestAutoSwap_BelowThreshold_NoSwap(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref"})
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 50})

	a, _, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if swapped {
		t.Errorf("expected no swap (50%% < 80%% default threshold)")
	}
}

func TestAutoSwap_AboveThreshold_PicksLowest(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	low, _ := s.CreateAccount(ctx, store.Account{Label: "low", KeyringRef: "ref-l"})
	mid, _ := s.CreateAccount(ctx, store.Account{Label: "mid", KeyringRef: "ref-m"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95})
	_ = s.PutLimits(ctx, low.ID, provider.Limits{FiveHourPct: 10})
	_ = s.PutLimits(ctx, mid.ID, provider.Limits{FiveHourPct: 40})
	immediateSwap(t, s) // grace=0 → fire on first tick

	a, fsw, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if !swapped {
		t.Fatalf("expected swap (active 95%% > 80%% threshold)")
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "low" {
		t.Errorf("Switcher.Switch called with %v, want [\"low\"]", fsw.switched)
	}
}

func TestAutoSwap_AllNearLimit_NoSwap(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 90})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 85})

	a, _, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if swapped {
		t.Errorf("no candidate has headroom — should not swap")
	}
}

func TestAutoSwap_Disabled(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref"})
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 99})
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapEnabled, "false")

	a, _, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if swapped {
		t.Errorf("auto-swap disabled — should not swap even at 99%%")
	}
}

func TestAutoSwap_CustomThreshold(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 55})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 10})
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapThreshold, "50")

	a, _, _ := withAutoSwapStubs(t, s)
	cand, found, _ := a.pickCandidate(ctx, active.ID, 50)
	if !found || cand.Label != "other" {
		t.Errorf("pickCandidate with threshold 50: found=%v cand=%v want other", found, cand)
	}
	// The full MaybeSwap will fail at Switcher.Switch since the
	// fakeProvider has no real credential bytes — but the config-read
	// and threshold-check we care about is exercised before that
	// failure. Strictly testing config parsing here:
	enabled, threshold, graceSec, err := a.config(ctx)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !enabled {
		t.Errorf("expected enabled=true (default)")
	}
	if threshold != 50 {
		t.Errorf("threshold = %v want 50", threshold)
	}
	if graceSec != DefaultAutoSwapGraceSec {
		t.Errorf("graceSec = %d want default %d", graceSec, DefaultAutoSwapGraceSec)
	}
}

// TestAutoSwap_SwitcherErrorPropagates verifies that a Switcher failure
// surfaces from MaybeSwap as an error and is not silently swallowed.
// Important because the daemon logs MaybeSwap errors via stderr —
// swallowing them would hide token-refresh failures from the operator.
func TestAutoSwap_SwitcherErrorPropagates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 10})

	immediateSwap(t, s) // skip grace so the switch (and its error) fires now

	a, fsw, _ := withAutoSwapStubs(t, s)
	fsw.err = errors.New("boom")

	_, err := a.MaybeSwap(ctx, "active")
	if err == nil {
		t.Fatalf("expected error from MaybeSwap when Switcher fails")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %v should wrap Switcher's error", err)
	}
}

// Parsing-edge test: bad values fall back to defaults silently.
func TestAutoSwap_BadConfigValues(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapEnabled, "notabool")
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapThreshold, "999")

	_ = s.PutSetting(ctx, SettingsKeyAutoSwapGrace, "notanint")

	a, _, _ := withAutoSwapStubs(t, s)
	enabled, threshold, graceSec, err := a.config(ctx)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if enabled != DefaultAutoSwapEnabled {
		t.Errorf("enabled fallback failed: got %v", enabled)
	}
	if threshold != DefaultAutoSwapThreshold {
		t.Errorf("threshold fallback failed: got %v (parsed=%q)", threshold, strconv.FormatFloat(threshold, 'f', -1, 64))
	}
	if graceSec != DefaultAutoSwapGraceSec {
		t.Errorf("graceSec fallback failed: got %d", graceSec)
	}
}

func TestAutoSwap_GraceArmsThenFires(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	low, _ := s.CreateAccount(ctx, store.Account{Label: "low", KeyringRef: "ref-l"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95})
	_ = s.PutLimits(ctx, low.ID, provider.Limits{FiveHourPct: 10})
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapGrace, "60")

	a, fsw, _ := withAutoSwapStubs(t, s)
	var notes []string
	a.Notify = func(title, _ string) { notes = append(notes, title) }
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return clock }

	// Tick 1: arm, notify, do NOT swap.
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil || swapped {
		t.Fatalf("tick1: swapped=%v err=%v, want armed-not-swapped", swapped, err)
	}
	if len(fsw.switched) != 0 {
		t.Errorf("tick1 should not have switched, got %v", fsw.switched)
	}
	if len(notes) != 1 {
		t.Errorf("tick1 should post one notification, got %v", notes)
	}

	// Tick 2 still inside grace: still no swap.
	clock = clock.Add(30 * time.Second)
	if swapped, _ := a.MaybeSwap(ctx, "active"); swapped {
		t.Errorf("tick2 (30s, inside 60s grace) should not swap")
	}

	// Tick 3 past the deadline: fire.
	clock = clock.Add(40 * time.Second) // now 70s after arm
	swapped, err = a.MaybeSwap(ctx, "active")
	if err != nil || !swapped {
		t.Fatalf("tick3 (past grace): swapped=%v err=%v, want swapped", swapped, err)
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "low" {
		t.Errorf("expected one switch to low, got %v", fsw.switched)
	}
}

func TestAutoSwap_GraceCancelledWhenActiveDropsBelowThreshold(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	low, _ := s.CreateAccount(ctx, store.Account{Label: "low", KeyringRef: "ref-l"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95})
	_ = s.PutLimits(ctx, low.ID, provider.Limits{FiveHourPct: 10})
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapGrace, "60")

	a, fsw, _ := withAutoSwapStubs(t, s)
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return clock }

	// Tick 1: arm.
	if swapped, _ := a.MaybeSwap(ctx, "active"); swapped {
		t.Fatalf("tick1 should arm, not swap")
	}
	// Active resets below threshold (window rolled over).
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 20})
	clock = clock.Add(90 * time.Second) // well past the deadline

	// Tick 2: pending must be cancelled, no swap even though the deadline passed.
	if swapped, err := a.MaybeSwap(ctx, "active"); err != nil || swapped {
		t.Fatalf("armed swap should cancel when active drops below threshold: swapped=%v err=%v", swapped, err)
	}
	if len(fsw.switched) != 0 {
		t.Errorf("no switch should have fired, got %v", fsw.switched)
	}
}
