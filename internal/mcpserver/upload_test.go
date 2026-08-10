package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResolveUploadContent(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "report.csv")
	const fileBody = "shop,plan\nacme,pro\nbeta,free\n"
	if err := os.WriteFile(csv, []byte(fileBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Inline content + filename.
	if b, name, err := resolveUploadContent("", "hello", "a.txt"); err != nil || string(b) != "hello" || name != "a.txt" {
		t.Errorf("content: got (%q,%q,%v)", b, name, err)
	}
	// Path with no filename → base name; bytes read from disk verbatim.
	if b, name, err := resolveUploadContent(csv, "", ""); err != nil || string(b) != fileBody || name != "report.csv" {
		t.Errorf("path: got (%q,%q,%v)", b, name, err)
	}
	// Path + explicit filename overrides the base name.
	if _, name, err := resolveUploadContent(csv, "", "renamed.csv"); err != nil || name != "renamed.csv" {
		t.Errorf("path+name: got (%q,%v)", name, err)
	}

	// Error paths.
	for _, tc := range []struct {
		name             string
		path, content, f string
	}{
		{"both", csv, "inline", ""},
		{"neither", "", "", ""},
		{"content-no-filename", "", "data", ""},
		{"dir", dir, "", ""},
		{"missing-file", filepath.Join(dir, "nope.csv"), "", ""},
	} {
		if _, _, err := resolveUploadContent(tc.path, tc.content, tc.f); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}

	// Over the size cap (sparse file — no real disk use).
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxUploadBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, _, err := resolveUploadContent(big, "", ""); err == nil {
		t.Error("over-cap file must error")
	}
}

// The reported bug: a large CSV couldn't be uploaded because only inline text
// was accepted. With a path, the bytes are read from disk (never through the
// model's output) and the filename defaults to the base name.
func TestSlackUploadFile_FromPath(t *testing.T) {
	dir := t.TempDir()
	// Bigger than any single-response budget would carry inline.
	body := strings.Repeat("shop_id,email,plan\n123,a@b.co,pro\n", 8000)
	p := filepath.Join(dir, "waitlist_shops.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var reserveName, reserveLen string
	var uploaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			reserveName = r.URL.Query().Get("filename")
			reserveLen = r.URL.Query().Get("length")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file_id": "F1", "upload_url": "http://" + r.Host + "/put"})
		case "/put":
			uploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case "/files.completeUploadExternal":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files": []map[string]any{{"id": "F1", "permalink": "https://x/F1"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	if _, _, err := c.slackUploadFile(context.Background(), nil, slackUploadIn{Path: p, Channel: "C1", ThreadTS: "9.9"}); err != nil {
		t.Fatalf("upload from path: %v", err)
	}
	if reserveName != "waitlist_shops.csv" {
		t.Errorf("reserve filename = %q, want waitlist_shops.csv (base of path)", reserveName)
	}
	if reserveLen != strconv.Itoa(len(body)) {
		t.Errorf("reserve length = %q, want %d", reserveLen, len(body))
	}
	if string(uploaded) != body {
		t.Errorf("uploaded %d bytes, want %d (full file, untruncated)", len(uploaded), len(body))
	}
}

func TestClickupUploadAttachment_FromPath(t *testing.T) {
	dir := t.TempDir()
	const body = "code,msg\n500,boom\n429,slow\n"
	p := filepath.Join(dir, "errors.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotName string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task/T1/attachment" {
			t.Errorf("path = %s, want /task/T1/attachment", r.URL.Path)
		}
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		gotName = part.FileName()
		gotBytes, _ = io.ReadAll(part)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "att1", "url": "http://x/att1", "title": gotName})
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_tok")
	c := NewClient(creds)

	res, _, err := c.clickupUploadAttachment(context.Background(), nil, cuUploadAttachmentIn{TaskID: "T1", Path: p})
	if err != nil {
		t.Fatalf("upload from path: %v", err)
	}
	if gotName != "errors.csv" {
		t.Errorf("attachment filename = %q, want errors.csv", gotName)
	}
	if string(gotBytes) != body {
		t.Errorf("attachment body = %q, want %q", gotBytes, body)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "attached") || !strings.Contains(out, "errors.csv") {
		t.Errorf("result missing attached/filename: %s", out)
	}
}
