package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clickup_list_comments must surface reply_count, and clickup_list_comment_replies
// must fetch a comment's thread from /comment/{id}/reply.
func TestClickupCommentReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/T1/comment":
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []map[string]any{
				{"id": "c1", "comment_text": "top-level", "user": map[string]any{"username": "vi"}, "date": "1", "reply_count": 2},
			}})
		case "/comment/c1/reply":
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []map[string]any{
				{"id": "r1", "comment_text": "reply one", "user": map[string]any{"username": "bob"}, "date": "2"},
				{"id": "r2", "comment_text": "reply two", "user": map[string]any{"username": "amy"}, "date": "3"},
			}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)
	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_x")
	c := NewClient(creds)

	res, _, err := c.clickupListComments(context.Background(), nil, cuTaskIn{TaskID: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	if body := resultJSON(t, res); !strings.Contains(body, `"reply_count": 2`) {
		t.Errorf("list_comments did not surface reply_count: %s", body)
	}

	res2, _, err := c.clickupListCommentReplies(context.Background(), nil, cuCommentRepliesIn{CommentID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	body2 := resultJSON(t, res2)
	if !strings.Contains(body2, `"reply one"`) || !strings.Contains(body2, `"id": "r2"`) || !strings.Contains(body2, `"by": "bob"`) {
		t.Errorf("replies missing/misshaped: %s", body2)
	}
}
