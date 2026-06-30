package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// VerifySlack: a valid user token (ok:true) yields the "user @ team" identity.
func TestVerifySlack_Valid(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "user": "violet", "team": "GemCommerce",
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	ident, err := VerifySlack(context.Background(), "xoxp-good")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/auth.test" {
		t.Errorf("path = %s, want /auth.test", gotPath)
	}
	if gotAuth != "Bearer xoxp-good" {
		t.Errorf("auth = %q, want Bearer xoxp-good", gotAuth)
	}
	if ident != "violet @ GemCommerce" {
		t.Errorf("identity = %q, want 'violet @ GemCommerce'", ident)
	}
}

// VerifySlack: an invalid token (ok:false) surfaces Slack's error clearly.
func TestVerifySlack_Invalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	_, err := VerifySlack(context.Background(), "xoxp-bad")
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("err = %v, want invalid_auth surfaced", err)
	}
}

// VerifyClickUp: a valid token returns the account identity (username + email)
// and sends the RAW token (no Bearer prefix) ClickUp expects.
func TestVerifyClickUp_Valid(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{"username": "violet", "email": "v@example.com"},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	ident, err := VerifyClickUp(context.Background(), "pk_1_TOK")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/user" {
		t.Errorf("path = %s, want /user", gotPath)
	}
	if gotAuth != "pk_1_TOK" {
		t.Errorf("auth = %q, want raw token (no Bearer)", gotAuth)
	}
	if ident != "violet (v@example.com)" {
		t.Errorf("identity = %q, want 'violet (v@example.com)'", ident)
	}
}

// VerifyClickUp: a 401 gives a pointed "check it starts with pk_" message;
// any other 4xx/5xx surfaces the HTTP status.
func TestVerifyClickUp_Errors(t *testing.T) {
	t.Run("401 → pointed pk_ hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		pointAPIsAt(t, srv)
		_, err := VerifyClickUp(context.Background(), "bad")
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "pk_") {
			t.Fatalf("err = %v, want 401 + pk_ hint", err)
		}
	})

	t.Run("500 → HTTP status surfaced", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		pointAPIsAt(t, srv)
		_, err := VerifyClickUp(context.Background(), "pk_x")
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("err = %v, want HTTP 500 surfaced", err)
		}
	})
}

// Verifier dispatches to the right verifier per service: Slack hits auth.test,
// ClickUp hits /user. One server, two calls.
func TestVerifier_Dispatch(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/auth.test":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": "u", "team": "t"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"username": "u"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	if _, err := Verifier(ServiceSlack)(context.Background(), "xoxp-x"); err != nil {
		t.Fatalf("slack verifier: %v", err)
	}
	if _, err := Verifier(ServiceClickUp)(context.Background(), "pk_x"); err != nil {
		t.Fatalf("clickup verifier: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/auth.test" || paths[1] != "/user" {
		t.Errorf("dispatched paths = %v, want [/auth.test /user]", paths)
	}
}

// ParseService normalizes valid names and rejects unknown ones.
func TestParseService(t *testing.T) {
	if svc, err := ParseService("  SLACK "); err != nil || svc != ServiceSlack {
		t.Errorf("ParseService(SLACK) = %q, %v; want slack", svc, err)
	}
	if svc, err := ParseService("ClickUp"); err != nil || svc != ServiceClickUp {
		t.Errorf("ParseService(ClickUp) = %q, %v; want clickup", svc, err)
	}
	if _, err := ParseService("jira"); err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("ParseService(jira) err = %v, want unknown service", err)
	}
}
