package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// fakeProvider plays the Claude provider for autoswitch tests. We avoid
// the real keychain layer by overriding the claude package-level
// stash functions (not directly — instead we side-step them with a
// custom Provider implementation that owns the keyring map).
//
// For v1's autoswitch tests we only need ActiveCredential,
// SetActiveCredential, and ProbeServerSide to behave deterministically.
type fakeProvider struct {
	mu              sync.Mutex
	active          provider.Credential
	probes          map[int64]provider.RateLimit // by account ID
	probeErr        map[int64]error
}

func (f *fakeProvider) Name() string { return claude.Name }
func (f *fakeProvider) LoadAccounts(_ context.Context) ([]provider.Account, error) {
	return nil, nil
}
func (f *fakeProvider) EstimateSessionUsage(_ context.Context, _ provider.Account) (provider.Usage, error) {
	return provider.Usage{}, nil
}
func (f *fakeProvider) ProbeServerSide(_ context.Context, acct provider.Account) (provider.RateLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.probeErr[acct.ID]; ok && err != nil {
		return provider.RateLimit{}, err
	}
	rl := f.probes[acct.ID]
	rl.AccountID = acct.ID
	rl.HTTPStatus = 200
	return rl, nil
}
func (f *fakeProvider) ActiveCredential(_ context.Context) (provider.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(f.active.Bytes))
	copy(cp, f.active.Bytes)
	return provider.Credential{Bytes: cp}, nil
}
func (f *fakeProvider) SetActiveCredential(_ context.Context, cred provider.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(cred.Bytes))
	copy(cp, cred.Bytes)
	f.active = provider.Credential{Bytes: cp}
	return nil
}
func (f *fakeProvider) OnboardingFlow(_ context.Context) (provider.Credential, error) {
	return provider.Credential{}, nil
}

func TestCrossedTripwire(t *testing.T) {
	ts := []int{40, 60, 100}
	cases := []struct {
		prev, cur float64
		want      int
	}{
		{0, 30, 0},
		{30, 41, 40},
		{45, 65, 60},   // crosses 60
		{65, 90, 0},    // no crossing
		{90, 105, 100}, // crosses 100
		{50, 39, 0},    // backwards motion, no crossing
		{0, 100, 40},   // huge jump returns the FIRST crossed (40) — confirm intent
	}
	for _, c := range cases {
		got := crossedTripwire(c.prev, c.cur, ts)
		if got != c.want {
			t.Errorf("crossedTripwire(%v, %v) = %d, want %d", c.prev, c.cur, got, c.want)
		}
	}
}

func TestAutoSwitcher_DisabledIsNoop(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_, _ = s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	_, _ = s.CreateAccount(ctx, store.Account{Label: "b", KeyringRef: "ref-b"})

	cfg := config.DefaultConfig() // AutoSwitch=false
	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   cfg,
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		a.OnSample(SampleEvent{
			Sample: claude.Sample{InputTokens: 300_000, OutputTokens: 200_000},
		})
	}
	// observedBudget should grow; lastPercent should grow.
	a.mu.Lock()
	if a.observedBudget < 1_500_000 {
		t.Errorf("budget should grow on samples; got %d", a.observedBudget)
	}
	if a.lastTripwireFired != 0 {
		t.Errorf("autoswitch disabled but tripwire fired: %d", a.lastTripwireFired)
	}
	a.mu.Unlock()
}

func TestAutoSwitcher_RespectsCooldown(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_, _ = s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	cfg := config.DefaultConfig()
	cfg.AutoSwitch = true
	cfg.AutoSwitchCooldownSeconds = 60

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }

	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   cfg,
		Clock:    clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.lastSwitchAt = now.Add(-30 * time.Second) // inside cool-down

	// Force a tripwire cross by pre-seeding lastPercent and observedBudget.
	a.mu.Lock()
	a.observedBudget = 1000
	a.lastPercent = 39
	a.mu.Unlock()
	a.OnSample(SampleEvent{Sample: claude.Sample{InputTokens: 50}}) // cur = (0+50)/1000 = 5%, not enough
	// Wait, we cleared usageSinceReset. Let me push more.

	a.mu.Lock()
	a.usageSinceReset = 390
	a.lastPercent = 39
	a.mu.Unlock()
	a.OnSample(SampleEvent{Sample: claude.Sample{InputTokens: 50}}) // 440 -> 44% crosses 40

	// Within cool-down: no audit row inserted, no switch.
	rows, _ := s.ListSwitchAudit(ctx, 10)
	if len(rows) != 0 {
		t.Errorf("expected no switch within cool-down; got %d rows", len(rows))
	}
	_ = ctx
}
