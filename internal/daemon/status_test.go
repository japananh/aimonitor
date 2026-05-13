package daemon

import (
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
)

func TestAutoSwitcherSnapshot(t *testing.T) {
	s := openStore(t)
	cfg := config.DefaultConfig()
	cfg.AutoSwitch = true

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)

	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   cfg,
		Clock:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed accumulator fields directly. Bypasses OnSample because we
	// don't want crossedTripwire / evaluateAndSwitch to run.
	a.mu.Lock()
	a.usageSinceReset = 750
	a.observedBudget = 1000
	a.lastSwitchAt = now.Add(-30 * time.Second)
	a.mu.Unlock()

	st := a.snapshot("personal")
	if st.ActiveLabel != "personal" {
		t.Errorf("ActiveLabel = %q want personal", st.ActiveLabel)
	}
	if st.UsageSinceReset != 750 || st.ObservedBudget != 1000 {
		t.Errorf("usage/budget mismatch: %+v", st)
	}
	if st.SessionPercent != 75.0 {
		t.Errorf("SessionPercent = %v want 75", st.SessionPercent)
	}
	if !st.AutoSwitchEnabled {
		t.Errorf("AutoSwitchEnabled should be true")
	}
	if !st.PublishedAt.Equal(now) {
		t.Errorf("PublishedAt = %v want %v", st.PublishedAt, now)
	}
}

func TestAutoSwitcherSnapshot_ZeroBudget(t *testing.T) {
	s := openStore(t)
	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   config.DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.observedBudget = 0
	a.mu.Unlock()

	st := a.snapshot("")
	if st.SessionPercent != 0 {
		t.Errorf("expected 0%% when budget is 0; got %v", st.SessionPercent)
	}
}
