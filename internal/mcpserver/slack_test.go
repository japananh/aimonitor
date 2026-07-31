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

// slack_post_message with a future post_at routes to chat.scheduleMessage
// (same body, incl. post_at), and surfaces scheduled_message_id instead of ts.
func TestSlackPostMessage_ScheduleWithPostAt(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "channel": "C1", "scheduled_message_id": "Q1234", "post_at": 1893456000,
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackPostMessage(context.Background(), nil, slackPostIn{
		Channel: "C1", Text: "review please", PostAt: 1893456000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat.scheduleMessage" {
		t.Errorf("path = %s, want /chat.scheduleMessage", gotPath)
	}
	if gotBody["channel"] != "C1" || gotBody["text"] != "review please" {
		t.Errorf("body = %v, want channel + text", gotBody)
	}
	// post_at decodes from JSON as a float64; compare numerically.
	if pa, _ := gotBody["post_at"].(float64); int64(pa) != 1893456000 {
		t.Errorf("post_at = %v, want 1893456000", gotBody["post_at"])
	}
	out := resultJSON(t, res)
	for _, want := range []string{"scheduled", "Q1234", `"channel": "C1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("scheduled result missing %q in %s", want, out)
		}
	}
	// A scheduled post has no ts yet — must not claim one.
	if strings.Contains(out, `"ts"`) {
		t.Errorf("scheduled result must not carry a ts: %s", out)
	}
}

// slack_post_message with post_at == 0 (the default) still posts immediately
// via chat.postMessage — scheduling is strictly opt-in.
func TestSlackPostMessage_NoPostAtPostsImmediately(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "9.9"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackPostMessage(context.Background(), nil, slackPostIn{Channel: "C1", Text: "now"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat.postMessage" {
		t.Errorf("path = %s, want /chat.postMessage", gotPath)
	}
	if _, ok := gotBody["post_at"]; ok {
		t.Errorf("post_at must be absent when not scheduling: %v", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "posted") || !strings.Contains(out, "9.9") {
		t.Errorf("result missing posted/ts: %s", out)
	}
}

// slack_cancel_scheduled_message removes a pending scheduled post via
// chat.deleteScheduledMessage (channel + scheduled_message_id) and reports
// status "canceled".
func TestSlackCancelScheduledMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackCancelScheduledMessage(context.Background(), nil, slackCancelScheduledIn{
		Channel: "C1", ScheduledMessageID: "Q1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat.deleteScheduledMessage" {
		t.Errorf("path = %s, want /chat.deleteScheduledMessage", gotPath)
	}
	if gotBody["channel"] != "C1" || gotBody["scheduled_message_id"] != "Q1234" {
		t.Errorf("body = %v, want channel + scheduled_message_id", gotBody)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "canceled") || !strings.Contains(out, "Q1234") {
		t.Errorf("cancel result missing canceled/id: %s", out)
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

// slack_get_file resolves a file id: files.info for metadata, then downloads
// url_private (with the workspace Bearer token) and returns the FULL text for a
// text-like mimetype — not just Slack's short preview — plus name/mimetype/size.
func TestSlackGetFile_TextFullContent(t *testing.T) {
	const content = "line1\nline2\nline3\nline4\nline5\n"
	var (
		infoQuery    url.Values
		downloadAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			infoQuery = r.URL.Query()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F1", "name": "app.log", "title": "App log",
					"mimetype": "text/plain", "filetype": "text",
					"size": len(content), "lines": 5,
					"url_private_download": "http://" + r.Host + "/download",
					"url_private":          "http://" + r.Host + "/private",
				},
			})
		case "/download":
			downloadAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, content)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F1"})
	if err != nil {
		t.Fatal(err)
	}
	if infoQuery.Get("file") != "F1" {
		t.Errorf("files.info file = %q, want F1", infoQuery.Get("file"))
	}
	if downloadAuth != "Bearer xoxp-tok" {
		t.Errorf("download auth = %q, want the workspace bearer token", downloadAuth)
	}
	out := resultJSON(t, res)
	for _, want := range []string{"app.log", "text/plain", "line1", "line5", `"lines": 5`} {
		if !strings.Contains(out, want) {
			t.Errorf("get_file result missing %q in %s", want, out)
		}
	}
}

// slack_get_file honours offset/limit as a 1-based line window and reports the
// window (offset + total content_lines) alongside the sliced content.
func TestSlackGetFile_LineRange(t *testing.T) {
	const content = "a\nb\nc\nd\ne\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F2", "name": "n.txt", "mimetype": "text/plain",
					"url_private_download": "http://" + r.Host + "/download",
				},
			})
		case "/download":
			_, _ = io.WriteString(w, content)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F2", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	out := resultJSON(t, res)
	if !strings.Contains(out, `"content": "b\nc"`) {
		t.Errorf("expected lines 2-3 (b,c) in %s", out)
	}
	if !strings.Contains(out, `"offset": 2`) || !strings.Contains(out, `"content_lines": 5`) {
		t.Errorf("expected offset/content_lines window metadata in %s", out)
	}
	if strings.Contains(out, `"d"`) || strings.Contains(out, `"e"`) {
		t.Errorf("range must exclude lines past the limit: %s", out)
	}
}

// slack_get_file returns metadata + a note (and NEVER downloads bytes) for a
// binary / non-text mimetype.
func TestSlackGetFile_BinaryReturnsNote(t *testing.T) {
	var downloaded bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F3", "name": "photo.png", "mimetype": "image/png",
					"size": 40000, "url_private_download": "http://" + r.Host + "/download",
				},
			})
		case "/download":
			downloaded = true
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F3"})
	if err != nil {
		t.Fatal(err)
	}
	if downloaded {
		t.Error("must NOT download bytes for a non-text mimetype")
	}
	out := resultJSON(t, res)
	if !strings.Contains(out, "photo.png") || !strings.Contains(out, "note") || !strings.Contains(out, "image/png") {
		t.Errorf("binary result must carry metadata + a note: %s", out)
	}
	if strings.Contains(out, `"content"`) {
		t.Errorf("binary result must not carry a content field: %s", out)
	}
}

// slack_get_file requires a file id before any network call.
func TestSlackGetFile_RequiresFileID(t *testing.T) {
	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)
	if _, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{}); err == nil {
		t.Error("missing file id must error")
	}
}

// sliceLines is 1-based, treats a trailing newline as not-a-line, and clamps an
// out-of-range offset instead of panicking.
func TestSliceLines(t *testing.T) {
	// No range → whole string, correct total (trailing "\n" not counted).
	if out, total, ranged := sliceLines("a\nb\nc\n", 0, 0); out != "a\nb\nc\n" || total != 3 || ranged {
		t.Errorf("no-range = (%q,%d,%v), want (whole,3,false)", out, total, ranged)
	}
	// Window in the middle.
	if out, _, ranged := sliceLines("a\nb\nc\nd", 2, 2); out != "b\nc" || !ranged {
		t.Errorf("window = (%q,%v), want (\"b\\nc\",true)", out, ranged)
	}
	// Offset past the end → empty window, no panic.
	if out, _, ranged := sliceLines("a\nb", 99, 5); out != "" || !ranged {
		t.Errorf("past-end = (%q,%v), want (\"\",true)", out, ranged)
	}
}

// isTextMimetype accepts text/* and a handful of text-bearing application/*
// types, and rejects binary ones.
func TestIsTextMimetype(t *testing.T) {
	for _, m := range []string{"text/plain", "text/csv", "application/json", "application/x-yaml"} {
		if !isTextMimetype(m) {
			t.Errorf("%q should be text-like", m)
		}
	}
	for _, m := range []string{"image/png", "application/pdf", "application/octet-stream", ""} {
		if isTextMimetype(m) {
			t.Errorf("%q should NOT be text-like", m)
		}
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
