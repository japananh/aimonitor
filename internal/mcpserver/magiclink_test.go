package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Real-shaped n8n post: "To:" line, subject, and the magic-link wrapped in
// Slack's <…> with a base64 email tail. dev.be.2's tail is the base64 of
// "dev.be.2@gempages.help".
const be2Msg = "To: <mailto:dev.be.2@gempages.help|dev.be.2@gempages.help>\n" +
	"Subject: Your secure link to <http://Claude.ai|Claude.ai> is here | 2026-08-05 13:44:14\n" +
	"Link login: <https://claude.ai/magic-link#338d419e12b406f220aec7a349d403b6:ZGV2LmJlLjJAZ2VtcGFnZXMuaGVscA==>\n" +
	"_Automated with this workflow_"

const be3Msg = "To: <mailto:dev.be.3@gempages.help|dev.be.3@gempages.help>\n" +
	"Link login: <https://claude.ai/magic-link#e1ef39965872d8cb17b19dd06956e45a:ZGV2LmJlLjNAZ2VtcGFnZXMuaGVscA==>"

func TestParseMagicLink_MatchesByEncodedEmail(t *testing.T) {
	// Exact account match returns that account's link.
	got, ok := parseMagicLink(be2Msg, "dev.be.2@gempages.help")
	if !ok || !strings.HasPrefix(got, "https://claude.ai/magic-link#338d419e") {
		t.Fatalf("be2 link = %q ok=%v", got, ok)
	}
	// Case-insensitive on the email.
	if _, ok := parseMagicLink(be2Msg, "DEV.BE.2@gempages.help"); !ok {
		t.Error("email match must be case-insensitive")
	}
	// A different account's email must NOT match be2's link.
	if got, ok := parseMagicLink(be2Msg, "dev.be.3@gempages.help"); ok {
		t.Errorf("wrong-account match leaked link %q", got)
	}
	// No link at all.
	if _, ok := parseMagicLink("nothing here", "dev.be.2@gempages.help"); ok {
		t.Error("empty text must not match")
	}
}

func TestLatestMagicLink_PicksRightAccountAndPassesOldest(t *testing.T) {
	var gotOldest, gotChannel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotOldest = r.URL.Query().Get("oldest")
		gotChannel = r.URL.Query().Get("channel")
		// Newest-first, interleaved accounts.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{"ts": "1785912325.884589", "text": be3Msg},
				{"ts": "1785912265.797749", "text": be2Msg},
			},
		})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	link, err := c.LatestMagicLink(context.Background(), "C0B1K4T9Y1J", "dev.be.2@gempages.help", "1785912000.000000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://claude.ai/magic-link#338d419e") {
		t.Errorf("got %q, want be2's link", link)
	}
	if gotChannel != "C0B1K4T9Y1J" {
		t.Errorf("channel = %q", gotChannel)
	}
	if gotOldest != "1785912000.000000" {
		t.Errorf("oldest = %q, want the bound passed through", gotOldest)
	}
}

func TestLatestMagicLink_NoneReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": []map[string]any{}})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	link, err := c.LatestMagicLink(context.Background(), "C1", "dev.be.2@gempages.help", "")
	if err != nil {
		t.Fatal(err)
	}
	if link != "" {
		t.Errorf("want empty, got %q", link)
	}
}
