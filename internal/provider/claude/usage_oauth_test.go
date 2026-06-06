package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

const goodToken = `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-fake"}}`

func TestUsageFetcher_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the request shape — anything missing here would
		// produce a real 4xx from Anthropic in production.
		if got := r.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-fake" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != UsageBetaHeader {
			t.Errorf("anthropic-beta = %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "aimonitor/") {
			t.Errorf("User-Agent = %q, want aimonitor/...", r.Header.Get("User-Agent"))
		}
		if r.URL.Path != UsageEndpoint {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"five_hour":  {"utilization": 75.2, "resets_at": "2026-05-31T23:59:59Z"},
			"seven_day":  {"utilization": 42.1, "resets_at": "2026-06-07T00:00:00Z"}
		}`))
	}))
	defer srv.Close()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	limits, err := f.FetchLimits(context.Background(), provider.Credential{Bytes: []byte(goodToken)})
	if err != nil {
		t.Fatalf("FetchLimits: %v", err)
	}
	if limits.FiveHourPct != 75.2 || limits.SevenDayPct != 42.1 {
		t.Errorf("percentages = (%v, %v) want (75.2, 42.1)", limits.FiveHourPct, limits.SevenDayPct)
	}
	if limits.FiveHourResetAt.IsZero() || limits.SevenDayResetAt.IsZero() {
		t.Errorf("reset times not parsed: %+v", limits)
	}
	if limits.Source != "oauth" {
		t.Errorf("Source = %q want oauth", limits.Source)
	}
	if limits.FetchedAt.IsZero() {
		t.Errorf("FetchedAt should be populated")
	}
}

func TestUsageFetcher_MissingFields(t *testing.T) {
	// Anthropic occasionally omits a window for accounts that haven't
	// consumed it — Go's zero values should cover this gracefully.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"five_hour": {"utilization": 12.5}}`))
	}))
	defer srv.Close()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	limits, err := f.FetchLimits(context.Background(), provider.Credential{Bytes: []byte(goodToken)})
	if err != nil {
		t.Fatalf("FetchLimits: %v", err)
	}
	if limits.FiveHourPct != 12.5 {
		t.Errorf("FiveHourPct = %v want 12.5", limits.FiveHourPct)
	}
	if !limits.SevenDayResetAt.IsZero() {
		t.Errorf("SevenDayResetAt should be zero when omitted, got %v", limits.SevenDayResetAt)
	}
}

func TestUsageFetcher_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := f.FetchLimits(context.Background(), provider.Credential{Bytes: []byte(goodToken)})
	if !IsAuthError(err) {
		t.Errorf("err = %v, want UsageAuthError", err)
	}
}

func TestUsageFetcher_Throttled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := f.FetchLimits(context.Background(), provider.Credential{Bytes: []byte(goodToken)})
	if !IsThrottledError(err) {
		t.Errorf("err = %v, want UsageThrottledError", err)
	}
	// No Retry-After header on this response → no usable hint.
	if d, ok := ThrottleRetryAfter(err); ok {
		t.Errorf("ThrottleRetryAfter = (%v, true), want (0, false) when header absent", d)
	}
}

func TestUsageFetcher_ThrottledRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := f.FetchLimits(context.Background(), provider.Credential{Bytes: []byte(goodToken)})
	if !IsThrottledError(err) {
		t.Fatalf("err = %v, want UsageThrottledError", err)
	}
	d, ok := ThrottleRetryAfter(err)
	if !ok || d != 120*time.Second {
		t.Errorf("ThrottleRetryAfter = (%v, %v), want (2m0s, true)", d, ok)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "120", 120 * time.Second},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"garbage", "soon", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRetryAfter(c.in); got != c.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
	// HTTP-date form: a time ~1 minute out should yield a positive,
	// roughly-correct wait. Allow slack for clock + parse latency.
	future := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 61*time.Second {
		t.Errorf("parseRetryAfter(date %q) = %v, want ~60s", future, got)
	}
	// A date in the past is not a usable wait.
	past := time.Now().Add(-60 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past %q) = %v, want 0", past, got)
	}
}

func TestUsageFetcher_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang past the context deadline.
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	f := &UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := f.FetchLimits(ctx, provider.Credential{Bytes: []byte(goodToken)})
	if err == nil {
		t.Fatalf("expected error from canceled context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		// Note: depending on Go version the wrap may surface as a url.Error
		// wrapping context.DeadlineExceeded; errors.Is unwraps both.
		t.Errorf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
}
