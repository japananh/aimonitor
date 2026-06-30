package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// cuClient is a small helper: a httptest server with the given handler, both
// (all three) API bases pointed at it, and a ready Client whose ClickUp token
// is stored. Returns the client so each test drives a real handler call.
func cuClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	pointAPIsAt(t, srv)
	creds, _ := testCreds(t)
	if err := creds.Store(ServiceClickUp, "pk_1_TOK"); err != nil {
		t.Fatal(err)
	}
	return NewClient(creds)
}

// --- ClickUp hierarchy handlers (all were 0%) ---------------------------

// Each ClickUp handler must hit the right v2 path with the RAW token in
// Authorization (no Bearer prefix) and surface the slimmed result. One
// dispatch server keeps every assertion in one place.
func TestClickUpHierarchyHandlers(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		switch r.URL.Path {
		case "/team":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"teams": []map[string]any{{
					"id": "T1", "name": "GemCommerce",
					"members": []map[string]any{
						{"user": map[string]any{"id": 11, "username": "violet", "email": "v@x.io"}},
					},
				}},
			})
		case "/team/T1/space":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"spaces": []map[string]any{{"id": "S1", "name": "Eng", "private": false}},
			})
		case "/space/S1/folder":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"folders": []map[string]any{{
					"id": "F1", "name": "Sprints",
					"lists": []map[string]any{{"id": "L1", "name": "Sprint 1"}},
				}},
			})
		case "/folder/F1/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"lists": []map[string]any{{"id": "L1", "name": "Sprint 1", "task_count": 4}},
			})
		case "/space/S1/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"lists": []map[string]any{{"id": "L9", "name": "Folderless", "task_count": 1}},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	t.Run("list_workspaces", func(t *testing.T) {
		res, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/team" || gotMethod != http.MethodGet {
			t.Errorf("path/method = %s %s", gotMethod, gotPath)
		}
		if gotAuth != "pk_1_TOK" {
			t.Errorf("auth = %q, want raw token (no Bearer)", gotAuth)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "GemCommerce") || !strings.Contains(out, "T1") {
			t.Errorf("workspaces missing: %s", out)
		}
	})

	t.Run("list_spaces", func(t *testing.T) {
		res, _, err := c.clickupListSpaces(context.Background(), nil, cuWorkspaceIn{WorkspaceID: "T1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/team/T1/space" {
			t.Errorf("path = %s", gotPath)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "Eng") {
			t.Errorf("spaces missing Eng: %s", out)
		}
	})

	t.Run("list_folders", func(t *testing.T) {
		res, _, err := c.clickupListFolders(context.Background(), nil, cuSpaceIn{SpaceID: "S1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/space/S1/folder" {
			t.Errorf("path = %s", gotPath)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "Sprints") || !strings.Contains(out, "Sprint 1") {
			t.Errorf("folders missing: %s", out)
		}
	})

	t.Run("list_lists by folder", func(t *testing.T) {
		res, _, err := c.clickupListLists(context.Background(), nil, cuListListsIn{FolderID: "F1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/folder/F1/list" {
			t.Errorf("path = %s, want /folder/F1/list", gotPath)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "Sprint 1") {
			t.Errorf("lists missing: %s", out)
		}
	})

	t.Run("list_lists by space (folderless)", func(t *testing.T) {
		res, _, err := c.clickupListLists(context.Background(), nil, cuListListsIn{SpaceID: "S1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/space/S1/list" {
			t.Errorf("path = %s, want /space/S1/list", gotPath)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "Folderless") {
			t.Errorf("folderless lists missing: %s", out)
		}
	})

	t.Run("list_members filters by workspace id", func(t *testing.T) {
		res, _, err := c.clickupListMembers(context.Background(), nil, cuWorkspaceIn{WorkspaceID: "T1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/team" {
			t.Errorf("path = %s, want /team (members ride on GET /team)", gotPath)
		}
		out := resultJSON(t, res)
		for _, want := range []string{"violet", "v@x.io", "11"} {
			if !strings.Contains(out, want) {
				t.Errorf("members missing %q: %s", want, out)
			}
		}
	})
}

// clickup_list_lists with neither folder_id nor space_id errors before any
// HTTP call.
func TestClickUpListLists_RequiresFolderOrSpace(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not make an HTTP call, hit %s", r.URL.Path)
	})
	if _, _, err := c.clickupListLists(context.Background(), nil, cuListListsIn{}); err == nil ||
		!strings.Contains(err.Error(), "folder_id or space_id") {
		t.Fatalf("err = %v, want folder_id or space_id guard", err)
	}
}

// list_members must skip teams whose id doesn't match (returns empty members).
func TestClickUpListMembers_NoMatchingWorkspace(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"teams": []map[string]any{{"id": "OTHER", "members": []map[string]any{
				{"user": map[string]any{"id": 1, "username": "x"}},
			}}},
		})
	})
	res, _, err := c.clickupListMembers(context.Background(), nil, cuWorkspaceIn{WorkspaceID: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	if out := resultJSON(t, res); strings.Contains(out, "\"x\"") {
		t.Errorf("non-matching workspace must yield no members: %s", out)
	}
}

// --- ClickUp tasks: full-param coverage + write handlers ----------------

// list_tasks with every optional param set threads statuses[], include_closed,
// subtasks, and page into the query (the unset branches were the missing lines).
func TestClickUpListTasks_AllParams(t *testing.T) {
	var gotQuery url.Values
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
			{"id": "t1", "name": "T"},
		}})
	})
	_, _, err := c.clickupListTasks(context.Background(), nil, cuListTasksIn{
		ListID: "L1", Statuses: []string{"open", "in progress"},
		IncludeClosed: true, IncludeSubtasks: true, Page: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gotQuery["statuses[]"]; len(got) != 2 || got[0] != "open" {
		t.Errorf("statuses[] = %v, want [open in progress]", got)
	}
	if gotQuery.Get("include_closed") != "true" {
		t.Errorf("include_closed = %q", gotQuery.Get("include_closed"))
	}
	if gotQuery.Get("subtasks") != "true" {
		t.Errorf("subtasks = %q", gotQuery.Get("subtasks"))
	}
	if gotQuery.Get("page") != "2" {
		t.Errorf("page = %q, want 2", gotQuery.Get("page"))
	}
}

// search_tasks with every optional param threads assignees[], statuses[],
// include_closed, subtasks, date_updated_gt, and page.
func TestClickUpSearchTasks_AllParams(t *testing.T) {
	var gotQuery url.Values
	var gotPath string
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotPath = r.URL.Query(), r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
			{"id": "t1", "name": "Found"},
		}})
	})
	res, _, err := c.clickupSearchTasks(context.Background(), nil, cuSearchTasksIn{
		WorkspaceID: "T1", AssigneeIDs: []string{"11", "22"},
		Statuses: []string{"open"}, IncludeClosed: true, IncludeSubtasks: true,
		UpdatedAfter: "1700000000000", Page: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/team/T1/task" {
		t.Errorf("path = %s, want /team/T1/task", gotPath)
	}
	if got := gotQuery["assignees[]"]; len(got) != 2 || got[1] != "22" {
		t.Errorf("assignees[] = %v", got)
	}
	if gotQuery.Get("statuses[]") != "open" {
		t.Errorf("statuses[] = %q", gotQuery.Get("statuses[]"))
	}
	if gotQuery.Get("include_closed") != "true" || gotQuery.Get("subtasks") != "true" {
		t.Errorf("flags = %v", gotQuery)
	}
	if gotQuery.Get("date_updated_gt") != "1700000000000" {
		t.Errorf("date_updated_gt = %q", gotQuery.Get("date_updated_gt"))
	}
	if gotQuery.Get("page") != "3" {
		t.Errorf("page = %q, want 3", gotQuery.Get("page"))
	}
	if out := resultJSON(t, res); !strings.Contains(out, "Found") {
		t.Errorf("result missing task: %s", out)
	}
}

// create_task POSTs to /list/{id}/task with every field mapped (markdown_description,
// status, priority, assignees, due_date, tags, parent) and slims the response.
func TestClickUpCreateTask_AllFields(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "NEW", "name": "Built"})
	})
	res, _, err := c.clickupCreateTask(context.Background(), nil, cuCreateTaskIn{
		ListID: "L1", Name: "Built", Description: "do it", Status: "open",
		Priority: 2, Assignees: []int{11}, DueDate: 1700000000000,
		Tags: []string{"bug"}, Parent: "PARENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/list/L1/task" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["markdown_description"] != "do it" || gotBody["status"] != "open" {
		t.Errorf("body desc/status = %v", gotBody)
	}
	if gotBody["priority"].(float64) != 2 || gotBody["parent"] != "PARENT" {
		t.Errorf("body priority/parent = %v", gotBody)
	}
	if _, ok := gotBody["assignees"]; !ok {
		t.Errorf("assignees missing: %v", gotBody)
	}
	if gotBody["due_date"].(float64) != 1700000000000 {
		t.Errorf("due_date = %v", gotBody["due_date"])
	}
	if out := resultJSON(t, res); !strings.Contains(out, "created") || !strings.Contains(out, "Built") {
		t.Errorf("result missing created/Built: %s", out)
	}
}

// create_task with only the required fields omits the optional body keys.
func TestClickUpCreateTask_MinimalOmitsOptionals(t *testing.T) {
	var gotBody map[string]any
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "N", "name": "Bare"})
	})
	if _, _, err := c.clickupCreateTask(context.Background(), nil, cuCreateTaskIn{
		ListID: "L1", Name: "Bare",
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"markdown_description", "status", "priority", "assignees", "due_date", "tags", "parent"} {
		if _, ok := gotBody[k]; ok {
			t.Errorf("optional key %q must be absent when unset: %v", k, gotBody)
		}
	}
	if gotBody["name"] != "Bare" {
		t.Errorf("name = %v", gotBody["name"])
	}
}

// update_task PUTs to /task/{id} with the mapped fields.
func TestClickUpUpdateTask_AllFields(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "T1", "name": "Renamed"})
	})
	res, _, err := c.clickupUpdateTask(context.Background(), nil, cuUpdateTaskIn{
		TaskID: "T1", Name: "Renamed", Description: "new", Status: "done",
		Priority: 1, DueDate: 1700000000001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/task/T1" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["name"] != "Renamed" || gotBody["markdown_description"] != "new" || gotBody["status"] != "done" {
		t.Errorf("body = %v", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "updated") || !strings.Contains(out, "Renamed") {
		t.Errorf("result missing updated/Renamed: %s", out)
	}
}

// update_task with no fields errors before any HTTP call.
func TestClickUpUpdateTask_NothingToUpdate(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not make HTTP call, hit %s", r.URL.Path)
	})
	if _, _, err := c.clickupUpdateTask(context.Background(), nil, cuUpdateTaskIn{TaskID: "T1"}); err == nil ||
		!strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v, want nothing-to-update guard", err)
	}
}

// --- ClickUp comments ----------------------------------------------------

// add_comment POSTs to /task/{id}/comment and returns the new comment id.
func TestClickUpAddComment(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "CM1"})
	})
	res, _, err := c.clickupAddComment(context.Background(), nil, cuAddCommentIn{
		TaskID: "T1", Comment: "looks good",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/task/T1/comment" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["comment_text"] != "looks good" {
		t.Errorf("comment_text = %v", gotBody["comment_text"])
	}
	if out := resultJSON(t, res); !strings.Contains(out, "CM1") || !strings.Contains(out, "posted") {
		t.Errorf("result missing CM1/posted: %s", out)
	}
}

// list_comments GETs /task/{id}/comment and slims each to id/text/by/date.
func TestClickUpListComments(t *testing.T) {
	var gotPath string
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"comments": []map[string]any{
				{"id": "CM1", "comment_text": "first comment",
					"user": map[string]any{"username": "violet"}, "date": "1700000000000"},
			},
		})
	})
	res, _, err := c.clickupListComments(context.Background(), nil, cuTaskIn{TaskID: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/task/T1/comment" {
		t.Errorf("path = %s, want /task/T1/comment", gotPath)
	}
	out := resultJSON(t, res)
	for _, want := range []string{"CM1", "first comment", "violet", "1700000000000"} {
		if !strings.Contains(out, want) {
			t.Errorf("comments missing %q: %s", want, out)
		}
	}
}

// delete_comment DELETEs /comment/{id} and reports deleted.
func TestClickUpDeleteComment(t *testing.T) {
	var gotMethod, gotPath string
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	res, _, err := c.clickupDeleteComment(context.Background(), nil, cuDeleteCommentIn{CommentID: "CM1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/comment/CM1" {
		t.Errorf("method/path = %s %s, want DELETE /comment/CM1", gotMethod, gotPath)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "deleted") || !strings.Contains(out, "CM1") {
		t.Errorf("result missing deleted/CM1: %s", out)
	}
}

// --- ClickUp attachments -------------------------------------------------

// upload_attachment POSTs multipart to /task/{id}/attachment with the raw
// token and surfaces the attachment id + url.
func TestClickUpUploadAttachment(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotCType string
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCType = r.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ATT1", "url": "https://clickup.com/att/ATT1", "title": "errors.csv",
		})
	})
	res, _, err := c.clickupUploadAttachment(context.Background(), nil, cuUploadAttachmentIn{
		TaskID: "T1", Filename: "errors.csv", Content: "a,b\n1,2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/task/T1/attachment" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "pk_1_TOK" {
		t.Errorf("auth = %q, want raw token", gotAuth)
	}
	if !strings.HasPrefix(gotCType, "multipart/form-data; boundary=") {
		t.Errorf("content-type = %q, want multipart", gotCType)
	}
	out := resultJSON(t, res)
	for _, want := range []string{"attached", "errors.csv", "ATT1", "https://clickup.com/att/ATT1"} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %q: %s", want, out)
		}
	}
}

// upload_attachment validates all three fields before any HTTP call.
func TestClickUpUploadAttachment_Validation(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not make HTTP call, hit %s", r.URL.Path)
	})
	cases := []cuUploadAttachmentIn{
		{Filename: "f", Content: "x"}, // missing task_id
		{TaskID: "T1", Content: "x"},  // missing filename
		{TaskID: "T1", Filename: "f"}, // missing content
	}
	for _, in := range cases {
		if _, _, err := c.clickupUploadAttachment(context.Background(), nil, in); err == nil ||
			!strings.Contains(err.Error(), "required") {
			t.Errorf("in=%+v err=%v, want required-fields guard", in, err)
		}
	}
}

// --- ClickUp docs (v3) — proves the v3 base redirect took effect ---------

func TestClickUpDocHandlers(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotQuery url.Values
	var gotBody map[string]any
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		gotQuery = r.URL.Query()
		gotBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
		}
		switch {
		case r.URL.Path == "/workspaces/T1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"docs":        []map[string]any{{"id": "D1", "name": "Runbook"}},
				"next_cursor": "NEXT",
			})
		case r.URL.Path == "/workspaces/T1/docs" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "D2", "name": "New Doc"})
		case r.URL.Path == "/workspaces/T1/docs/D1/pages" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "P1", "name": "Intro", "content": "# Hello"},
			})
		case r.URL.Path == "/workspaces/T1/docs/D1/pages" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "P9", "name": "Added"})
		case r.URL.Path == "/workspaces/T1/docs/D1/pages/P1" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "P1", "name": "Intro", "content": "# Body"})
		case r.URL.Path == "/workspaces/T1/docs/D1/pages/P1" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	t.Run("list_docs threads cursor", func(t *testing.T) {
		res, _, err := c.clickupListDocs(context.Background(), nil, cuListDocsIn{WorkspaceID: "T1", Cursor: "CUR"})
		if err != nil {
			t.Fatal(err)
		}
		if gotAuth != "pk_1_TOK" {
			t.Errorf("v3 auth = %q, want raw token", gotAuth)
		}
		if gotQuery.Get("next_cursor") != "CUR" {
			t.Errorf("next_cursor query = %q", gotQuery.Get("next_cursor"))
		}
		out := resultJSON(t, res)
		if !strings.Contains(out, "Runbook") || !strings.Contains(out, "NEXT") {
			t.Errorf("docs result missing: %s", out)
		}
	})

	t.Run("get_doc requests markdown pages", func(t *testing.T) {
		res, _, err := c.clickupGetDoc(context.Background(), nil, cuDocIn{WorkspaceID: "T1", DocID: "D1"})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/workspaces/T1/docs/D1/pages" {
			t.Errorf("path = %s", gotPath)
		}
		if gotQuery.Get("content_format") != "text/md" {
			t.Errorf("content_format = %q, want text/md", gotQuery.Get("content_format"))
		}
		if out := resultJSON(t, res); !strings.Contains(out, "# Hello") || !strings.Contains(out, "Intro") {
			t.Errorf("get_doc result missing: %s", out)
		}
	})

	t.Run("create_doc", func(t *testing.T) {
		res, _, err := c.clickupCreateDoc(context.Background(), nil, cuCreateDocIn{WorkspaceID: "T1", Name: "New Doc"})
		if err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodPost || gotPath != "/workspaces/T1/docs" {
			t.Errorf("method/path = %s %s", gotMethod, gotPath)
		}
		if gotBody["name"] != "New Doc" {
			t.Errorf("body name = %v", gotBody["name"])
		}
		if out := resultJSON(t, res); !strings.Contains(out, "created_doc") || !strings.Contains(out, "D2") {
			t.Errorf("create_doc result missing: %s", out)
		}
	})

	t.Run("create_page with parent", func(t *testing.T) {
		res, _, err := c.clickupCreatePage(context.Background(), nil, cuCreatePageIn{
			WorkspaceID: "T1", DocID: "D1", Name: "Added", Content: "body", ParentPageID: "P0",
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodPost || gotPath != "/workspaces/T1/docs/D1/pages" {
			t.Errorf("method/path = %s %s", gotMethod, gotPath)
		}
		if gotBody["content_format"] != "text/md" || gotBody["parent_page_id"] != "P0" {
			t.Errorf("body = %v", gotBody)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "created_page") || !strings.Contains(out, "P9") {
			t.Errorf("create_page result missing: %s", out)
		}
	})

	t.Run("get_page requests markdown", func(t *testing.T) {
		res, _, err := c.clickupGetPage(context.Background(), nil, cuGetPageIn{
			WorkspaceID: "T1", DocID: "D1", PageID: "P1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/workspaces/T1/docs/D1/pages/P1" {
			t.Errorf("path = %s", gotPath)
		}
		if gotQuery.Get("content_format") != "text/md" {
			t.Errorf("content_format = %q", gotQuery.Get("content_format"))
		}
		if out := resultJSON(t, res); !strings.Contains(out, "# Body") {
			t.Errorf("get_page result missing content: %s", out)
		}
	})

	t.Run("update_page append mode", func(t *testing.T) {
		res, _, err := c.clickupUpdatePage(context.Background(), nil, cuUpdatePageIn{
			WorkspaceID: "T1", DocID: "D1", PageID: "P1", Content: "more", EditMode: "append",
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodPut || gotPath != "/workspaces/T1/docs/D1/pages/P1" {
			t.Errorf("method/path = %s %s", gotMethod, gotPath)
		}
		if gotBody["content_edit_mode"] != "append" || gotBody["content_format"] != "text/md" {
			t.Errorf("body = %v", gotBody)
		}
		if out := resultJSON(t, res); !strings.Contains(out, "updated") || !strings.Contains(out, "P1") {
			t.Errorf("update_page result missing: %s", out)
		}
	})

	t.Run("update_page name only defaults edit mode unused", func(t *testing.T) {
		res, _, err := c.clickupUpdatePage(context.Background(), nil, cuUpdatePageIn{
			WorkspaceID: "T1", DocID: "D1", PageID: "P1", Name: "Renamed Page",
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotBody["name"] != "Renamed Page" {
			t.Errorf("body name = %v", gotBody["name"])
		}
		if _, ok := gotBody["content"]; ok {
			t.Errorf("content must be absent when only name given: %v", gotBody)
		}
		_ = res
	})

	t.Run("update_page content defaults to replace mode", func(t *testing.T) {
		if _, _, err := c.clickupUpdatePage(context.Background(), nil, cuUpdatePageIn{
			WorkspaceID: "T1", DocID: "D1", PageID: "P1", Content: "x",
		}); err != nil {
			t.Fatal(err)
		}
		if gotBody["content_edit_mode"] != "replace" {
			t.Errorf("default edit mode = %v, want replace", gotBody["content_edit_mode"])
		}
	})
}

// update_page with neither name nor content errors before any HTTP call.
func TestClickUpUpdatePage_NothingToUpdate(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not make HTTP call, hit %s", r.URL.Path)
	})
	if _, _, err := c.clickupUpdatePage(context.Background(), nil, cuUpdatePageIn{
		WorkspaceID: "T1", DocID: "D1", PageID: "P1",
	}); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v, want nothing-to-update guard", err)
	}
}

// --- client.go: do / clickupBase / truncate error paths ------------------

// A transport-level failure (server closed before the call) surfaces as an
// "http:" wrapped error.
func TestClientDo_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	pointAPIsAt(t, srv)
	srv.Close() // close so the connection is refused

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_1_TOK")
	c := NewClient(creds)
	_, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "http:") {
		t.Fatalf("err = %v, want http: transport error", err)
	}
}

// A >=400 with a long body surfaces "HTTP <code>: <truncated body>" — this hits
// both the error branch and truncate() (body > 400 chars gets the … suffix).
func TestClientDo_HTTPErrorTruncates(t *testing.T) {
	longBody := strings.Repeat("E", 1000)
	c := cuClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(longBody))
	})
	_, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v, want HTTP 400 surfaced", err)
	}
	if !strings.Contains(err.Error(), "…") {
		t.Errorf("long body must be truncated with …: %v", err)
	}
	// truncated body is shorter than the original.
	if strings.Count(err.Error(), "E") >= len(longBody) {
		t.Errorf("body was not truncated: %v", err)
	}
}

// A short (<400) error body is surfaced verbatim (truncate's <= n branch).
func TestClientDo_HTTPErrorShortBody(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden: bad scope"))
	})
	_, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") ||
		!strings.Contains(err.Error(), "forbidden: bad scope") {
		t.Fatalf("err = %v, want HTTP 403 + verbatim body", err)
	}
	if strings.Contains(err.Error(), "…") {
		t.Errorf("short body must not be truncated: %v", err)
	}
}

// A malformed JSON body on a 2xx response surfaces a decode error.
func TestClientDo_DecodeError(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	})
	_, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want decode response error", err)
	}
}

// do() with out==nil (e.g. DELETE comment / update with no response struct)
// returns nil even though the server sent a body — the nil-out branch.
func TestClientDo_NilOutIgnoresBody(t *testing.T) {
	c := cuClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ignored":"body"}`))
	})
	if _, _, err := c.clickupDeleteComment(context.Background(), nil, cuDeleteCommentIn{CommentID: "X"}); err != nil {
		t.Fatalf("nil-out call must succeed, got %v", err)
	}
}

// clickup v2 and v3 calls fail with the not-connected hint when no token is
// stored — exercises the token() error path through clickupBase.
func TestClickUpNotConnected(t *testing.T) {
	creds, _ := testCreds(t) // no token stored
	c := NewClient(creds)
	if _, _, err := c.clickupListWorkspaces(context.Background(), nil, struct{}{}); err == nil ||
		!strings.Contains(err.Error(), "mcp connect clickup") {
		t.Fatalf("v2 err = %v, want not-connected hint", err)
	}
	if _, _, err := c.clickupListDocs(context.Background(), nil, cuListDocsIn{WorkspaceID: "T1"}); err == nil ||
		!strings.Contains(err.Error(), "mcp connect clickup") {
		t.Fatalf("v3 err = %v, want not-connected hint", err)
	}
}

// --- config.go: branches beyond what BuildServer tests cover -------------

// A non-boolean setting value falls back to the default (never a hard error).
func TestLoadConfig_BadBoolFallsBackToDefault(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_ = s.PutSetting(ctx, SettingsKeySlackEnabled, "not-a-bool")
	cfg, err := LoadConfig(ctx, s)
	if err != nil {
		t.Fatalf("bad bool must not error: %v", err)
	}
	if !cfg.Enabled[ServiceSlack] {
		t.Errorf("bad bool must fall back to default true, got %v", cfg.Enabled[ServiceSlack])
	}
}

// Explicit toggles round-trip: clickup read-only on, slack disabled off.
func TestLoadConfig_ExplicitToggles(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_ = s.PutSetting(ctx, SettingsKeyClickUpReadOnly, "true")
	_ = s.PutSetting(ctx, SettingsKeySlackEnabled, "false")
	cfg, err := LoadConfig(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadOnly[ServiceClickUp] {
		t.Errorf("clickup read_only = %v, want true", cfg.ReadOnly[ServiceClickUp])
	}
	if cfg.Enabled[ServiceSlack] {
		t.Errorf("slack enabled = %v, want false", cfg.Enabled[ServiceSlack])
	}
	// Unset keys keep defaults.
	if !cfg.Enabled[ServiceClickUp] || cfg.ReadOnly[ServiceSlack] {
		t.Errorf("unset keys must keep defaults: %+v", cfg)
	}
}

// disabled_tools with empty entries (extra commas / spaces) are skipped.
func TestLoadConfig_DisabledToolsTrimsEmpties(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_ = s.PutSetting(ctx, SettingsKeyDisabledTools, " slack_get_user , , clickup_get_task ,")
	cfg, err := LoadConfig(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Disabled["slack_get_user"] || !cfg.Disabled["clickup_get_task"] {
		t.Errorf("named tools must be disabled: %v", cfg.Disabled)
	}
	if cfg.Disabled[""] || len(cfg.Disabled) != 2 {
		t.Errorf("empty entries must be skipped, got %v", cfg.Disabled)
	}
}

// --- creds.go: remaining branches ----------------------------------------

// Store rejects an empty/whitespace-only token before touching the keyring.
func TestCredStore_StoreEmptyRejected(t *testing.T) {
	creds, ring := testCreds(t)
	if err := creds.Store(ServiceSlack, "   "); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want empty-token rejection", err)
	}
	if _, err := ring.Get(keyringService(ServiceSlack), "tester"); err == nil {
		t.Error("nothing should have been written for an empty token")
	}
}

// Token surfaces a non-ErrNotFound keyring read failure (not silently empty).
func TestCredStore_TokenReadError(t *testing.T) {
	creds := &CredStore{Ring: errRing{}, User: "tester"}
	if _, err := creds.Token(ServiceSlack); err == nil ||
		!strings.Contains(err.Error(), "read slack token") {
		t.Fatalf("err = %v, want wrapped read error", err)
	}
}

// Delete surfaces a non-ErrNotFound keyring delete failure.
func TestCredStore_DeleteError(t *testing.T) {
	creds := &CredStore{Ring: errRing{}, User: "tester"}
	if err := creds.Delete(ServiceClickUp); err == nil ||
		!strings.Contains(err.Error(), "delete clickup token") {
		t.Fatalf("err = %v, want wrapped delete error", err)
	}
}

// MigrateFromClaudeBar surfaces a read failure on claude-bar's entry (e.g. the
// user denied the keychain ACL) rather than treating it as absent.
func TestMigrateFromClaudeBar_ReadError(t *testing.T) {
	creds := &CredStore{Ring: errRing{}, User: "tester"}
	verify := func(context.Context, string) (string, error) { return "x", nil }
	if _, err := creds.MigrateFromClaudeBar(context.Background(), ServiceSlack, verify); err == nil ||
		!strings.Contains(err.Error(), "read claude-bar's slack entry") {
		t.Fatalf("err = %v, want wrapped read error", err)
	}
}

// MigrateFromClaudeBar treats a present-but-blank claude-bar entry as absent.
func TestMigrateFromClaudeBar_BlankEntry(t *testing.T) {
	creds, ring := testCreds(t)
	_ = ring.Set(claudeBarService(ServiceSlack), "tester", []byte("   \n"))
	verify := func(context.Context, string) (string, error) {
		t.Error("verify must not run for a blank entry")
		return "", nil
	}
	ident, err := creds.MigrateFromClaudeBar(context.Background(), ServiceSlack, verify)
	if err != nil || ident != "" {
		t.Fatalf("ident=%q err=%v, want empty/nil for blank entry", ident, err)
	}
}

// errRing is a Keyring whose every op fails with a non-ErrNotFound error, to
// drive the error branches in Token / Delete / MigrateFromClaudeBar.
type errRing struct{}

func (errRing) Get(string, string) ([]byte, error) { return nil, errBoom }
func (errRing) Set(string, string, []byte) error   { return errBoom }
func (errRing) Delete(string, string) error        { return errBoom }

var errBoom = errBoomT("keyring exploded")

type errBoomT string

func (e errBoomT) Error() string { return string(e) }

// --- server.go: textResult error branch ----------------------------------

// textResult surfaces a marshal failure: a channel can't be JSON-encoded, so
// MarshalIndent errors and textResult returns it wrapped.
func TestTextResult_MarshalError(t *testing.T) {
	_, _, err := textResult(map[string]any{"bad": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "encode result") {
		t.Fatalf("err = %v, want encode result error", err)
	}
}
