package mcpserver

import (
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

// multipartFile must produce a multipart/form-data body whose single part
// carries the given field name, filename, and content verbatim — the shape
// ClickUp's POST /task/{id}/attachment expects (field "attachment").
func TestMultipartFile_RoundTrip(t *testing.T) {
	const data = "code,msg\n1,boom\n"
	body, ctype, err := multipartFile("attachment", "errors.csv", []byte(data))
	if err != nil {
		t.Fatalf("multipartFile: %v", err)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data; boundary=") {
		t.Fatalf("content-type = %q, want multipart/form-data", ctype)
	}
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		t.Fatalf("parse content-type: %v", err)
	}
	part, err := multipart.NewReader(body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatalf("read part: %v", err)
	}
	if part.FormName() != "attachment" {
		t.Errorf("form field = %q, want attachment", part.FormName())
	}
	if part.FileName() != "errors.csv" {
		t.Errorf("filename = %q, want errors.csv", part.FileName())
	}
	got, _ := io.ReadAll(part)
	if string(got) != data {
		t.Errorf("content = %q, want %q", got, data)
	}
}

// slimTask must surface the numeric ids a caller needs to recreate/act on a
// known task via clickup_create_task — list_id, assignee_ids, and tags — not
// just their human-readable names (#27).
func TestSlimTask_CapturesIDsForRecreate(t *testing.T) {
	var raw rawCUTask
	raw.ID = "abc"
	raw.Name = "T"
	raw.List.ID = "901600123"
	raw.List.Name = "Sprint 102"
	raw.Assignees = []struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	}{{ID: 11, Username: "alice"}, {ID: 22, Username: "bob"}}
	raw.Tags = []struct {
		Name string `json:"name"`
	}{{Name: "bug"}, {Name: "p1"}}
	raw.Parent = "86c2parent"
	raw.TopLevelParent = "86c2top"

	got := slimTask(raw)

	if got.Parent != "86c2parent" || got.TopLevelParent != "86c2top" {
		t.Errorf("parent = %q / top = %q, want 86c2parent / 86c2top", got.Parent, got.TopLevelParent)
	}

	if got.ListID != "901600123" || got.List != "Sprint 102" {
		t.Errorf("list = %q / %q, want Sprint 102 / 901600123", got.List, got.ListID)
	}
	if len(got.AssigneeIDs) != 2 || got.AssigneeIDs[0] != 11 || got.AssigneeIDs[1] != 22 {
		t.Errorf("AssigneeIDs = %v, want [11 22]", got.AssigneeIDs)
	}
	if len(got.Assignees) != 2 || got.Assignees[0] != "alice" {
		t.Errorf("Assignees = %v, want [alice bob]", got.Assignees)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "bug" || got.Tags[1] != "p1" {
		t.Errorf("Tags = %v, want [bug p1]", got.Tags)
	}
}

// slimAttachment must prefer the signed url_w_query (downloads without a
// separate auth dance) over the plain url, and coalesce mimetype/mime_type so
// the shape survives both the v2 (mimetype) and v3 (mime_type) API surfaces.
func TestSlimAttachment_PrefersSignedURLAndCoalescesMime(t *testing.T) {
	got := slimAttachment(rawCUAttachment{
		ID:           "att1.png",
		Title:        "image.png",
		Extension:    "png",
		Mimetype:     "image/png",
		Date:         "1782891262181",
		Size:         5881,
		URL:          "https://cdn.clickup.com/att1.png",
		URLWithQuery: "https://cdn.clickup.com/att1.png?sig=abc",
		URLWithHost:  "https://host.clickup.com/att1.png",
	})
	if got.URL != "https://cdn.clickup.com/att1.png?sig=abc" {
		t.Errorf("url = %q, want the signed url_w_query variant", got.URL)
	}
	if got.Mimetype != "image/png" || got.Extension != "png" {
		t.Errorf("mimetype/extension = %q / %q, want image/png / png", got.Mimetype, got.Extension)
	}
	if got.ID != "att1.png" || got.Title != "image.png" || got.Date != "1782891262181" || got.Size != 5881 {
		t.Errorf("attachment = %+v, missing id/title/date/size", got)
	}

	// v3 shape: only mime_type is set, and only the plain url is present.
	v3 := slimAttachment(rawCUAttachment{MimeType: "image/jpeg", URL: "https://cdn/x.jpg"})
	if v3.Mimetype != "image/jpeg" {
		t.Errorf("mime coalesce = %q, want image/jpeg from mime_type", v3.Mimetype)
	}
	if v3.URL != "https://cdn/x.jpg" {
		t.Errorf("url fallback = %q, want plain url when url_w_query absent", v3.URL)
	}
}

// No mentions, no rich array → flat comment_text, exactly as before (fallback).
func TestCommentBody_FlatWhenNoMentions(t *testing.T) {
	b := commentBody("hello", nil, nil)
	if b["comment_text"] != "hello" {
		t.Fatalf("comment_text = %v, want %q", b["comment_text"], "hello")
	}
	if _, ok := b["comment"]; ok {
		t.Errorf("must not send a structured comment array when there are no mentions")
	}
}

// Mentions → structured comment array with one type:tag block per user id, and
// no flat comment_text. The tag blocks carry the user ids in order.
func TestCommentBody_StructuredWithMentions(t *testing.T) {
	b := commentBody("ping", []int{123, 456}, nil)
	if _, ok := b["comment_text"]; ok {
		t.Errorf("must not send flat comment_text when mentions are present")
	}
	blocks, ok := b["comment"].([]map[string]any)
	if !ok {
		t.Fatalf("comment must be a []map[string]any block array, got %T", b["comment"])
	}
	if len(blocks) == 0 || blocks[0]["text"] != "ping" {
		t.Errorf("first block must carry the text %q, got %v", "ping", blocks)
	}
	var ids []int
	for _, bl := range blocks {
		if bl["type"] != "tag" {
			continue
		}
		u, _ := bl["user"].(map[string]any)
		id, _ := u["id"].(int)
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 123 || ids[1] != 456 {
		t.Errorf("tag user ids = %v, want [123 456]", ids)
	}
}

// A caller-supplied rich-text array is sent verbatim as `comment` and wins over
// both the plain text and mentions (it can carry its own tag blocks).
func TestCommentBody_RichTextWinsAndIsVerbatim(t *testing.T) {
	rich := []map[string]any{
		{"text": "item one", "attributes": map[string]any{}},
		{"text": "\n", "attributes": map[string]any{"list": map[string]any{"list": "bullet"}}},
	}
	b := commentBody("ignored text", []int{99}, rich)
	if _, ok := b["comment_text"]; ok {
		t.Errorf("rich array must not fall back to flat comment_text")
	}
	got, ok := b["comment"].([]map[string]any)
	if !ok {
		t.Fatalf("comment must be the rich array, got %T", b["comment"])
	}
	if len(got) != 2 || got[0]["text"] != "item one" {
		t.Errorf("comment must be the verbatim rich array, got %v", got)
	}
	// verbatim: no injected tag block from the ignored mentions arg
	for _, bl := range got {
		if bl["type"] == "tag" {
			t.Errorf("rich path must not inject mention tags; got %v", got)
		}
	}
}

// createTaskBody must map custom_item_id onto the ClickUp body, and send it even
// when the type ID is 0 (a valid work-item type id) — the field is a pointer so
// "set to 0" is distinct from "omitted".
func TestCreateTaskBody_CustomItemID(t *testing.T) {
	zero := 0
	bug := 1300
	cases := []struct {
		name string
		in   cuCreateTaskIn
		want any // nil means the key must be absent
	}{
		{"omitted", cuCreateTaskIn{Name: "T"}, nil},
		{"zero is sent", cuCreateTaskIn{Name: "T", CustomItemID: &zero}, 0},
		{"bug type", cuCreateTaskIn{Name: "T", CustomItemID: &bug}, 1300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := createTaskBody(tc.in)
			got, ok := b["custom_item_id"]
			if tc.want == nil {
				if ok {
					t.Errorf("custom_item_id must be absent, got %v", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("custom_item_id = %v (present=%v), want %v", got, ok, tc.want)
			}
		})
	}
}

// A description must be sent under markdown_content (which renders [label](url)
// and bare URLs as clickable link marks) as well as the legacy
// markdown_description fallback — both carrying the verbatim markdown (#102).
func TestCreateTaskBody_DescriptionUsesMarkdownContent(t *testing.T) {
	const md = "See [X](https://example.com) and https://example.com"

	b := createTaskBody(cuCreateTaskIn{Name: "T", Description: md})
	if b["markdown_content"] != md {
		t.Errorf("markdown_content = %v, want %q", b["markdown_content"], md)
	}
	if b["markdown_description"] != md {
		t.Errorf("markdown_description = %v, want %q (no-regression fallback)", b["markdown_description"], md)
	}

	// No description → neither markdown key is sent.
	empty := createTaskBody(cuCreateTaskIn{Name: "T"})
	if _, ok := empty["markdown_content"]; ok {
		t.Errorf("markdown_content must be absent when no description given")
	}
	if _, ok := empty["markdown_description"]; ok {
		t.Errorf("markdown_description must be absent when no description given")
	}
}

// Same link-rendering fix on the update path: a new description goes out under
// both markdown_content and markdown_description, and neither when unset (#102).
func TestUpdateTaskBody_DescriptionUsesMarkdownContent(t *testing.T) {
	const md = "Ref [PR](https://example.com/pr/1)"

	b, err := updateTaskBody(cuUpdateTaskIn{TaskID: "abc", Description: md})
	if err != nil {
		t.Fatalf("updateTaskBody: %v", err)
	}
	if b["markdown_content"] != md {
		t.Errorf("markdown_content = %v, want %q", b["markdown_content"], md)
	}
	if b["markdown_description"] != md {
		t.Errorf("markdown_description = %v, want %q (no-regression fallback)", b["markdown_description"], md)
	}

	// Updating another field without a description must not send either key.
	other, err := updateTaskBody(cuUpdateTaskIn{TaskID: "abc", Status: "open"})
	if err != nil {
		t.Fatalf("updateTaskBody: %v", err)
	}
	if _, ok := other["markdown_content"]; ok {
		t.Errorf("markdown_content must be absent when no description given")
	}
	if _, ok := other["markdown_description"]; ok {
		t.Errorf("markdown_description must be absent when no description given")
	}
}

// updateTaskBody must map add/remove assignees onto ClickUp's
// {"assignees":{"add":[…],"rem":[…]}} shape, include only the side that's set,
// and carry custom_item_id through.
func TestUpdateTaskBody_AssigneesAndType(t *testing.T) {
	bug := 1300
	in := cuUpdateTaskIn{
		TaskID:          "abc",
		AddAssignees:    []int{11, 22},
		RemoveAssignees: []int{33},
		CustomItemID:    &bug,
	}
	b, err := updateTaskBody(in)
	if err != nil {
		t.Fatalf("updateTaskBody: %v", err)
	}
	a, ok := b["assignees"].(map[string]any)
	if !ok {
		t.Fatalf("assignees must be a map, got %T", b["assignees"])
	}
	add, _ := a["add"].([]int)
	rem, _ := a["rem"].([]int)
	if len(add) != 2 || add[0] != 11 || add[1] != 22 {
		t.Errorf("assignees.add = %v, want [11 22]", add)
	}
	if len(rem) != 1 || rem[0] != 33 {
		t.Errorf("assignees.rem = %v, want [33]", rem)
	}
	if b["custom_item_id"] != 1300 {
		t.Errorf("custom_item_id = %v, want 1300", b["custom_item_id"])
	}
}

// Only the assignee side that's provided is sent; the missing side is absent
// (not an empty array, which ClickUp would read as "remove all").
func TestUpdateTaskBody_AssigneesOneSided(t *testing.T) {
	b, err := updateTaskBody(cuUpdateTaskIn{TaskID: "abc", AddAssignees: []int{7}})
	if err != nil {
		t.Fatalf("updateTaskBody: %v", err)
	}
	a, ok := b["assignees"].(map[string]any)
	if !ok {
		t.Fatalf("assignees must be a map, got %T", b["assignees"])
	}
	if _, ok := a["rem"]; ok {
		t.Errorf("assignees.rem must be absent when no removals given, got %v", a["rem"])
	}
	if add, _ := a["add"].([]int); len(add) != 1 || add[0] != 7 {
		t.Errorf("assignees.add = %v, want [7]", a["add"])
	}
}

// With no fields set, updateTaskBody must refuse rather than send an empty PUT.
func TestUpdateTaskBody_RejectsEmpty(t *testing.T) {
	if _, err := updateTaskBody(cuUpdateTaskIn{TaskID: "abc"}); err == nil {
		t.Errorf("expected an error for an update with no fields")
	}
}

// setCustomFieldBody must always carry the value verbatim (any shape) and only
// include value_options when provided.
func TestSetCustomFieldBody(t *testing.T) {
	// value_options omitted when empty.
	b := setCustomFieldBody(cuSetCustomFieldIn{Value: "hello"})
	if b["value"] != "hello" {
		t.Errorf("value = %v, want hello", b["value"])
	}
	if _, ok := b["value_options"]; ok {
		t.Errorf("value_options must be absent when not provided")
	}

	// A zero-ish value (0, false) must still be forwarded — it's a real value,
	// not "unset" (the handler guards nil separately).
	if got := setCustomFieldBody(cuSetCustomFieldIn{Value: 0})["value"]; got != 0 {
		t.Errorf("value = %v, want 0 forwarded verbatim", got)
	}

	// value_options forwarded when present.
	withOpts := setCustomFieldBody(cuSetCustomFieldIn{Value: 1700000000000, ValueOptions: map[string]any{"time": true}})
	opts, ok := withOpts["value_options"].(map[string]any)
	if !ok || opts["time"] != true {
		t.Errorf("value_options = %v, want {time:true}", withOpts["value_options"])
	}
}

// dependencySide must require EXACTLY ONE direction, returning the ClickUp field
// name and value shared by the add-body and delete-query paths.
func TestDependencySide(t *testing.T) {
	t.Run("depends_on", func(t *testing.T) {
		key, val, err := dependencySide(cuDependencyIn{TaskID: "t", DependsOn: "up"})
		if err != nil || key != "depends_on" || val != "up" {
			t.Errorf("got (%q,%q,%v), want (depends_on,up,nil)", key, val, err)
		}
	})
	t.Run("dependency_of", func(t *testing.T) {
		key, val, err := dependencySide(cuDependencyIn{TaskID: "t", DependencyOf: "down"})
		if err != nil || key != "dependency_of" || val != "down" {
			t.Errorf("got (%q,%q,%v), want (dependency_of,down,nil)", key, val, err)
		}
	})
	t.Run("neither errors", func(t *testing.T) {
		if _, _, err := dependencySide(cuDependencyIn{TaskID: "t"}); err == nil {
			t.Errorf("expected an error when neither side is set")
		}
	})
	t.Run("both errors", func(t *testing.T) {
		if _, _, err := dependencySide(cuDependencyIn{TaskID: "t", DependsOn: "a", DependencyOf: "b"}); err == nil {
			t.Errorf("expected an error when both sides are set")
		}
	})
}

// updateChecklistBody must map name/position, keep position=0 (a valid first
// slot) distinct from omitted, and refuse an empty update.
func TestUpdateChecklistBody(t *testing.T) {
	zero := 0
	b, err := updateChecklistBody(cuUpdateChecklistIn{ChecklistID: "c", Name: "QA", Position: &zero})
	if err != nil {
		t.Fatalf("updateChecklistBody: %v", err)
	}
	if b["name"] != "QA" {
		t.Errorf("name = %v, want QA", b["name"])
	}
	if b["position"] != 0 {
		t.Errorf("position = %v, want 0 sent explicitly", b["position"])
	}

	// name only → no position key.
	nameOnly, _ := updateChecklistBody(cuUpdateChecklistIn{ChecklistID: "c", Name: "QA"})
	if _, ok := nameOnly["position"]; ok {
		t.Errorf("position must be absent when not provided")
	}

	if _, err := updateChecklistBody(cuUpdateChecklistIn{ChecklistID: "c"}); err == nil {
		t.Errorf("expected an error for an empty checklist update")
	}
}

// updateChecklistItemBody must map each optional field, keep resolved=false /
// assignee=0 distinct from omitted (pointers), and refuse an empty update.
func TestUpdateChecklistItemBody(t *testing.T) {
	notDone := false
	who := 0
	b, err := updateChecklistItemBody(cuUpdateChecklistItemIn{
		ChecklistID: "c", ChecklistItemID: "i",
		Name: "step", Resolved: &notDone, Assignee: &who, Parent: "p",
	})
	if err != nil {
		t.Fatalf("updateChecklistItemBody: %v", err)
	}
	if b["name"] != "step" || b["parent"] != "p" {
		t.Errorf("name/parent = %v/%v, want step/p", b["name"], b["parent"])
	}
	if b["resolved"] != false {
		t.Errorf("resolved = %v, want false sent explicitly", b["resolved"])
	}
	if b["assignee"] != 0 {
		t.Errorf("assignee = %v, want 0 sent explicitly", b["assignee"])
	}

	// resolved only → no name/assignee/parent keys.
	done := true
	only, _ := updateChecklistItemBody(cuUpdateChecklistItemIn{ChecklistID: "c", ChecklistItemID: "i", Resolved: &done})
	for _, k := range []string{"name", "assignee", "parent"} {
		if _, ok := only[k]; ok {
			t.Errorf("%s must be absent when not provided", k)
		}
	}
	if only["resolved"] != true {
		t.Errorf("resolved = %v, want true", only["resolved"])
	}

	if _, err := updateChecklistItemBody(cuUpdateChecklistItemIn{ChecklistID: "c", ChecklistItemID: "i"}); err == nil {
		t.Errorf("expected an error for an empty checklist-item update")
	}
}

// slimChecklist must surface the checklist id, name, and each item's id/name/
// resolved (never a nil items slice, so the JSON carries [] not null).
func TestSlimChecklist(t *testing.T) {
	raw := rawCUChecklist{ID: "c1", Name: "QA"}
	raw.Items = []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Resolved bool   `json:"resolved"`
	}{{ID: "i1", Name: "build", Resolved: true}, {ID: "i2", Name: "ship", Resolved: false}}

	got := slimChecklist(raw)
	if got.ID != "c1" || got.Name != "QA" {
		t.Errorf("checklist = %q/%q, want c1/QA", got.ID, got.Name)
	}
	if len(got.Items) != 2 || got.Items[0].ID != "i1" || !got.Items[0].Resolved || got.Items[1].ID != "i2" || got.Items[1].Resolved {
		t.Errorf("items = %+v, want [{i1 build true} {i2 ship false}]", got.Items)
	}

	// Empty checklist → non-nil empty slice.
	if empty := slimChecklist(rawCUChecklist{ID: "c2"}); empty.Items == nil {
		t.Errorf("items must be a non-nil empty slice, got nil")
	}
}
