package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// fakeRing is an in-memory secret.Keyring for tests.
type fakeRing struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newFakeRing() *fakeRing { return &fakeRing{m: map[string][]byte{}} }

func (f *fakeRing) key(service, account string) string { return service + "\x00" + account }

func (f *fakeRing) Get(service, account string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.m[f.key(service, account)]
	if !ok {
		return nil, secret.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (f *fakeRing) Set(service, account string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[f.key(service, account)] = append([]byte(nil), data...)
	return nil
}

func (f *fakeRing) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(service, account)
	if _, ok := f.m[k]; !ok {
		return secret.ErrNotFound
	}
	delete(f.m, k)
	return nil
}

func testCreds(t *testing.T) (*CredStore, *fakeRing) {
	t.Helper()
	ring := newFakeRing()
	return &CredStore{Ring: ring, User: "tester"}, ring
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- credential store + migration ---------------------------------------

func TestCredStore_RoundTrip(t *testing.T) {
	creds, _ := testCreds(t)
	if tok, err := creds.Token(ServiceSlack); err != nil || tok != "" {
		t.Fatalf("empty store: tok=%q err=%v, want empty/nil", tok, err)
	}
	if err := creds.Store(ServiceSlack, "  xoxp-abc  "); err != nil {
		t.Fatal(err)
	}
	tok, err := creds.Token(ServiceSlack)
	if err != nil || tok != "xoxp-abc" {
		t.Fatalf("got %q err=%v, want trimmed xoxp-abc", tok, err)
	}
	if err := creds.Delete(ServiceSlack); err != nil {
		t.Fatal(err)
	}
	if err := creds.Delete(ServiceSlack); err != nil {
		t.Fatalf("double delete must be idempotent, got %v", err)
	}
}

func TestMigrateFromClaudeBar(t *testing.T) {
	ctx := context.Background()
	okVerify := func(_ context.Context, token string) (string, error) {
		if token != "xoxp-from-claude-bar" {
			t.Fatalf("verify got %q", token)
		}
		return "violet @ team", nil
	}

	t.Run("no claude-bar entry → empty, no error", func(t *testing.T) {
		creds, _ := testCreds(t)
		ident, err := creds.MigrateFromClaudeBar(ctx, ServiceSlack, okVerify)
		if err != nil || ident != "" {
			t.Fatalf("ident=%q err=%v, want empty/nil", ident, err)
		}
	})

	t.Run("migrates, verifies, leaves source intact", func(t *testing.T) {
		creds, ring := testCreds(t)
		_ = ring.Set("claude-bar-mcp:shared:slack", "tester", []byte("xoxp-from-claude-bar\n"))
		ident, err := creds.MigrateFromClaudeBar(ctx, ServiceSlack, okVerify)
		if err != nil || ident != "violet @ team" {
			t.Fatalf("ident=%q err=%v", ident, err)
		}
		tok, _ := creds.Token(ServiceSlack)
		if tok != "xoxp-from-claude-bar" {
			t.Fatalf("our copy = %q", tok)
		}
		if src, err := ring.Get("claude-bar-mcp:shared:slack", "tester"); err != nil || len(src) == 0 {
			t.Fatalf("claude-bar's entry must be untouched: %v", err)
		}
	})

	t.Run("failed verification does not store", func(t *testing.T) {
		creds, ring := testCreds(t)
		_ = ring.Set("claude-bar-mcp:shared:clickup", "tester", []byte("pk_bad"))
		badVerify := func(context.Context, string) (string, error) {
			return "", context.DeadlineExceeded
		}
		if _, err := creds.MigrateFromClaudeBar(ctx, ServiceClickUp, badVerify); err == nil {
			t.Fatal("want verification error")
		}
		if tok, _ := creds.Token(ServiceClickUp); tok != "" {
			t.Fatalf("bad token stored: %q", tok)
		}
	})
}

// --- registration honoring config ----------------------------------------

func has(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestBuildServer_RegistrationHonorsConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing connected → no tools", func(t *testing.T) {
		creds, _ := testCreds(t)
		cfg, _ := LoadConfig(ctx, openTestStore(t))
		_, reg := BuildServer(cfg, creds)
		if len(reg) != 0 {
			t.Fatalf("no tokens stored, got tools %v", reg)
		}
	})

	t.Run("slack connected → slack tools only", func(t *testing.T) {
		creds, _ := testCreds(t)
		_ = creds.Store(ServiceSlack, "xoxp-x")
		cfg, _ := LoadConfig(ctx, openTestStore(t))
		_, reg := BuildServer(cfg, creds)
		if !has(reg, "slack_post_message") || !has(reg, "slack_search_messages") {
			t.Fatalf("slack tools missing: %v", reg)
		}
		if has(reg, "clickup_get_task") {
			t.Fatalf("clickup not connected but tools present: %v", reg)
		}
	})

	t.Run("read_only hides write tools", func(t *testing.T) {
		creds, _ := testCreds(t)
		_ = creds.Store(ServiceSlack, "xoxp-x")
		_ = creds.Store(ServiceClickUp, "pk_1_X")
		s := openTestStore(t)
		_ = s.PutSetting(ctx, SettingsKeySlackReadOnly, "true")
		cfg, _ := LoadConfig(ctx, s)
		_, reg := BuildServer(cfg, creds)
		if has(reg, "slack_post_message") {
			t.Fatalf("read-only slack still exposes post: %v", reg)
		}
		if !has(reg, "slack_search_messages") {
			t.Fatalf("read tools must stay: %v", reg)
		}
		if !has(reg, "clickup_create_task") {
			t.Fatalf("clickup not read-only, create must stay: %v", reg)
		}
	})

	t.Run("service disabled hides everything", func(t *testing.T) {
		creds, _ := testCreds(t)
		_ = creds.Store(ServiceClickUp, "pk_1_X")
		s := openTestStore(t)
		_ = s.PutSetting(ctx, SettingsKeyClickUpEnabled, "false")
		cfg, _ := LoadConfig(ctx, s)
		_, reg := BuildServer(cfg, creds)
		for _, n := range reg {
			if strings.HasPrefix(n, "clickup_") {
				t.Fatalf("disabled service exposes %s", n)
			}
		}
	})

	t.Run("disabled_tools hides individual tools", func(t *testing.T) {
		creds, _ := testCreds(t)
		_ = creds.Store(ServiceSlack, "xoxp-x")
		s := openTestStore(t)
		_ = s.PutSetting(ctx, SettingsKeyDisabledTools, "slack_list_users, slack_get_user")
		cfg, _ := LoadConfig(ctx, s)
		_, reg := BuildServer(cfg, creds)
		if has(reg, "slack_list_users") || has(reg, "slack_get_user") {
			t.Fatalf("disabled tools present: %v", reg)
		}
		if !has(reg, "slack_list_channels") {
			t.Fatalf("non-disabled tool missing: %v", reg)
		}
	})
}

// --- HTTP clients ---------------------------------------------------------

// pointAPIsAt redirects both API bases at a test server for the duration
// of the test.
func pointAPIsAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldSlack, oldCU := slackAPIBase, clickupAPIBase
	slackAPIBase = srv.URL
	clickupAPIBase = srv.URL
	t.Cleanup(func() { slackAPIBase, clickupAPIBase = oldSlack, oldCU })
}

func TestSlackPostMessage(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C123", "ts": "1.2"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackPostMessage(context.Background(), nil, slackPostIn{
		Channel: "C123", Text: "hi", ThreadTS: "1.0", ReplyBroadcast: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer xoxp-tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["thread_ts"] != "1.0" || gotBody["reply_broadcast"] != true {
		t.Errorf("body = %v", gotBody)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty result")
	}
}

func TestSlackErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)
	_, _, err := c.slackChannelHistory(context.Background(), nil, slackHistoryIn{Channel: "Cnope"})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v, want slack error surfaced", err)
	}
}

func TestSlackNotConnected(t *testing.T) {
	creds, _ := testCreds(t)
	c := NewClient(creds)
	_, _, err := c.slackGetUser(context.Background(), nil, slackGetUserIn{User: "U1"})
	if err == nil || !strings.Contains(err.Error(), "mcp connect slack") {
		t.Fatalf("err = %v, want not-connected hint", err)
	}
}

func TestClickUpAuthAndSlimming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "pk_1_TOK" {
			t.Errorf("clickup auth must be raw token, got %q", got)
		}
		switch r.URL.Path {
		case "/list/L1/task":
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{
				"id": "t1", "name": "Fix bug",
				"status":    map[string]any{"status": "in progress"},
				"assignees": []map[string]any{{"username": "violet"}},
				"priority":  map[string]any{"priority": "high"},
				"url":       "https://app.clickup.com/t/t1",
				"list":      map[string]any{"name": "Sprint 1"},
				// noise that must be dropped by slimming:
				"custom_fields": []any{1, 2, 3},
				"watchers":      []any{"a", "b"},
			}}})
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_1_TOK")
	c := NewClient(creds)
	res, _, err := c.clickupListTasks(context.Background(), nil, cuListTasksIn{ListID: "L1"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(interface{ MarshalJSON() ([]byte, error) })
	b, _ := text.MarshalJSON()
	out := string(b)
	for _, want := range []string{"Fix bug", "in progress", "violet", "high", "Sprint 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("slimmed task missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "custom_fields") || strings.Contains(out, "watchers") {
		t.Errorf("slimming leaked noise: %s", out)
	}
}

func TestClickUpRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_1_TOK")
	c := NewClient(creds)
	_, _, err := c.clickupGetTask(context.Background(), nil, cuTaskIn{TaskID: "x"})
	if err == nil || !strings.Contains(err.Error(), "retry after 42") {
		t.Fatalf("err = %v, want 429 with Retry-After surfaced", err)
	}
}

func TestClickUpUpdateComment(t *testing.T) {
	var gotMethod, gotPath, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		var body struct {
			CommentText string `json:"comment_text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotText = body.CommentText
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_1_TOK")
	c := NewClient(creds)
	res, _, err := c.clickupUpdateComment(context.Background(), nil,
		cuUpdateCommentIn{CommentID: "C1", Comment: "edited text"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/comment/C1" {
		t.Errorf("path = %s, want /comment/C1", gotPath)
	}
	if gotText != "edited text" {
		t.Errorf("comment_text = %q, want %q", gotText, "edited text")
	}
	text := res.Content[0].(interface{ MarshalJSON() ([]byte, error) })
	b, _ := text.MarshalJSON()
	if out := string(b); !strings.Contains(out, "updated") || !strings.Contains(out, "C1") {
		t.Errorf("result missing updated/C1: %s", out)
	}
}

func TestVerifySlack_RejectsBotToken(t *testing.T) {
	if _, err := VerifySlack(context.Background(), "xoxb-bot"); err == nil ||
		!strings.Contains(err.Error(), "bot token") {
		t.Fatalf("bot token must be rejected with a pointed message, got %v", err)
	}
}

func TestSlackSearchURLEncoding(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": map[string]any{"total": 0}})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)
	if _, _, err := c.slackSearchMessages(context.Background(), nil, slackSearchIn{Query: "in:#dev from:@violet hỏi"}); err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("query") != "in:#dev from:@violet hỏi" {
		t.Errorf("query = %q", gotQuery.Get("query"))
	}
	if gotQuery.Get("count") != "20" {
		t.Errorf("default count = %q, want 20", gotQuery.Get("count"))
	}
}
