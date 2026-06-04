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

// B: candidate selection must prefer an account with FRESH, known low usage
// over accounts whose usage is stale or unknown — even though those would
// look "lower" if trusted naively (stale 5% < fresh 10%, unknown = 0%). With
// no just-in-time refresh wired, stale/unknown are last-resort only.
func TestAutoSwap_PrefersFreshKnownCandidate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "r0"})
	freshLow, _ := s.CreateAccount(ctx, store.Account{Label: "fresh-low", KeyringRef: "r1"})
	staleLow, _ := s.CreateAccount(ctx, store.Account{Label: "stale-low", KeyringRef: "r2"})
	_, _ = s.CreateAccount(ctx, store.Account{Label: "unknown", KeyringRef: "r3"})

	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 90})   // over threshold, fresh
	_ = s.PutLimits(ctx, freshLow.ID, provider.Limits{FiveHourPct: 10}) // fresh, low
	// Deceptively lower, but two hours old → must NOT be trusted over fresh-low.
	_ = s.PutLimits(ctx, staleLow.ID, provider.Limits{FiveHourPct: 5, FetchedAt: time.Now().Add(-2 * time.Hour)})
	// "unknown" has no limits row at all.

	a, fsw, _ := withAutoSwapStubs(t, s)
	immediateSwap(t, s) // grace 0: fire on first call
	// RefreshUsage left nil — selection must rely on stored data only.

	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if !swapped {
		t.Fatalf("expected a swap (active 90%% >= 80%% threshold)")
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "fresh-low" {
		t.Errorf("switched to %v, want [fresh-low] — must prefer fresh-known over stale/unknown", fsw.switched)
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

// Candidates are judged RELATIVE to the active account: even when both are
// over the threshold, an account with lower utilization on the binding
// window is still a win (it has more remaining headroom).
func TestAutoSwap_RelativeHeadroom_SwapsEvenWhenCandidateOverThreshold(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 90})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 85})
	immediateSwap(t, s)

	a, fsw, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if !swapped || len(fsw.switched) != 1 || fsw.switched[0] != "other" {
		t.Errorf("85%% beats 90%% on the binding window — want switch to other; swapped=%v switched=%v", swapped, fsw.switched)
	}
}

// When no account is lower than the active on the binding window, there is
// nothing to gain — stay put and notify.
func TestAutoSwap_AllNearLimit_NoSwap(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 90})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 95}) // hotter than active
	immediateSwap(t, s)

	a, fsw, _ := withAutoSwapStubs(t, s)
	var notes []string
	a.Notify = func(title, _ string) { notes = append(notes, title) }
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if swapped || len(fsw.switched) != 0 {
		t.Errorf("no candidate beats active on the binding window — should not swap; got %v", fsw.switched)
	}
	if len(notes) != 1 || notes[0] != "All accounts near limit" {
		t.Errorf("want the all-accounts-near-limit notification, got %v", notes)
	}
}

// The Gem-2 regression: weekly (7d) limit nearly exhausted while the 5-hour
// window is quiet. The swap must trigger on the 7-day window.
func TestAutoSwap_WeeklyCapTriggersSwap(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "gem2", KeyringRef: "ref-a"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "gem3", KeyringRef: "ref-o"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 10, SevenDayPct: 97})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 20, SevenDayPct: 30})
	immediateSwap(t, s)

	a, fsw, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "gem2")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	if !swapped || len(fsw.switched) != 1 || fsw.switched[0] != "gem3" {
		t.Errorf("97%% weekly must trigger a swap to gem3; swapped=%v switched=%v", swapped, fsw.switched)
	}
}

// Tier 2: active is weekly-capped; the only candidates are 5h-hot but
// weekly-healthy. Escaping the multi-day weekly exhaustion wins — pick the
// candidate lowest on the binding (7-day) window even though its 5-hour
// usage is above the active's.
func TestAutoSwap_WeeklyCapped_SwitchesTo5hHotButWeeklyHealthy(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	hotA, _ := s.CreateAccount(ctx, store.Account{Label: "hot-a", KeyringRef: "ref-b"})
	hotB, _ := s.CreateAccount(ctx, store.Account{Label: "hot-b", KeyringRef: "ref-c"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 10, SevenDayPct: 97})
	_ = s.PutLimits(ctx, hotA.ID, provider.Limits{FiveHourPct: 85, SevenDayPct: 30})
	_ = s.PutLimits(ctx, hotB.ID, provider.Limits{FiveHourPct: 90, SevenDayPct: 20})
	immediateSwap(t, s)

	a, fsw, _ := withAutoSwapStubs(t, s)
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil {
		t.Fatalf("MaybeSwap: %v", err)
	}
	// Binding window is 7d → lowest 7d wins (hot-b at 20%), even though its
	// 5h (90%) is hotter than hot-a's (85%).
	if !swapped || len(fsw.switched) != 1 || fsw.switched[0] != "hot-b" {
		t.Errorf("want switch to hot-b (lowest 7d); swapped=%v switched=%v", swapped, fsw.switched)
	}
}

// An armed swap must NOT cancel when only the 5-hour window recovers — a
// weekly-capped account is still weekly-capped.
func TestAutoSwap_ArmedSwapSurvives5hRecoveryWhileWeeklyCapped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "ref-a"})
	low, _ := s.CreateAccount(ctx, store.Account{Label: "low", KeyringRef: "ref-l"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 95, SevenDayPct: 92})
	_ = s.PutLimits(ctx, low.ID, provider.Limits{FiveHourPct: 10, SevenDayPct: 10})
	_ = s.PutSetting(ctx, SettingsKeyAutoSwapGrace, "60")

	a, fsw, _ := withAutoSwapStubs(t, s)
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return clock }

	// Tick 1: arm.
	if swapped, _ := a.MaybeSwap(ctx, "active"); swapped {
		t.Fatalf("tick1 should arm, not swap")
	}
	// 5h window rolls over but the weekly cap remains.
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 20, SevenDayPct: 92})
	clock = clock.Add(90 * time.Second)

	// Tick 2: still weekly-capped — the armed swap must fire.
	swapped, err := a.MaybeSwap(ctx, "active")
	if err != nil || !swapped {
		t.Fatalf("weekly still capped — armed swap must fire: swapped=%v err=%v", swapped, err)
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "low" {
		t.Errorf("expected switch to low, got %v", fsw.switched)
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
	cand, found, _ := a.pickCandidate(ctx, active.ID, provider.Limits{FiveHourPct: 55}, window5h)
	if !found || cand.Label != "other" {
		t.Errorf("pickCandidate (active 5h=55, binding 5h): found=%v cand=%v want other", found, cand)
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
