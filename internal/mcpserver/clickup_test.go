package mcpserver

import "testing"

// No mentions → flat comment_text, exactly as before (the fallback path).
func TestCommentBody_FlatWhenNoMentions(t *testing.T) {
	b := commentBody("hello", nil)
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
	b := commentBody("ping", []int{123, 456})
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
