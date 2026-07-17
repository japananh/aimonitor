package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sentryClient builds a client whose Sentry token is stored and whose default
// org is set, so handler tests exercise the org-resolution + auth paths.
func sentryClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	pointAPIsAt(t, srv)
	creds, _ := testCreds(t)
	if err := creds.Store(ServiceSentry, "sntry-tok"); err != nil {
		t.Fatal(err)
	}
	c := NewClient(creds)
	c.SentryOrg = "test-org"
	return c
}

// VerifySentry: a valid token lists the reachable orgs as the identity and
// sends the token as a Bearer credential.
func TestVerifySentry_Valid(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"slug": "gemcommerce", "name": "GemCommerce"},
			{"slug": "side-proj", "name": "Side Proj"},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	ident, err := VerifySentry(context.Background(), "sntry-good")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/organizations/" {
		t.Errorf("path = %s, want /organizations/", gotPath)
	}
	if gotAuth != "Bearer sntry-good" {
		t.Errorf("auth = %q, want Bearer sntry-good", gotAuth)
	}
	if !strings.Contains(ident, "gemcommerce") || !strings.Contains(ident, "side-proj") {
		t.Errorf("identity = %q, want both org slugs", ident)
	}
}

// VerifySentry: a 401 surfaces an actionable message.
func TestVerifySentry_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	_, err := VerifySentry(context.Background(), "sntry-bad")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want a 401 message", err)
	}
}

// VerifySentry: a valid token with zero orgs is treated as a scope problem.
func TestVerifySentry_NoOrgs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	_, err := VerifySentry(context.Background(), "sntry-noscopes")
	if err == nil || !strings.Contains(err.Error(), "no organization") {
		t.Fatalf("err = %v, want a no-org message", err)
	}
}

func TestSentryListProjects(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "41", "slug": "pricing-service", "name": "Pricing Service", "platform": "go", "environments": []string{"production"}},
		})
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	res, _, err := c.sentryListProjects(context.Background(), nil, sentryListProjectsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/organizations/test-org/projects/" {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer sntry-tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	body := resultJSON(t, res)
	if !strings.Contains(body, `"id": "41"`) || !strings.Contains(body, `"slug": "pricing-service"`) {
		t.Errorf("body missing project fields: %s", body)
	}
}

func TestSentrySearchIssues_Defaults(t *testing.T) {
	var gotQuery, gotSort, gotLimit, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotSort = r.URL.Query().Get("sort")
		gotLimit = r.URL.Query().Get("limit")
		// count is a STRING in Sentry's API — the row must preserve that.
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "12345", "shortId": "PRICING-SERVICE-9V",
			"title": "execute_usage_fee_billing failed", "culprit": "ExecuteUsageFeeBilling",
			"count": "348", "userCount": 3,
			"firstSeen": "2026-07-17T00:00:00Z", "lastSeen": "2026-07-17T08:00:00Z",
			"level": "error", "status": "unresolved", "permalink": "https://sentry.io/x/9V/",
			"project": map[string]any{"id": "41", "slug": "pricing-service", "name": "Pricing Service"},
		}})
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	res, _, err := c.sentrySearchIssues(context.Background(), nil, sentrySearchIssuesIn{})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/organizations/test-org/issues/" {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "is:unresolved" {
		t.Errorf("query = %q, want default is:unresolved", gotQuery)
	}
	if gotSort != "freq" {
		t.Errorf("sort = %q, want default freq", gotSort)
	}
	if gotLimit != "25" {
		t.Errorf("limit = %q, want default 25", gotLimit)
	}
	body := resultJSON(t, res)
	if !strings.Contains(body, `"count": "348"`) {
		t.Errorf("count not preserved as string: %s", body)
	}
	if !strings.Contains(body, `"userCount": 3`) || !strings.Contains(body, `"shortId": "PRICING-SERVICE-9V"`) {
		t.Errorf("body missing issue fields: %s", body)
	}
}

func TestSentrySearchIssues_InvalidSort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not hit the API on an invalid sort")
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	_, _, err := c.sentrySearchIssues(context.Background(), nil, sentrySearchIssuesIn{Sort: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid sort") {
		t.Fatalf("err = %v, want invalid sort", err)
	}
}

// A project slug is resolved to its numeric id (via /projects/) before it goes
// to the issues API as the `project` filter.
func TestSentrySearchIssues_ProjectSlugResolution(t *testing.T) {
	var gotProjectParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/organizations/test-org/projects/":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "41", "slug": "pricing-service", "name": "Pricing Service"},
			})
		case "/organizations/test-org/issues/":
			gotProjectParam = r.URL.Query().Get("project")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	_, _, err := c.sentrySearchIssues(context.Background(), nil, sentrySearchIssuesIn{Project: "pricing-service"})
	if err != nil {
		t.Fatal(err)
	}
	if gotProjectParam != "41" {
		t.Errorf("project param = %q, want resolved id 41", gotProjectParam)
	}
}

// A numeric project id skips resolution and passes straight through.
func TestSentrySearchIssues_ProjectNumericPassthrough(t *testing.T) {
	var gotProjectParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/organizations/test-org/projects/" {
			t.Error("numeric project must not trigger a /projects/ lookup")
		}
		gotProjectParam = r.URL.Query().Get("project")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	_, _, err := c.sentrySearchIssues(context.Background(), nil, sentrySearchIssuesIn{Project: "41"})
	if err != nil {
		t.Fatal(err)
	}
	if gotProjectParam != "41" {
		t.Errorf("project param = %q, want 41", gotProjectParam)
	}
}

func TestSentryGetIssue_ByNumericID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "12345", "shortId": "PRICING-SERVICE-9V", "title": "boom",
			"count": "348", "userCount": 3, "status": "unresolved",
			"metadata":     map[string]any{"title": "boom", "type": "RuntimeError", "value": "not implemented"},
			"firstRelease": map[string]any{"version": "app@1.2.3"},
		})
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	res, _, err := c.sentryGetIssue(context.Background(), nil, sentryGetIssueIn{Issue: "12345"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/organizations/test-org/issues/12345/" {
		t.Errorf("path = %s", gotPath)
	}
	body := resultJSON(t, res)
	if !strings.Contains(body, `"shortId": "PRICING-SERVICE-9V"`) ||
		!strings.Contains(body, `"value": "not implemented"`) ||
		!strings.Contains(body, `"version": "app@1.2.3"`) {
		t.Errorf("detail body missing fields: %s", body)
	}
}

// A shortId is resolved to its numeric id (via the issues search with
// shortIdLookup) before the detail fetch.
func TestSentryGetIssue_ByShortID(t *testing.T) {
	var searchQuery, shortIDLookup, detailPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/organizations/test-org/issues/":
			searchQuery = r.URL.Query().Get("query")
			shortIDLookup = r.URL.Query().Get("shortIdLookup")
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "12345", "shortId": "PRICING-SERVICE-9V"}})
		case "/organizations/test-org/issues/12345/":
			detailPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "12345", "shortId": "PRICING-SERVICE-9V", "title": "boom"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := sentryClient(t, srv)

	res, _, err := c.sentryGetIssue(context.Background(), nil, sentryGetIssueIn{Issue: "PRICING-SERVICE-9V"})
	if err != nil {
		t.Fatal(err)
	}
	if searchQuery != "PRICING-SERVICE-9V" || shortIDLookup != "1" {
		t.Errorf("lookup query=%q shortIdLookup=%q", searchQuery, shortIDLookup)
	}
	if detailPath != "/organizations/test-org/issues/12345/" {
		t.Errorf("detail path = %q", detailPath)
	}
	if body := resultJSON(t, res); !strings.Contains(body, `"title": "boom"`) {
		t.Errorf("body = %s", body)
	}
}

// With no org configured and none passed, the tool errors before any call.
func TestSentryOrg_Unset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not hit the API when org is unset")
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)
	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSentry, "sntry-tok")
	c := NewClient(creds) // no SentryOrg

	_, _, err := c.sentryListProjects(context.Background(), nil, sentryListProjectsIn{})
	if err == nil || !strings.Contains(err.Error(), "no Sentry organization") {
		t.Fatalf("err = %v, want no-org guidance", err)
	}
}

// A per-call organization_slug overrides the configured default.
func TestSentryOrg_Override(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()
	c := sentryClient(t, srv) // default org test-org

	_, _, err := c.sentryListProjects(context.Background(), nil, sentryListProjectsIn{OrganizationSlug: "other-org"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/organizations/other-org/projects/" {
		t.Errorf("path = %s, want override org", gotPath)
	}
}
