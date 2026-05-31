package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

func TestTokenRefresher_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "aimonitor/") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		if req["grant_type"] != "refresh_token" || req["refresh_token"] != "rtok" || req["client_id"] != ClaudeCodeClientID {
			t.Errorf("request body = %v", req)
		}
		_, _ = w.Write([]byte(`{
			"access_token":  "fresh-access",
			"refresh_token": "rotated-refresh",
			"expires_in":    3600,
			"scope":         "user:inference user:profile"
		}`))
	}))
	defer srv.Close()

	r := &TokenRefresher{HTTP: srv.Client(), TokenURL: srv.URL}
	tokens, err := r.Refresh(context.Background(), "rtok")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.AccessToken != "fresh-access" {
		t.Errorf("AccessToken = %q", tokens.AccessToken)
	}
	if tokens.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q want rotated-refresh", tokens.RefreshToken)
	}
	if len(tokens.Scopes) != 2 || tokens.Scopes[0] != "user:inference" {
		t.Errorf("Scopes = %v", tokens.Scopes)
	}
	if tokens.ExpiresAt.Before(time.Now().Add(50*time.Minute)) {
		t.Errorf("ExpiresAt should be ~1h ahead, got %v", tokens.ExpiresAt)
	}
}

func TestTokenRefresher_NoRotation(t *testing.T) {
	// Anthropic may omit refresh_token from the response when it's not
	// rotated. The refresher must keep the input value so the caller
	// can refresh again later.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer srv.Close()

	r := &TokenRefresher{HTTP: srv.Client(), TokenURL: srv.URL}
	tokens, err := r.Refresh(context.Background(), "original-rtok")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken != "original-rtok" {
		t.Errorf("RefreshToken = %q, want original-rtok (no rotation case)", tokens.RefreshToken)
	}
}

func TestTokenRefresher_RefreshExpired(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "invalid_grant", status)
			}))
			defer srv.Close()
			r := &TokenRefresher{HTTP: srv.Client(), TokenURL: srv.URL}
			_, err := r.Refresh(context.Background(), "expired-rtok")
			if !IsRefreshExpired(err) {
				t.Errorf("want TokenRefreshExpiredError, got %v", err)
			}
		})
	}
}

func TestTokenRefresher_Throttled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	r := &TokenRefresher{HTTP: srv.Client(), TokenURL: srv.URL}
	_, err := r.Refresh(context.Background(), "rtok")
	if !IsThrottledError(err) {
		t.Errorf("want UsageThrottledError, got %v", err)
	}
}

func TestTokenRefresher_EmptyInput(t *testing.T) {
	r := &TokenRefresher{}
	_, err := r.Refresh(context.Background(), "")
	if err == nil {
		t.Errorf("expected error for empty refresh token")
	}
}

func TestIsExpired(t *testing.T) {
	if IsExpired(time.Time{}) {
		t.Errorf("zero time should be treated as 'unknown, not expired'")
	}
	if IsExpired(time.Now().Add(1 * time.Hour)) {
		t.Errorf("1h in future should not be expired")
	}
	if !IsExpired(time.Now().Add(2 * time.Minute)) {
		t.Errorf("2m in future should be expired (within 5m buffer)")
	}
	if !IsExpired(time.Now().Add(-1 * time.Hour)) {
		t.Errorf("1h in past should be expired")
	}
}

func TestParseCredential_HappyPath(t *testing.T) {
	cred := provider.Credential{Bytes: []byte(`{
		"claudeAiOauth": {
			"accessToken": "sk-test",
			"refreshToken": "rtok",
			"expiresAt": 1748793600000,
			"scopes": ["user:inference"]
		}
	}`)}
	tokens, err := ParseCredential(cred)
	if err != nil {
		t.Fatalf("ParseCredential: %v", err)
	}
	if tokens.AccessToken != "sk-test" || tokens.RefreshToken != "rtok" {
		t.Errorf("tokens = %+v", tokens)
	}
	if tokens.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt should be parsed")
	}
	if len(tokens.Scopes) != 1 || tokens.Scopes[0] != "user:inference" {
		t.Errorf("Scopes = %v", tokens.Scopes)
	}
}

func TestParseCredential_PartialBlob(t *testing.T) {
	// Older Claude Code versions don't write expiresAt.
	cred := provider.Credential{Bytes: []byte(`{"claudeAiOauth":{"accessToken":"sk-x","refreshToken":"r"}}`)}
	tokens, err := ParseCredential(cred)
	if err != nil {
		t.Fatalf("ParseCredential: %v", err)
	}
	if !tokens.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt should be zero when blob omits it")
	}
}

func TestReplaceTokens_PreservesUnknownFields(t *testing.T) {
	// Future-proofing: if Anthropic adds a new field to the blob,
	// ReplaceTokens must not drop it.
	original := provider.Credential{Bytes: []byte(`{
		"claudeAiOauth": {
			"accessToken": "old",
			"refreshToken": "rtok",
			"subscriptionType": "pro",
			"someFutureField": ["a","b"]
		}
	}`)}
	fresh := CredentialTokens{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    time.UnixMilli(1748793600000),
	}
	out, err := ReplaceTokens(original, fresh)
	if err != nil {
		t.Fatalf("ReplaceTokens: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes, &doc); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	oauth := doc["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "new-access" {
		t.Errorf("accessToken not replaced: %v", oauth["accessToken"])
	}
	if oauth["subscriptionType"] != "pro" {
		t.Errorf("subscriptionType not preserved: %v", oauth["subscriptionType"])
	}
	if _, ok := oauth["someFutureField"]; !ok {
		t.Errorf("someFutureField dropped")
	}
}

func TestReplaceTokens_EmptyOriginal(t *testing.T) {
	// Fresh install case: ReplaceTokens should build a valid blob from
	// scratch when given no original.
	out, err := ReplaceTokens(provider.Credential{}, CredentialTokens{
		AccessToken:  "a",
		RefreshToken: "r",
	})
	if err != nil {
		t.Fatalf("ReplaceTokens(empty): %v", err)
	}
	tokens, err := ParseCredential(out)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if tokens.AccessToken != "a" || tokens.RefreshToken != "r" {
		t.Errorf("round-trip failed: %+v", tokens)
	}
}
