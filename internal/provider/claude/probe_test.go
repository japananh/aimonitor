package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// fakeTransport lets tests return canned responses with chosen headers.
type fakeTransport struct {
	status  int
	headers map[string]string
	err     error
	// observed by the test to confirm the probe sent the right request.
	calledWith *http.Request
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calledWith = r
	if f.err != nil {
		return nil, f.err
	}
	hdr := http.Header{}
	for k, v := range f.headers {
		hdr.Set(k, v)
	}
	return &http.Response{
		StatusCode: f.status,
		Header:     hdr,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    r,
	}, nil
}

func newTestProber(ft *fakeTransport) *Prober {
	return &Prober{
		BaseURL: "https://api.test",
		HTTP:    &http.Client{Transport: ft},
	}
}

func goodCred() provider.Credential {
	return provider.Credential{
		Bytes: []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-fake"}}`),
	}
}

func TestProbe_200Success(t *testing.T) {
	ft := &fakeTransport{
		status: 200,
		headers: map[string]string{
			"anthropic-ratelimit-tokens-remaining": "42000",
			"anthropic-ratelimit-tokens-reset":     "2026-05-13T13:00:00Z",
		},
	}
	rl, err := newTestProber(ft).Probe(context.Background(), goodCred())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rl.TokensRemaining != 42000 {
		t.Errorf("TokensRemaining = %d, want 42000", rl.TokensRemaining)
	}
	want := time.Date(2026, 5, 13, 13, 0, 0, 0, time.UTC)
	if !rl.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", rl.ResetAt, want)
	}
	if rl.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200", rl.HTTPStatus)
	}

	// Verify the request the probe made.
	r := ft.calledWith
	if r.URL.Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-fake" {
		t.Errorf("Authorization = %q", got)
	}
	if r.Header.Get("anthropic-version") == "" {
		t.Errorf("missing anthropic-version header")
	}
}

func TestProbe_401TokenDead(t *testing.T) {
	ft := &fakeTransport{status: 401}
	rl, err := newTestProber(ft).Probe(context.Background(), goodCred())
	if err == nil {
		t.Fatal("401: want error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %q", err.Error())
	}
	if rl.HTTPStatus != 401 {
		t.Errorf("HTTPStatus = %d, want 401", rl.HTTPStatus)
	}
}

func TestProbe_429RateLimited(t *testing.T) {
	ft := &fakeTransport{
		status: 429,
		headers: map[string]string{
			"anthropic-ratelimit-tokens-remaining": "0",
			"anthropic-ratelimit-tokens-reset":     "2026-05-13T14:00:00Z",
		},
	}
	rl, err := newTestProber(ft).Probe(context.Background(), goodCred())
	if err == nil {
		t.Fatal("429: want error, got nil")
	}
	// We DO want a populated rl on 429 — auto-switch needs the truth
	// that this account is exhausted to deprioritize it.
	if rl.TokensRemaining != 0 {
		t.Errorf("TokensRemaining = %d, want 0", rl.TokensRemaining)
	}
	if rl.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %d, want 429", rl.HTTPStatus)
	}
}

func TestProbe_NetworkError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("dial: connection refused")}
	_, err := newTestProber(ft).Probe(context.Background(), goodCred())
	if err == nil {
		t.Fatal("network error: want error, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err should wrap connection refused: %q", err.Error())
	}
}

func TestProbe_MalformedCredential(t *testing.T) {
	cases := map[string]provider.Credential{
		"empty":            {},
		"not JSON":         {Bytes: []byte("garbage")},
		"missing nested":   {Bytes: []byte(`{"other":"thing"}`)},
		"missing token":    {Bytes: []byte(`{"claudeAiOauth":{}}`)},
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newTestProber(&fakeTransport{status: 200}).Probe(context.Background(), cred)
			if err == nil {
				t.Errorf("want error for %s, got nil", name)
			}
		})
	}
}

func TestProbe_MissingHeadersAreTolerated(t *testing.T) {
	// Anthropic in theory could omit rate-limit headers (e.g. on a server
	// error). The probe should still return a usable RateLimit struct.
	ft := &fakeTransport{status: 200, headers: map[string]string{}}
	rl, err := newTestProber(ft).Probe(context.Background(), goodCred())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rl.TokensRemaining != 0 {
		t.Errorf("missing header: TokensRemaining should default to 0, got %d", rl.TokensRemaining)
	}
	if !rl.ResetAt.IsZero() {
		t.Errorf("missing header: ResetAt should be zero, got %v", rl.ResetAt)
	}
}

func TestExtractAccessToken_RoundTrip(t *testing.T) {
	got, err := extractAccessToken(goodCred())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "sk-ant-oat01-fake" {
		t.Errorf("got %q, want %q", got, "sk-ant-oat01-fake")
	}
}
