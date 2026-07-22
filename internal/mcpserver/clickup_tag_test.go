package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clickup_add_tag / clickup_remove_tag must hit ClickUp's
// POST|DELETE /task/{id}/tag/{tag_name} endpoints (PUT /task can't mutate tags),
// url-escaping both the task id and the tag name.
func TestClickupTag(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)
	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_x")
	c := NewClient(creds)

	res, _, err := c.clickupAddTag(context.Background(), nil, cuTagIn{TaskID: "T1", Tag: "back end"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/task/T1/tag/back%20end" {
		t.Errorf("add: got %s %s, want POST /task/T1/tag/back%%20end", gotMethod, gotPath)
	}
	if body := resultJSON(t, res); !strings.Contains(body, `"status": "added"`) || !strings.Contains(body, `"tag": "back end"`) {
		t.Errorf("add result misshaped: %s", body)
	}

	res2, _, err := c.clickupRemoveTag(context.Background(), nil, cuTagIn{TaskID: "T1", Tag: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/task/T1/tag/backend" {
		t.Errorf("remove: got %s %s, want DELETE /task/T1/tag/backend", gotMethod, gotPath)
	}
	if body := resultJSON(t, res2); !strings.Contains(body, `"status": "removed"`) {
		t.Errorf("remove result misshaped: %s", body)
	}
}

// Both tag tools must refuse a missing task_id or tag rather than send a
// malformed path.
func TestClickupTagRequiresArgs(t *testing.T) {
	c := NewClient(nil)
	if _, _, err := c.clickupAddTag(context.Background(), nil, cuTagIn{Tag: "x"}); err == nil {
		t.Error("add_tag accepted empty task_id")
	}
	if _, _, err := c.clickupRemoveTag(context.Background(), nil, cuTagIn{TaskID: "T1"}); err == nil {
		t.Error("remove_tag accepted empty tag")
	}
}
