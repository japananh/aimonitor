package mcpserver

import "testing"

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
