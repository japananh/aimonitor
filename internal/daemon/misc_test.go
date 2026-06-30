package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// TestCompactTokensInt covers every magnitude branch of the daily-summary
// token formatter.
func TestCompactTokensInt(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_500, "1.5K"},
		{2_500_000, "2.5M"},
		{3_200_000_000, "3.2B"},
	}
	for _, c := range cases {
		if got := compactTokensInt(c.n); got != c.want {
			t.Errorf("compactTokensInt(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestAutoSwitcher_SetConfig swaps the live config under the lock; a later
// snapshot reflects the new AutoSwitch toggle.
func TestAutoSwitcher_SetConfig(t *testing.T) {
	s := openStore(t)
	cfg := config.DefaultConfig()
	cfg.AutoSwitch = false
	a, err := NewAutoSwitcher(AutoSwitcherConfig{
		Store:    s,
		Provider: &fakeProvider{probes: map[int64]provider.RateLimit{}},
		Config:   cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.snapshot("").AutoSwitchEnabled {
		t.Fatalf("precondition: AutoSwitch should start false")
	}

	next := config.DefaultConfig()
	next.AutoSwitch = true
	a.SetConfig(next)

	if !a.snapshot("").AutoSwitchEnabled {
		t.Errorf("SetConfig did not update the live config")
	}
}

// TestNewSwitcher_Constructs builds a Switcher via the production constructor
// (no real keychain/network touched — it only wires a refresher and resolves
// the home dir for claude.json). Asserts the dependencies are wired.
func TestNewSwitcher_Constructs(t *testing.T) {
	s := openStore(t)
	p := &fakeProvider{}
	sw := NewSwitcher(s, p)
	if sw.Store != s || sw.Provider != p {
		t.Errorf("NewSwitcher did not wire Store/Provider")
	}
	if sw.Refresher == nil {
		t.Errorf("NewSwitcher should wire a token refresher")
	}
	// Sanity: the constructed switcher is usable for a no-account lookup.
	// Pin the advisory lock to a temp path so the test stays hermetic
	// (the empty default would resolve to the real $HOME/.aimonitor-lock).
	sw.LockPath = filepath.Join(t.TempDir(), ".aimonitor-lock")
	if err := sw.Switch(context.Background(), ""); err == nil {
		t.Errorf("Switch with an empty label should error")
	}
}

// TestSampleRecorder_ReportsResolverError: a resolveActive failure routes
// through report → onError, and Record drops the sample (no attribution).
func TestSampleRecorder_ReportsResolverError(t *testing.T) {
	s := openStore(t)
	var reported []error
	r := NewSampleRecorder(s,
		func(context.Context) (store.Account, bool, error) {
			return store.Account{}, false, errors.New("resolve boom")
		},
		func(err error) { reported = append(reported, err) },
	)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	r.clock = func() time.Time { return now }

	r.Record(context.Background(), SampleEvent{Sample: claude.Sample{
		Ts: now.Add(-time.Second), MessageID: "m1", InputTokens: 10,
	}})

	if len(reported) != 1 {
		t.Errorf("resolver error should be reported once, got %d", len(reported))
	}
}

// TestClearThrottle_StoreErrorIsLogged: ClearCooldown against a closed store
// fails; clearThrottle swallows it (best-effort, never panics).
func TestClearThrottle_StoreErrorIsLogged(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	_ = s.Close() // subsequent writes error

	// Must not panic; the error path is exercised and swallowed.
	clearThrottle(ctx, s, acct)
}
