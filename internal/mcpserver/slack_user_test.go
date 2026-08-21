package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// slack_get_user flattens the nested profile: title, email, display_name and
// phone come up next to the top-level id/name/tz.
func TestSlackGetUser_FlattensProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.info" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"user": map[string]any{
				"id": "U1", "name": "violet", "real_name": "Violet Tran", "tz": "Asia/Bangkok",
				"profile": map[string]any{
					"title": "Backend Developer", "email": "violet@example.com",
					"display_name": "vio", "phone": "+84900000000",
				},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetUser(context.Background(), nil, slackGetUserIn{User: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	out := resultJSON(t, res)
	for _, want := range []string{
		`"title": "Backend Developer"`, `"email": "violet@example.com"`,
		`"display_name": "vio"`, `"phone": "+84900000000"`,
		`"tz": "Asia/Bangkok"`, `"real_name": "Violet Tran"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("user result missing %q in %s", want, out)
		}
	}
	// The nested object is flattened away, not passed through.
	if strings.Contains(out, `"profile"`) {
		t.Errorf("profile should be flattened, got %s", out)
	}
}

// A token without users:read.email (or a bot with no title) yields an empty
// profile — those keys drop out instead of being emitted blank.
func TestSlackGetUser_OmitsEmptyProfileFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"user": map[string]any{"id": "B1", "name": "buildbot", "is_bot": true},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetUser(context.Background(), nil, slackGetUserIn{User: "B1"})
	if err != nil {
		t.Fatal(err)
	}
	out := resultJSON(t, res)
	for _, unwanted := range []string{`"title"`, `"email"`, `"phone"`, `"tz"`, `"display_name"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("empty %s should be omitted, got %s", unwanted, out)
		}
	}
	if !strings.Contains(out, `"is_bot": true`) {
		t.Errorf("is_bot missing in %s", out)
	}
}

// users.list can leave the top-level real_name empty and only fill
// profile.real_name; the slim view falls back to it.
func TestSlackListUsers_ProfileFieldsAndRealNameFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"members": []map[string]any{
				{"id": "U1", "name": "violet", "tz": "Asia/Bangkok", "profile": map[string]any{
					"real_name": "Violet Tran", "title": "Backend Developer",
					"email": "violet@example.com", "display_name": "vio",
				}},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackListUsers(context.Background(), nil, slackListUsersIn{})
	if err != nil {
		t.Fatal(err)
	}
	out := resultJSON(t, res)
	for _, want := range []string{
		`"real_name": "Violet Tran"`, `"title": "Backend Developer"`,
		`"email": "violet@example.com"`, `"display_name": "vio"`, `"tz": "Asia/Bangkok"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("users result missing %q in %s", want, out)
		}
	}
}
