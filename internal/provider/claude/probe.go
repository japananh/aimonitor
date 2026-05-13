package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// AnthropicAPIBase is the base URL the probe targets. Overridable via
// AIMONITOR_ANTHROPIC_API for tests / staging.
const AnthropicAPIBase = "https://api.anthropic.com"

// AnthropicAPIVersion is the value of the `anthropic-version` header. The
// probe doesn't depend on a specific API version's response shape (it
// only reads rate-limit headers + HTTP status), but we send a value
// because Anthropic rejects requests that omit it.
const AnthropicAPIVersion = "2023-06-01"

// ProbeModel is the cheap model used for the probe. Haiku costs roughly
// one cent per million input tokens and resets the same session-quota
// counter that the user's main session does — exactly the signal we want.
const ProbeModel = "claude-haiku-4-5-20251001"

// Prober issues a 1-token request against /v1/messages and reads the
// rate-limit response headers. Construction takes an *http.Client so
// tests can swap in a RoundTripper; production callers use NewProber()
// which wires http.DefaultClient.
type Prober struct {
	BaseURL string
	HTTP    *http.Client
}

// NewProber returns a Prober configured for the production Anthropic API.
func NewProber() *Prober {
	return &Prober{
		BaseURL: AnthropicAPIBase,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

type oauthBlob struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

// extractAccessToken pulls the OAuth access token out of a Claude Code
// credential blob. Returns a clear error when the blob is the wrong shape
// — most likely cause is a corrupted Keychain entry or a future
// Claude Code release that changed the schema.
func extractAccessToken(cred provider.Credential) (string, error) {
	if len(cred.Bytes) == 0 {
		return "", errors.New("extractAccessToken: empty credential")
	}
	var b oauthBlob
	if err := json.Unmarshal(cred.Bytes, &b); err != nil {
		return "", fmt.Errorf("extractAccessToken: parse credential JSON: %w", err)
	}
	if b.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("extractAccessToken: credential has no claudeAiOauth.accessToken field")
	}
	return b.ClaudeAiOauth.AccessToken, nil
}

// Probe sends one tiny request and parses the rate-limit headers. The
// returned RateLimit is populated even on HTTP 429 (the server tells us
// we're rate-limited but still emits the remaining-tokens header, which
// is exactly the answer 'this account is exhausted').
//
// Network errors and 401s surface as Go errors — the caller treats those
// as 'candidate unusable' and skips them in auto-switch decisions.
func (p *Prober) Probe(ctx context.Context, cred provider.Credential) (provider.RateLimit, error) {
	token, err := extractAccessToken(cred)
	if err != nil {
		return provider.RateLimit{}, err
	}

	body := map[string]any{
		"model":      ProbeModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "."}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return provider.RateLimit{}, fmt.Errorf("probe: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return provider.RateLimit{}, fmt.Errorf("probe: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", AnthropicAPIVersion)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	// User-agent helps Anthropic's support distinguish aimonitor probes
	// from regular Claude Code traffic on their side if they ever look.
	req.Header.Set("User-Agent", "aimonitor-probe/1.0")

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return provider.RateLimit{}, fmt.Errorf("probe: HTTP: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	// 401: the token is bad. Don't return a RateLimit; caller treats this
	// as 'candidate is dead, remove from rotation'.
	if resp.StatusCode == http.StatusUnauthorized {
		return provider.RateLimit{HTTPStatus: resp.StatusCode}, fmt.Errorf("probe: 401 unauthorized (token expired or revoked)")
	}

	rl := parseRateLimitHeaders(resp.Header, resp.StatusCode)
	rl.ProbedAt = time.Now()

	// 429: rate-limited. We still return rl (with TokensRemaining likely 0
	// or low), and a non-nil error so the auto-switcher knows to deprioritize.
	if resp.StatusCode == http.StatusTooManyRequests {
		return rl, fmt.Errorf("probe: 429 rate-limited (this candidate is exhausted)")
	}
	// Other 4xx/5xx: surface as error but still return whatever headers
	// we got (Anthropic always emits rate-limit headers).
	if resp.StatusCode >= 400 {
		return rl, fmt.Errorf("probe: HTTP %d", resp.StatusCode)
	}
	return rl, nil
}

// parseRateLimitHeaders extracts the three Anthropic rate-limit headers
// from an HTTP response, tolerating any of them being missing.
func parseRateLimitHeaders(h http.Header, status int) provider.RateLimit {
	rl := provider.RateLimit{HTTPStatus: status}

	if v := h.Get("anthropic-ratelimit-tokens-remaining"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			rl.TokensRemaining = n
		}
	}
	if v := h.Get("anthropic-ratelimit-tokens-reset"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			rl.ResetAt = t
		}
	}
	return rl
}
