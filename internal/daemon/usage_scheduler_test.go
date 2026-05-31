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
