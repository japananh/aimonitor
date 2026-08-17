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
)

// An image attached to a ClickUp task must come back as real pixels, resolved
// through a FRESH task lookup (ClickUp's download URLs are presigned and expire,
// so the caller must never have to pass one in).
func TestClickupGetAttachment_Image(t *testing.T) {
	want := pngBytes(1024)
	var taskHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/86abc":
			taskHits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"attachments": []map[string]any{
					{"id": "att-other", "title": "other.png", "mimetype": "image/png"},
					{
						"id": "att-1", "title": "bug.png", "mimetype": "image/png",
						"extension": "png", "size": len(want),
						"url_w_query": "http://" + r.Host + "/dl?sig=abc",
					},
				},
			})
		case "/dl":
			// Presigned link: must be fetched WITHOUT an Authorization header,
			// which S3 would reject alongside the query signature.
			if r.Header.Get("Authorization") != "" {
				t.Errorf("presigned URL must not carry an Authorization header")
			}
			_, _ = w.Write(want)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_tok")
	c := NewClient(creds)

	res, _, err := c.clickupGetAttachment(context.Background(), nil, cuGetAttachmentIn{
		TaskID: "86abc", AttachmentID: "att-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if taskHits != 1 {
		t.Errorf("task lookups = %d, want exactly 1 fresh resolve", taskHits)
	}
	img := imageContent(t, res)
	if !bytes.Equal(img.Data, want) {
		t.Errorf("image bytes = %d, want the %d downloaded bytes", len(img.Data), len(want))
	}
	if img.MIMEType != "image/png" {
		t.Errorf("mimetype = %q, want image/png", img.MIMEType)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "bug.png") {
		t.Errorf("metadata must identify the attachment: %s", out)
	}
}

// A presigned URL that 403s must be retried with the ClickUp token — some
// workspaces serve attachments from an authenticated host.
func TestClickupGetAttachment_RetriesWithToken(t *testing.T) {
	want := pngBytes(512)
	var sawUnauthed, sawAuthed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/T":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"attachments": []map[string]any{{
					"id": "a1", "title": "s.png", "mimetype": "image/png",
					"size": len(want), "url": "http://" + r.Host + "/private",
				}},
			})
		case "/private":
			if r.Header.Get("Authorization") == "" {
				sawUnauthed = true
				w.WriteHeader(http.StatusForbidden)
				return
			}
			sawAuthed = true
			_, _ = w.Write(want)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_tok")
	c := NewClient(creds)

	res, _, err := c.clickupGetAttachment(context.Background(), nil, cuGetAttachmentIn{TaskID: "T", AttachmentID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if !sawUnauthed || !sawAuthed {
		t.Errorf("want an unauthenticated try then an authenticated retry (got %v/%v)", sawUnauthed, sawAuthed)
	}
	if img := imageContent(t, res); !bytes.Equal(img.Data, want) {
		t.Error("retry must return the bytes")
	}
}

func TestClickupGetAttachment_SaveToAndText(t *testing.T) {
	csv := []byte("code,msg\n500,boom\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/T":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"attachments": []map[string]any{{
					"id": "a1", "title": "errors.csv", "mime_type": "text/csv",
					"size": len(csv), "url_w_query": "http://" + r.Host + "/dl",
				}},
			})
		case "/dl":
			_, _ = w.Write(csv)
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_tok")
	c := NewClient(creds)

	// Text attachment → inline text (mime_type fallback field must be honored).
	res, _, err := c.clickupGetAttachment(context.Background(), nil, cuGetAttachmentIn{TaskID: "T", AttachmentID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if out := resultJSON(t, res); !strings.Contains(out, "500,boom") {
		t.Errorf("text attachment must return content: %s", out)
	}

	// save_to → bytes on disk.
	dst := filepath.Join(t.TempDir(), "errors.csv")
	if _, _, err := c.clickupGetAttachment(context.Background(), nil, cuGetAttachmentIn{
		TaskID: "T", AttachmentID: "a1", SaveTo: dst,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, csv) {
		t.Errorf("saved %q (err %v), want %q", got, err, csv)
	}
}

func TestClickupGetAttachment_Validation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/task/T" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"attachments": []map[string]any{{"id": "real", "title": "x.png"}},
			})
		}
	}))
	defer srv.Close()
	pointAPIsAt(t, srv)

	creds, _ := testCreds(t)
	_ = creds.Store(ServiceClickUp, "pk_tok")
	c := NewClient(creds)
	ctx := context.Background()

	for _, in := range []cuGetAttachmentIn{
		{AttachmentID: "a"}, // missing task_id
		{TaskID: "T"},       // missing attachment_id
	} {
		if _, _, err := c.clickupGetAttachment(ctx, nil, in); err == nil {
			t.Errorf("in=%+v must error", in)
		}
	}
	// Unknown id names what's actually there, so the caller can correct itself.
	_, _, err := c.clickupGetAttachment(ctx, nil, cuGetAttachmentIn{TaskID: "T", AttachmentID: "nope"})
	if err == nil || !strings.Contains(err.Error(), "real") {
		t.Errorf("err = %v, want a not-found naming the available ids", err)
	}
}

func TestCUAttachmentURLHelpers(t *testing.T) {
	// url_w_query (presigned) wins over the bare url and the host variant.
	a := rawCUAttachment{URL: "u", URLWithQuery: "q", URLWithHost: "h"}
	if got := a.downloadURL(); got != "q" {
		t.Errorf("downloadURL = %q, want the presigned q", got)
	}
	if got := (rawCUAttachment{URLWithHost: "h"}).downloadURL(); got != "h" {
		t.Errorf("downloadURL = %q, want h", got)
	}
	if got := (rawCUAttachment{}).downloadURL(); got != "" {
		t.Errorf("downloadURL = %q, want empty", got)
	}
	th := rawCUAttachment{ThumbnailSmall: "s", ThumbnailLarge: "l"}.thumbsLargestFirst()
	if len(th) != 2 || th[0] != "l" || th[1] != "s" {
		t.Errorf("thumbs = %v, want [l s]", th)
	}
}
