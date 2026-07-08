package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// slimMsg must pick User, falling back to Username when the bot/legacy
// payload only carries the latter, and prefix the channel name with "#"
// while leaving a bare channel ID alone.
func TestSlimMsg_UserFallbackAndChannelPrefix(t *testing.T) {
	// User present → used verbatim; channel has a name → "#name".
	var withName rawSlackMsg
	withName.TS = "1.1"
	withName.User = "U123"
	withName.Text = "hello"
	withName.Channel.ID = "C1"
	withName.Channel.Name = "dev"
	got := slimMsg(withName, slimOpts{})
	if got.User != "U123" {
		t.Errorf("user = %q, want U123", got.User)
	}
	if got.Channel != "#dev" {
		t.Errorf("channel = %q, want #dev", got.Channel)
	}

	// No User → fall back to Username; channel has no name → raw ID.
	var botMsg rawSlackMsg
	botMsg.TS = "2.2"
	botMsg.Username = "ci-bot"
	botMsg.Channel.ID = "C9"
	got = slimMsg(botMsg, slimOpts{})
	if got.User != "ci-bot" {
		t.Errorf("user = %q, want ci-bot fallback", got.User)
	}
	if got.Channel != "C9" {
		t.Errorf("channel = %q, want raw C9", got.Channel)
	}
}

// slimMsg must ALWAYS surface counts (the cheap detection signal for #58) but
// only copy the heavy arrays through when the matching opt is set. The
// attachment shape mirrors a real app unfurl (is_app_unfurl / app_unfurl_url),
// the ClickUp link_shared card the issue is about.
func TestSlimMsg_CountsAlwaysOptInPayload(t *testing.T) {
	var m rawSlackMsg
	m.TS = "1.1"
	m.Attachments = []json.RawMessage{
		json.RawMessage(`{"is_app_unfurl":true,"app_unfurl_url":"https://app.clickup.com/t/123"}`),
	}
	m.Blocks = []json.RawMessage{json.RawMessage(`{"type":"rich_text"}`)}
	m.Files = nil

	// Default: counts present, no full arrays.
	def := slimMsg(m, slimOpts{})
	if def.AttachmentCount != 1 || def.BlockCount != 1 || def.FileCount != 0 {
		t.Errorf("counts = att %d / block %d / file %d, want 1/1/0", def.AttachmentCount, def.BlockCount, def.FileCount)
	}
	if def.Attachments != nil || def.Blocks != nil || def.Files != nil {
		t.Errorf("default must omit full arrays, got att=%v blocks=%v files=%v", def.Attachments, def.Blocks, def.Files)
	}

	// Opt into attachments only: the unfurl card is passed through verbatim,
	// blocks stay omitted (they're the heavy payload).
	att := slimMsg(m, slimOpts{attachments: true})
	if len(att.Attachments) != 1 {
		t.Fatalf("attachments not included: %v", att.Attachments)
	}
	if !strings.Contains(string(att.Attachments[0]), "is_app_unfurl") {
		t.Errorf("attachment not passed through verbatim: %s", att.Attachments[0])
	}
	if att.Blocks != nil {
		t.Errorf("blocks must stay omitted when only attachments requested")
	}
}

// slack_post_message in a thread must thread the reply (thread_ts) and carry
// the raw mrkdwn text; non-thread fields stay absent.
func TestSlackPostMessage_ThreadReplyAndText(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("path = %s, want /chat.postMessage", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "9.9"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackPostMessage(context.Background(), nil, slackPostIn{
		Channel: "C1", Text: "<@U1> deploy done", ThreadTS: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["channel"] != "C1" || gotBody["text"] != "<@U1> deploy done" {
		t.Errorf("body = %v, want raw mrkdwn text + channel", gotBody)
	}
	if gotBody["thread_ts"] != "1.0" {
		t.Errorf("thread_ts = %v, want 1.0", gotBody["thread_ts"])
	}
	// reply_broadcast not requested → must be absent, not false.
	if _, ok := gotBody["reply_broadcast"]; ok {
		t.Errorf("reply_broadcast must be absent when not asked: %v", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "posted") || !strings.Contains(out, "9.9") {
		t.Errorf("result missing posted/ts: %s", out)
	}
}

// slack_update_message edits a posted message via chat.update and reports
// status "updated".
func TestSlackUpdateMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "5.5"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackUpdateMessage(context.Background(), nil, slackUpdateIn{
		Channel: "C1", TS: "5.5", Text: "fixed <@U2>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat.update" {
		t.Errorf("path = %s, want /chat.update", gotPath)
	}
	if gotBody["channel"] != "C1" || gotBody["ts"] != "5.5" || gotBody["text"] != "fixed <@U2>" {
		t.Errorf("body = %v", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "updated") {
		t.Errorf("result missing updated: %s", out)
	}
}

// slack_delete_message removes a message via chat.delete and reports
// status "deleted".
func TestSlackDeleteMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "5.5"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackDeleteMessage(context.Background(), nil, slackDeleteIn{
		Channel: "C1", TS: "5.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat.delete" {
		t.Errorf("path = %s, want /chat.delete", gotPath)
	}
	if gotBody["channel"] != "C1" || gotBody["ts"] != "5.5" {
		t.Errorf("body = %v", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "deleted") {
		t.Errorf("result missing deleted: %s", out)
	}
}

// slack_search_messages parses populated matches into the slim message shape:
// total + each match's ts/user/text/permalink survive, raw noise is dropped.
func TestSlackSearchMessages_SlimsMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": map[string]any{
				"total": 2,
				"matches": []map[string]any{
					{"ts": "1.1", "user": "U1", "text": "found it",
						"permalink": "https://slack.com/p1",
						"channel":   map[string]any{"id": "C1", "name": "dev"}},
					{"ts": "2.2", "username": "bot", "text": "and this"},
				},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackSearchMessages(context.Background(), nil, slackSearchIn{Query: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	out := resultJSON(t, res)
	for _, want := range []string{`"total": 2`, "found it", "and this",
		"https://slack.com/p1", `"#dev"`, `"user": "bot"`} {
		if !strings.Contains(out, want) {
			t.Errorf("search result missing %q in %s", want, out)
		}
	}
}

// slack_channel_history sends channel + default limit (and oldest/latest when
// given) and slims each message through slimMsg.
func TestSlackChannelHistory_ParamsAndSlimming(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"has_more": true,
			"messages": []map[string]any{
				{"ts": "1.1", "user": "U1", "text": "first"},
				{"ts": "2.2", "username": "bot", "text": "second"},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackChannelHistory(context.Background(), nil, slackHistoryIn{
		Channel: "C1", Oldest: "100.0", Latest: "200.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("channel") != "C1" {
		t.Errorf("channel = %q", gotQuery.Get("channel"))
	}
	if gotQuery.Get("limit") != "30" {
		t.Errorf("default limit = %q, want 30", gotQuery.Get("limit"))
	}
	if gotQuery.Get("oldest") != "100.0" || gotQuery.Get("latest") != "200.0" {
		t.Errorf("oldest/latest = %q/%q", gotQuery.Get("oldest"), gotQuery.Get("latest"))
	}
	out := resultJSON(t, res)
	for _, want := range []string{"first", "second", `"user": "bot"`, `"has_more": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("history result missing %q in %s", want, out)
		}
	}
}

// slack_channel_history must expose an unfurl/preview card (#58): by default via
// attachment_count only (cheap detection), and via the full attachments array
// when include_attachments is set. blocks stay omitted unless separately asked.
func TestSlackChannelHistory_AttachmentsCountAndOptIn(t *testing.T) {
	// A message carrying the ClickUp app's async link_shared unfurl card.
	unfurlMsg := map[string]any{
		"ts": "1.1", "user": "U1", "text": "see task",
		"attachments": []map[string]any{
			{"is_app_unfurl": true, "app_unfurl_url": "https://app.clickup.com/t/86cwq1wrh"},
		},
		"blocks": []map[string]any{{"type": "rich_text"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": []map[string]any{unfurlMsg}})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	// Default: count present, full arrays absent (no unfurl body, no blocks).
	res, _, err := c.slackChannelHistory(context.Background(), nil, slackHistoryIn{Channel: "C1"})
	if err != nil {
		t.Fatal(err)
	}
	def := resultJSON(t, res)
	if !strings.Contains(def, `"attachment_count": 1`) {
		t.Errorf("default must surface attachment_count for cheap detection: %s", def)
	}
	if strings.Contains(def, "app_unfurl_url") || strings.Contains(def, "rich_text") {
		t.Errorf("default must NOT include the heavy attachment/block bodies: %s", def)
	}

	// Opt in to attachments only: unfurl card passed through, blocks still out.
	res, _, err = c.slackChannelHistory(context.Background(), nil, slackHistoryIn{Channel: "C1", IncludeAttachments: true})
	if err != nil {
		t.Fatal(err)
	}
	att := resultJSON(t, res)
	if !strings.Contains(att, "app_unfurl_url") || !strings.Contains(att, "app.clickup.com/t/86cwq1wrh") {
		t.Errorf("include_attachments must pass the unfurl card through: %s", att)
	}
	if strings.Contains(att, "rich_text") {
		t.Errorf("blocks must stay omitted when only attachments requested: %s", att)
	}
}

// slack_thread_replies sends channel + ts + default limit and slims replies.
func TestSlackThreadReplies(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{"ts": "1.0", "user": "U1", "text": "parent", "reply_count": 1},
				{"ts": "1.1", "user": "U2", "text": "reply", "thread_ts": "1.0"},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackThreadReplies(context.Background(), nil, slackRepliesIn{
		Channel: "C1", TS: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("channel") != "C1" || gotQuery.Get("ts") != "1.0" {
		t.Errorf("channel/ts = %q/%q", gotQuery.Get("channel"), gotQuery.Get("ts"))
	}
	if gotQuery.Get("limit") != "50" {
		t.Errorf("default limit = %q, want 50", gotQuery.Get("limit"))
	}
	out := resultJSON(t, res)
	for _, want := range []string{"parent", "reply", `"thread_ts": "1.0"`} {
		if !strings.Contains(out, want) {
			t.Errorf("replies result missing %q in %s", want, out)
		}
	}
}

// slack_list_channels defaults to public+private, excludes archived, caps the
// limit, threads a cursor, and returns next_cursor for pagination.
func TestSlackListChannels_DefaultsAndPaging(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"channels": []map[string]any{
				{"id": "C1", "name": "general", "is_private": false},
			},
			"response_metadata": map[string]any{"next_cursor": "CUR2"},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackListChannels(context.Background(), nil, slackListChannelsIn{Cursor: "CUR1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("types") != "public_channel,private_channel" {
		t.Errorf("types = %q, want public_channel,private_channel default", gotQuery.Get("types"))
	}
	if gotQuery.Get("limit") != "100" {
		t.Errorf("limit = %q, want 100 default", gotQuery.Get("limit"))
	}
	if gotQuery.Get("exclude_archived") != "true" {
		t.Errorf("exclude_archived = %q, want true", gotQuery.Get("exclude_archived"))
	}
	if gotQuery.Get("cursor") != "CUR1" {
		t.Errorf("cursor = %q, want CUR1 threaded", gotQuery.Get("cursor"))
	}
	out := resultJSON(t, res)
	if !strings.Contains(out, "general") || !strings.Contains(out, "CUR2") {
		t.Errorf("channels result missing name/next_cursor: %s", out)
	}
}

// slack_list_users caps the limit and surfaces members + next_cursor.
func TestSlackListUsers(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"members": []map[string]any{
				{"id": "U1", "name": "violet", "real_name": "Violet T"},
			},
			"response_metadata": map[string]any{"next_cursor": "NXT"},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackListUsers(context.Background(), nil, slackListUsersIn{Limit: 999})
	if err != nil {
		t.Fatal(err)
	}
	// 999 > 200 → falls back to default 100.
	if gotQuery.Get("limit") != "100" {
		t.Errorf("limit = %q, want clamped to 100", gotQuery.Get("limit"))
	}
	out := resultJSON(t, res)
	for _, want := range []string{"violet", "Violet T", "NXT"} {
		if !strings.Contains(out, want) {
			t.Errorf("users result missing %q in %s", want, out)
		}
	}
}

// slack_get_permalink sends channel + message_ts and returns the URL.
func TestSlackGetPermalink(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.getPermalink" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "permalink": "https://slack.com/archives/C1/p123",
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetPermalink(context.Background(), nil, slackPermalinkIn{
		Channel: "C1", MessageTS: "123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("channel") != "C1" || gotQuery.Get("message_ts") != "123" {
		t.Errorf("query = %v", gotQuery)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "https://slack.com/archives/C1/p123") {
		t.Errorf("result missing permalink: %s", out)
	}
}

// slack_upload_file drives Slack's three-step external upload: it reserves a
// URL (filename+length query), PUT/POSTs the raw bytes to that URL WITHOUT the
// auth token (it must not leak to the reserved URL), then finalizes + shares
// into the channel/thread with the initial comment, and surfaces the resulting
// file_id + permalink.
func TestSlackUploadFile_ExternalUploadFlow(t *testing.T) {
	const content = "package main\n\nfunc main() {}\n"
	var (
		reserveQuery  url.Values
		uploadedBody  []byte
		uploadCType   string
		uploadHadAuth bool
		completeBody  map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			reserveQuery = r.URL.Query()
			// Build the reserved URL from this request so it points back at us
			// (the server's own address) without a chicken-and-egg on srv.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"file_id":    "F123",
				"upload_url": "http://" + r.Host + "/upload-bytes",
			})
		case "/upload-bytes":
			uploadedBody, _ = io.ReadAll(r.Body)
			uploadCType = r.Header.Get("Content-Type")
			uploadHadAuth = r.Header.Get("Authorization") != ""
			w.WriteHeader(http.StatusOK)
		case "/files.completeUploadExternal":
			_ = json.NewDecoder(r.Body).Decode(&completeBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"files": []map[string]any{
					{"id": "F123", "permalink": "https://slack.com/files/F123"},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackUploadFile(context.Background(), nil, slackUploadIn{
		Filename: "main.go", Content: content, Channel: "C1",
		ThreadTS: "1.0", Title: "Snippet", Comment: "here you go",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 1: reserve carries filename + the byte length.
	if reserveQuery.Get("filename") != "main.go" {
		t.Errorf("reserve filename = %q, want main.go", reserveQuery.Get("filename"))
	}
	if reserveQuery.Get("length") != strconv.Itoa(len(content)) {
		t.Errorf("reserve length = %q, want %d", reserveQuery.Get("length"), len(content))
	}

	// Step 2: the raw bytes are POSTed verbatim, as octet-stream, with NO auth.
	if string(uploadedBody) != content {
		t.Errorf("uploaded body = %q, want %q", uploadedBody, content)
	}
	if uploadCType != "application/octet-stream" {
		t.Errorf("upload content-type = %q, want application/octet-stream", uploadCType)
	}
	if uploadHadAuth {
		t.Error("auth token must NOT be sent to the reserved upload URL")
	}

	// Step 3: finalize + share carries the file id/title, channel, thread, comment.
	files, ok := completeBody["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("complete files = %v, want one entry", completeBody["files"])
	}
	f, _ := files[0].(map[string]any)
	if f["id"] != "F123" || f["title"] != "Snippet" {
		t.Errorf("complete file = %v, want id F123 / title Snippet", f)
	}
	if completeBody["channel_id"] != "C1" || completeBody["thread_ts"] != "1.0" {
		t.Errorf("complete channel/thread = %v", completeBody)
	}
	if completeBody["initial_comment"] != "here you go" {
		t.Errorf("complete comment = %v", completeBody["initial_comment"])
	}

	// Result surfaces the file id, status, and shared permalink.
	out := resultJSON(t, res)
	for _, want := range []string{"F123", "uploaded", "https://slack.com/files/F123"} {
		if !strings.Contains(out, want) {
			t.Errorf("upload result missing %q in %s", want, out)
		}
	}
}

// slack_upload_file requires both filename and content before any network call.
func TestSlackUploadFile_RequiresFilenameAndContent(t *testing.T) {
	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)
	if _, _, err := c.slackUploadFile(context.Background(), nil, slackUploadIn{Content: "x"}); err == nil {
		t.Error("missing filename must error")
	}
	if _, _, err := c.slackUploadFile(context.Background(), nil, slackUploadIn{Filename: "f.txt"}); err == nil {
		t.Error("missing content must error")
	}
}

// A Slack missing_scope error on a read tool surfaces the actionable scope
// message (the envelope check path, exercised through a real tool call).
func TestSlackMissingScope_SurfacedThroughTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error": "missing_scope",
			"needed": "channels:read", "provided": "chat:write",
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)
	_, _, err := c.slackListChannels(context.Background(), nil, slackListChannelsIn{})
	if err == nil {
		t.Fatal("missing_scope must surface as an error")
	}
	for _, want := range []string{"channels:read", "OAuth & Permissions", "chat:write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}
