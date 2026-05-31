package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/japananh/aimonitor/internal/version"
)

// TokenEndpoint is Anthropic's OAuth token endpoint. Overridable via
// the TokenRefresher.TokenURL field for tests.
const TokenEndpoint = "https://platform.claude.com/v1/oauth/token"

// ClaudeCodeClientID is the OAuth client_id Claude Code itself uses.
// Published in Claude Code's source / observed empirically; reusing it
// means aimonitor's refresh-token calls look indistinguishable from
// Claude Code's own (which is the truth — both apps are managing the
// same user's credentials).
const ClaudeCodeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// tokenRefreshTimeout bounds one Refresh() round-trip. The endpoint is
// fast in practice (single-digit seconds); 15 s absorbs cold-start
// latency on Anthropic's edge.
const tokenRefreshTimeout = 15 * time.Second

// expiryBuffer is how much earlier than the actual expiry we treat a
// token as "due for refresh". Prevents handing out a token that
// expires mid-request.
const expiryBuffer = 5 * time.Minute

// TokenRefresher exchanges a refresh_token for a fresh access_token via
// Anthropic's OAuth token endpoint. Construction takes an *http.Client
// so tests can swap a RoundTripper; production callers use NewTokenRefresher.
type TokenRefresher struct {
	HTTP     *http.Client
	TokenURL string
}

// NewTokenRefresher returns a refresher configured for the production
// Anthropic endpoint with tokenRefreshTimeout.
func NewTokenRefresher() *TokenRefresher {
	return &TokenRefresher{
		HTTP:     &http.Client{Timeout: tokenRefreshTimeout},
		TokenURL: TokenEndpoint,
	}
}

// Refresh exchanges refreshToken for a fresh CredentialTokens. The
// returned RefreshToken may equal the input (Anthropic doesn't always
// rotate it) or may be a brand-new value (rotation). Callers must
// persist whichever value comes back, not the one they passed in.
//
// Error classifications callers may want to discriminate:
//   - TokenRefreshExpiredError (401 or 400): refresh token is dead;
//     the user must re-authenticate from scratch via `aimonitor add`.
//   - UsageThrottledError (429): Anthropic rate-limited us. Back off.
//   - All other errors are transient and worth retrying.
func (r *TokenRefresher) Refresh(ctx context.Context, refreshToken string) (CredentialTokens, error) {
	if refreshToken == "" {
		return CredentialTokens{}, errors.New("token refresh: empty refresh_token")
	}

	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClaudeCodeClientID,
	})
	if err != nil {
		return CredentialTokens{}, fmt.Errorf("token refresh: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.TokenURL, bytes.NewReader(body))
	if err != nil {
		return CredentialTokens{}, fmt.Errorf("token refresh: build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "aimonitor/"+version.Version+" (+https://github.com/japananh/aimonitor)")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return CredentialTokens{}, fmt.Errorf("token refresh: HTTP: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		return CredentialTokens{}, &UsageThrottledError{Status: resp.StatusCode}
	}
	// 400 and 401 both indicate the refresh token itself is no longer
	// valid. Anthropic returns 400 when the token format is wrong /
	// expired, 401 when the auth server explicitly rejects it. Either
	// way the recovery is the same: re-login from scratch.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		return CredentialTokens{}, &TokenRefreshExpiredError{Status: resp.StatusCode}
	}
	if resp.StatusCode >= 400 {
		return CredentialTokens{}, fmt.Errorf("token refresh: HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return CredentialTokens{}, fmt.Errorf("token refresh: read body: %w", err)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CredentialTokens{}, fmt.Errorf("token refresh: decode body: %w", err)
	}
	if out.AccessToken == "" {
		return CredentialTokens{}, errors.New("token refresh: empty access_token in response")
	}

	t := CredentialTokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}
	if t.RefreshToken == "" {
		// Anthropic sometimes does and sometimes does not rotate the
		// refresh token. When omitted, keep the caller's original so
		// they can refresh again later.
		t.RefreshToken = refreshToken
	}
	if out.Scope != "" {
		t.Scopes = strings.Fields(out.Scope)
	}
	return t, nil
}

// IsExpired reports whether a token with the given expiry is within
// the refresh buffer (or already past). A zero expiry is treated as
// "unknown, not expired" so the caller doesn't unnecessarily refresh
// blobs that don't carry an expiresAt field.
func IsExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return time.Now().Add(expiryBuffer).After(expiresAt)
}

// TokenRefreshExpiredError signals that the refresh token itself is no
// longer valid. The user must re-authenticate from scratch.
type TokenRefreshExpiredError struct{ Status int }

func (e *TokenRefreshExpiredError) Error() string {
	return fmt.Sprintf("token refresh: %d (refresh token expired or revoked; user must re-login via `aimonitor add`)", e.Status)
}

// IsRefreshExpired reports whether err is a TokenRefreshExpiredError,
// recursing through wrapped errors via errors.As.
func IsRefreshExpired(err error) bool {
	var e *TokenRefreshExpiredError
	return errors.As(err, &e)
}
