package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// goodCred is a syntactically-valid Claude OAuth blob the fetcher can
// extract a token from. The token value doesn't matter — the test
// server doesn't verify, it just responds with a canned payload.
var goodCred = []byte(`{"claudeAiOauth":{"accessToken":"sk-test"}}`)

func TestUsageScheduler_TickOnce_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"five_hour":  {"utilization": 50.0, "resets_at": "2026-05-31T23:00:00Z"},
			"seven_day":  {"utilization": 20.0, "resets_at": "2026-06-07T00:00:00Z"}
		}`))
	}))
	defer srv.Close()

	st := openStore(t)
	acct, err := st.CreateAccount(context.Background(), store.Account{Label: "p", KeyringRef: "ref"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	fp := &fakeProvider{}
	fp.active = provider.Credential{Bytes: append([]byte(nil), goodCred...)}

	u := &UsageScheduler{
		Store:    st,
		Provider: fp,
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(_ context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
	}
	if err := u.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	got, err := st.GetLimits(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("GetLimits: %v", err)
	}
	if got.FiveHourPct != 50.0 || got.SevenDayPct != 20.0 {
		t.Errorf("limits = %+v", got)
	}
	if got.Source != "oauth" {
		t.Errorf("source = %q want oauth", got.Source)
	}
}

func TestUsageScheduler_TickOnce_NoActive(t *testing.T) {
	// When ResolveActive reports no active account, tickOnce returns nil
	// and never hits HTTP — verifies background daemon does not poll
	// Anthropic before the user has added any accounts.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	st := openStore(t)
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{},
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(_ context.Context) (store.Account, bool, error) {
			return store.Account{}, false, nil
		},
	}
	if err := u.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if called {
		t.Errorf("HTTP server should not have been called when no active account")
	}
}

func TestUsageScheduler_TickOnce_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer srv.Close()

	st := openStore(t)
	acct, _ := st.CreateAccount(context.Background(), store.Account{Label: "p", KeyringRef: "ref"})
	fp := &fakeProvider{}
	fp.active = provider.Credential{Bytes: append([]byte(nil), goodCred...)}

	u := &UsageScheduler{
		Store:    st,
		Provider: fp,
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(_ context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
	}
	err := u.tickOnce(context.Background())
	if !claude.IsAuthError(err) {
		t.Errorf("want UsageAuthError, got %v", err)
	}
}

// When RefreshActive is wired, a 401 on the first fetch must trigger a
// forced refresh and a retry rather than failing the tick — the recovery
// path that replaces the old permanent halt. The fake server 401s once
// then succeeds, standing in for "stale access token → refresh → valid".
func TestUsageScheduler_TickOnce_AuthError_RefreshRecovers(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{
			"five_hour": {"utilization": 33.0, "resets_at": "2026-05-31T23:00:00Z"},
			"seven_day": {"utilization": 10.0, "resets_at": "2026-06-07T00:00:00Z"}
		}`))
	}))
	defer srv.Close()

	st := openStore(t)
	acct, _ := st.CreateAccount(context.Background(), store.Account{Label: "p", KeyringRef: "ref"})
	fp := &fakeProvider{}
	fp.active = provider.Credential{Bytes: append([]byte(nil), goodCred...)}

	var forcedRefresh bool
	u := &UsageScheduler{
		Store:    st,
		Provider: fp,
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(_ context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
		RefreshActive: func(_ context.Context, _ store.Account, force bool) (provider.Credential, error) {
			if force {
				forcedRefresh = true
			}
			return provider.Credential{Bytes: append([]byte(nil), goodCred...)}, nil
		},
	}
	if err := u.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce should recover via refresh, got: %v", err)
	}
	if !forcedRefresh {
		t.Errorf("expected a forced refresh (force=true) after the 401")
	}
	got, err := st.GetLimits(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("GetLimits: %v", err)
	}
	if got.FiveHourPct != 33.0 {
		t.Errorf("limits after recovery = %+v, want 5h=33", got)
	}
}

// A dead refresh token surfaces as TokenRefreshExpiredError from the
// refresh attempt, which tickOnce must propagate unchanged so Run can
// classify it (back off hourly, never latch).
func TestUsageScheduler_TickOnce_RefreshExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer srv.Close()

	st := openStore(t)
	acct, _ := st.CreateAccount(context.Background(), store.Account{Label: "p", KeyringRef: "ref"})
	fp := &fakeProvider{}
	fp.active = provider.Credential{Bytes: append([]byte(nil), goodCred...)}

	u := &UsageScheduler{
		Store:    st,
		Provider: fp,
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(_ context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
		RefreshActive: func(_ context.Context, _ store.Account, force bool) (provider.Credential, error) {
			if force {
				return provider.Credential{}, &claude.TokenRefreshExpiredError{Status: http.StatusUnauthorized}
			}
			// Proactive read returns the (stale) live credential unchanged.
			return provider.Credential{Bytes: append([]byte(nil), goodCred...)}, nil
		},
	}
	err := u.tickOnce(context.Background())
	if !claude.IsRefreshExpired(err) {
		t.Errorf("want TokenRefreshExpiredError, got %v", err)
	}
}

func TestUsageScheduler_Jittered(t *testing.T) {
	u := &UsageScheduler{Jitter: 10 * time.Second}
	u.defaults()
	base := 60 * time.Second
	for i := 0; i < 100; i++ {
		got := u.jittered(base)
		if got < base-u.Jitter || got > base+u.Jitter {
			t.Fatalf("jittered(60s)=%v outside [50s, 70s]", got)
		}
	}
}

func TestUsageScheduler_Jittered_MinClamp(t *testing.T) {
	// A pathologically short base + a wide jitter should clamp at 1s
	// rather than producing zero/negative durations.
	u := &UsageScheduler{Jitter: 10 * time.Second}
	u.defaults()
	for i := 0; i < 100; i++ {
		got := u.jittered(100 * time.Millisecond)
		if got < time.Second {
			t.Fatalf("jittered should clamp to >= 1s, got %v", got)
		}
	}
}

func TestDoubleCapped(t *testing.T) {
	cases := []struct {
		in, cap, want time.Duration
	}{
		{300 * time.Second, 1 * time.Hour, 600 * time.Second},
		{30 * time.Minute, 1 * time.Hour, 1 * time.Hour},
		{45 * time.Minute, 1 * time.Hour, 1 * time.Hour},
	}
	for _, c := range cases {
		got := doubleCapped(c.in, c.cap)
		if got != c.want {
			t.Errorf("doubleCapped(%v, %v) = %v want %v", c.in, c.cap, got, c.want)
		}
	}
}

// successInterval must speed up not only when the active account is near its
// limit (pct >= SpeedupAtPct) but also whenever a swap is armed — even below
// the threshold — so the grace deadline isn't a full baseline interval late.
// Error/backoff paths never call this, so a pending swap can't undercut a 429
// backoff (that precedence lives in Run's switch).
func TestUsageScheduler_SuccessInterval(t *testing.T) {
	u := &UsageScheduler{}
	u.defaults() // SpeedupAtPct=90, Baseline=300s, SpeedupInterval=60s

	cases := []struct {
		name    string
		pct     float64
		known   bool
		pending bool
		want    time.Duration
	}{
		{"below threshold, no pending", 50, true, false, u.Baseline},
		{"below threshold, pending", 50, true, true, u.SpeedupInterval},
		{"at/above threshold, no pending", 95, true, false, u.SpeedupInterval},
		{"at/above threshold, pending", 95, true, true, u.SpeedupInterval},
		{"pct unknown, no pending", 0, false, false, u.Baseline},
		{"pct unknown, pending", 0, false, true, u.SpeedupInterval},
	}
	for _, c := range cases {
		if got := u.successInterval(c.pct, c.known, c.pending); got != c.want {
			t.Errorf("%s: successInterval(%v,%v,%v) = %v, want %v",
				c.name, c.pct, c.known, c.pending, got, c.want)
		}
	}
}
