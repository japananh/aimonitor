package mcpserver

import "testing"

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
