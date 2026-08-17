package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pngBytes returns n bytes that sniff as image/png (8-byte signature + filler).
func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("p"), max(0, n-8))...)
	return b
}

// jpegBytes returns n bytes that sniff as image/jpeg.
func jpegBytes(n int) []byte {
	b := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte("j"), max(0, n-4))...)
	return b
}

// imageContent returns the single image block in a result, failing if absent.
func imageContent(t *testing.T, res *mcp.CallToolResult) *mcp.ImageContent {
	t.Helper()
	for _, ct := range res.Content {
		if ic, ok := ct.(*mcp.ImageContent); ok {
			return ic
		}
	}
	t.Fatalf("result carries no image content block: %+v", res.Content)
	return nil
}

// The #120 fix: an image attachment must come back as real pixels in an MCP
// image block (alongside the metadata), not a "content not returned" note.
func TestSlackGetFile_ImageReturnsImageBlock(t *testing.T) {
	want := pngBytes(2048)
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F9", "name": "screen.png", "title": "screen.png",
					"mimetype": "image/png", "filetype": "png", "size": len(want),
					"original_w": 650, "original_h": 686,
					"url_private_download": "http://" + r.Host + "/dl",
				},
			})
		case "/dl":
			authHeader = r.Header.Get("Authorization")
			_, _ = w.Write(want)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F9"})
	if err != nil {
		t.Fatal(err)
	}
	img := imageContent(t, res)
	if !bytes.Equal(img.Data, want) {
		t.Errorf("image bytes = %d, want the %d downloaded bytes verbatim", len(img.Data), len(want))
	}
	if img.MIMEType != "image/png" {
		t.Errorf("mimetype = %q, want image/png", img.MIMEType)
	}
	// The workspace token must authenticate the download.
	if authHeader != "Bearer xoxp-tok" {
		t.Errorf("download Authorization = %q, want the workspace bearer token", authHeader)
	}
	// Metadata still rides along as text.
	out := resultJSON(t, res)
	for _, wantStr := range []string{"screen.png", "650x686", "image/png"} {
		if !strings.Contains(out, wantStr) {
			t.Errorf("metadata missing %q: %s", wantStr, out)
		}
	}
	if strings.Contains(out, "content not returned") {
		t.Errorf("image must not be refused: %s", out)
	}
}

// An image over the inline cap must fall back to Slack's largest fitting
// thumbnail — readable-but-smaller beats not-at-all — and be labelled by what
// the bytes actually are (a PNG original can have a JPEG thumb).
func TestSlackGetFile_OversizedImageFallsBackToThumb(t *testing.T) {
	thumb := jpegBytes(4096)
	var hitOriginal, hit1024 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F10", "name": "huge.png", "mimetype": "image/png",
					"size":                 inlineImageMaxBytes * 4, // way over the cap
					"url_private_download": "http://" + r.Host + "/dl",
					"thumb_1024":           "http://" + r.Host + "/t1024",
					"thumb_360":            "http://" + r.Host + "/t360",
				},
			})
		case "/dl":
			hitOriginal = true
			_, _ = w.Write(pngBytes(inlineImageMaxBytes * 4))
		case "/t1024":
			hit1024 = true
			_, _ = w.Write(thumb)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F10"})
	if err != nil {
		t.Fatal(err)
	}
	if hitOriginal {
		t.Error("must not download an original files.info already reports over the cap")
	}
	if !hit1024 {
		t.Error("must try the largest thumbnail first")
	}
	img := imageContent(t, res)
	if !bytes.Equal(img.Data, thumb) {
		t.Errorf("returned %d bytes, want the %d-byte thumb", len(img.Data), len(thumb))
	}
	if img.MIMEType != "image/jpeg" {
		t.Errorf("mimetype = %q, want image/jpeg sniffed from the thumb bytes (not the PNG original's)", img.MIMEType)
	}
	out := resultJSON(t, res)
	if !strings.Contains(out, "thumbnail") || !strings.Contains(out, "save_to") {
		t.Errorf("must flag the thumbnail fallback and name the full-res route: %s", out)
	}
}

// save_to downloads any mimetype to disk and reports the path — the escape hatch
// for PDFs, archives, and full-resolution images.
func TestSlackGetFile_SaveTo(t *testing.T) {
	want := []byte("%PDF-1.7\nnot really a pdf\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F11", "name": "spec.pdf", "mimetype": "application/pdf",
					"size": len(want), "url_private_download": "http://" + r.Host + "/dl",
				},
			})
		case "/dl":
			_, _ = w.Write(want)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	dst := filepath.Join(t.TempDir(), "spec.pdf")
	res, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F11", SaveTo: dst})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("saved %q, want %q", got, want)
	}
	out := resultJSON(t, res)
	if !strings.Contains(out, dst) || !strings.Contains(out, "bytes_written") {
		t.Errorf("result must report the path + byte count: %s", out)
	}
	// Bytes went to disk, so they must NOT also be inlined.
	for _, ct := range res.Content {
		if _, ok := ct.(*mcp.ImageContent); ok {
			t.Error("save_to must not also return an inline image block")
		}
	}
}

// An unwritable save_to path must surface as an error, not a silent no-op.
func TestSlackGetFile_SaveToBadPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id": "F12", "name": "a.png", "mimetype": "image/png",
					"size": 10, "url_private_download": "http://" + r.Host + "/dl",
				},
			})
		case "/dl":
			_, _ = w.Write(pngBytes(10))
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceSlack, "xoxp-tok")
	c := NewClient(creds)

	bad := filepath.Join(t.TempDir(), "no-such-dir", "a.png")
	if _, _, err := c.slackGetFile(context.Background(), nil, slackGetFileIn{File: "F12", SaveTo: bad}); err == nil {
		t.Error("writing into a missing directory must error")
	}
}

func TestImageMimetypeHelpers(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !isInlineImageMimetype(m) {
			t.Errorf("%s should be inline-able", m)
		}
	}
	// Not renderable as an image block by the model → must stay metadata-only.
	for _, m := range []string{"image/svg+xml", "image/tiff", "image/bmp", "application/pdf", "text/plain", ""} {
		if isInlineImageMimetype(m) {
			t.Errorf("%s must not be treated as an inline image", m)
		}
	}
	if got := sniffImageMimetype(pngBytes(64)); got != "image/png" {
		t.Errorf("sniff png = %q", got)
	}
	if got := sniffImageMimetype(jpegBytes(64)); got != "image/jpeg" {
		t.Errorf("sniff jpeg = %q", got)
	}
	// Non-image bytes sniff to nothing so we never ship an unrenderable block.
	if got := sniffImageMimetype([]byte("just text, not an image at all")); got != "" {
		t.Errorf("sniff text = %q, want empty", got)
	}
}

func TestThumbsLargestFirst(t *testing.T) {
	f := slackFileInfo{Thumb360: "u360", Thumb800: "u800", Thumb1024: "u1024"}
	got := f.thumbsLargestFirst()
	want := []string{"u1024", "u800", "u360"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (largest first, missing sizes skipped)", got, want)
		}
	}
	if len(slackFileInfo{}.thumbsLargestFirst()) != 0 {
		t.Error("no thumbs → empty slice")
	}
}
