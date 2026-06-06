package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/version"
)

// UsageEndpoint is the Anthropic OAuth introspection endpoint that reports
// per-window utilization. Documented in claude-bar's reference
// implementation; observed to be stable across the oauth-2025-04-20 beta.
//
// This endpoint does NOT consume tokens or count against rate limits — it
// is pure introspection. That's why it is preferred over the legacy
// "send a 1-token request to /v1/messages and read response headers"
// probe path, which (a) burns real tokens and (b) creates traffic that
// Anthropic's abuse classifiers may flag.
const UsageEndpoint = "/api/oauth/usage"

// UsageBetaHeader gates access to /api/oauth/usage and several other
// OAuth-flow surfaces. Required; omitting it returns 404.
const UsageBetaHeader = "oauth-2025-04-20"

// usageHTTPTimeout bounds a single fetch. The endpoint typically responds
// in < 500 ms; 10 s absorbs cold-start latency on the Anthropic edge.
const usageHTTPTimeout = 10 * time.Second

// usageDTO is the JSON shape returned by /api/oauth/usage. Mirrors the
// fields claude-bar's reference implementation parses. Fields can be
// missing on a given response (e.g. an account that has never used the
// 7-day window may omit seven_day); Go's zero-value semantics make that
// safe to handle in the caller.
type usageDTO struct {
	FiveHour usageWindowDTO `json:"five_hour"`
	SevenDay usageWindowDTO `json:"seven_day"`
}

type usageWindowDTO struct {
	// Utilization is a percentage 0..100. Anthropic emits it as a JSON
	// number; we deserialize as float64 to preserve the fractional part
	// the API returns (the rendered bar usually rounds to an integer).
	Utilization float64 `json:"utilization"`

	// ResetsAt is the RFC3339 timestamp at which the window fully resets.
	// May be empty on accounts that have never consumed the window;
	// the caller treats time.Time{} as "unknown".
	ResetsAt string `json:"resets_at"`
}

// UsageFetcher issues GETs against /api/oauth/usage and parses the
// response into provider.Limits. Construction takes an *http.Client so
// tests can swap a RoundTripper; production callers use NewUsageFetcher.
type UsageFetcher struct {
	BaseURL string
	HTTP    *http.Client
}

// NewUsageFetcher returns a fetcher configured for the production
// Anthropic API with the package-level usageHTTPTimeout.
func NewUsageFetcher() *UsageFetcher {
	return &UsageFetcher{
		BaseURL: AnthropicAPIBase,
		HTTP:    &http.Client{Timeout: usageHTTPTimeout},
	}
}

// FetchLimits returns the current Limits for the credential. The Limits
// carries Source = "oauth" and FetchedAt = time.Now() on success.
//
// Errors fall into three categories the daemon treats differently:
//   - 401: token bad. Caller surfaces "re-auth needed" without retrying.
//   - 429: introspection itself was throttled (rare). Caller backs off.
//   - everything else: transient. Caller exponentially backs off.
func (f *UsageFetcher) FetchLimits(ctx context.Context, cred provider.Credential) (provider.Limits, error) {
	token, err := extractAccessToken(cred)
	if err != nil {
		return provider.Limits{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.BaseURL+UsageEndpoint, nil)
	if err != nil {
		return provider.Limits{}, fmt.Errorf("usage: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", AnthropicAPIVersion)
	req.Header.Set("anthropic-beta", UsageBetaHeader)
	req.Header.Set("Accept", "application/json")
	// Honest User-Agent: identifies aimonitor and points at the public
	// repo. Anthropic's abuse-triage team can recognise the traffic
	// pattern from a real, named tool rather than seeing anonymous polls.
	req.Header.Set("User-Agent", "aimonitor/"+version.Version+" (+https://github.com/japananh/aimonitor)")

	resp, err := f.HTTP.Do(req)
	if err != nil {
		return provider.Limits{}, fmt.Errorf("usage: HTTP: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return provider.Limits{}, &UsageAuthError{Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Anthropic documents a Retry-After header on 429 for the Messages
		// API; the OAuth introspection endpoint is undocumented, so it may
		// or may not send one. Parse it when present so the scheduler can
		// wait exactly as long as the server asks instead of guessing with
		// exponential backoff. RetryAfter == 0 means "no usable header".
		return provider.Limits{}, &UsageThrottledError{
			Status:     resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode >= 400 {
		return provider.Limits{}, fmt.Errorf("usage: HTTP %d", resp.StatusCode)
	}

	var dto usageDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		return provider.Limits{}, fmt.Errorf("usage: decode body: %w", err)
	}

	return provider.Limits{
		FiveHourPct:     dto.FiveHour.Utilization,
		FiveHourResetAt: parseResetTime(dto.FiveHour.ResetsAt),
		SevenDayPct:     dto.SevenDay.Utilization,
		SevenDayResetAt: parseResetTime(dto.SevenDay.ResetsAt),
		Source:          "oauth",
		FetchedAt:       time.Now(),
	}, nil
}

// parseResetTime returns the parsed time or zero. Empty / unparseable
// inputs are silently zero so the UI can hide the reset countdown
// rather than display "Invalid date".
func parseResetTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// UsageAuthError signals that the OAuth token is no longer accepted.
// Caller should prompt the user to re-authenticate rather than retry.
type UsageAuthError struct{ Status int }

func (e *UsageAuthError) Error() string {
	return fmt.Sprintf("usage: %d unauthorized (token expired or revoked)", e.Status)
}

// IsAuthError reports whether err is a UsageAuthError, recursing through
// wrapped errors via errors.As.
func IsAuthError(err error) bool {
	var ae *UsageAuthError
	return errors.As(err, &ae)
}

// UsageThrottledError signals that Anthropic rate-limited the
// introspection endpoint itself. Caller should back off aggressively.
// RetryAfter carries the server's Retry-After hint when the response
// included a parseable one; it is zero when the header was absent or
// unparseable, in which case the caller falls back to its own backoff.
type UsageThrottledError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *UsageThrottledError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("usage: %d throttled by Anthropic (retry-after %s)", e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("usage: %d throttled by Anthropic (no retry-after)", e.Status)
}

// parseRetryAfter interprets an HTTP Retry-After header, which RFC 7231
// allows in two forms: delay-seconds (an integer) or an HTTP-date. Returns
// 0 for an empty, negative, or unparseable value so the caller can tell
// "no usable hint" from a real duration.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Form 1: delay in whole seconds.
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	// Form 2: an absolute HTTP-date; the wait is until then.
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// IsThrottledError reports whether err is a UsageThrottledError.
func IsThrottledError(err error) bool {
	var te *UsageThrottledError
	return errors.As(err, &te)
}

// ThrottleRetryAfter returns the server-provided Retry-After hint carried
// by a throttle error, and true when one was present. Returns (0, false)
// when err is not a throttle error or the 429 carried no usable header —
// the caller then falls back to its own backoff schedule.
func ThrottleRetryAfter(err error) (time.Duration, bool) {
	var te *UsageThrottledError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		return te.RetryAfter, true
	}
	return 0, false
}
