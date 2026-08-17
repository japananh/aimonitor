package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// inlineImageMaxBytes caps the raw bytes returned as an inline image block.
// Base64 inflates by 4/3, so 3 MiB of pixels lands near 4 MB on the wire —
// under the 5 MB per-image ceiling the model's API enforces. Screenshots are
// well under this; anything bigger falls back to a smaller pre-rendered size.
const inlineImageMaxBytes = 3 << 20

// isInlineImageMimetype reports whether a mimetype can be handed back as an MCP
// image block. Deliberately a whitelist of the four raster formats models read
// (png/jpeg/gif/webp) rather than all of image/*: shipping a tiff or svg as an
// image block yields an unrenderable payload, so those keep metadata-only
// treatment (with save_to as the escape hatch).
func isInlineImageMimetype(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// sniffImageMimetype returns the mimetype implied by the bytes themselves when
// it is inline-able, else "". Used to label what we actually downloaded: a
// service may hand back a JPEG thumbnail for a PNG original, and mislabeling
// breaks rendering.
func sniffImageMimetype(data []byte) string {
	m := http.DetectContentType(data) // may carry "; charset=…"
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	if isInlineImageMimetype(m) {
		return m
	}
	return ""
}

// imageSource is one candidate URL for an image, tried in slice order.
// Original marks the full-resolution file so the result can say when it fell
// back to a smaller rendition.
type imageSource struct {
	URL      string
	Original bool
}

// fetchFunc downloads up to maxBytes from a URL with whatever auth the service
// needs. Each provider supplies its own.
type fetchFunc func(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error)

// inlineImageResult returns an image's real pixels as an MCP image block next to
// the JSON metadata in out. Sources are tried in order — pass the original first,
// then smaller pre-rendered sizes — so an oversized screenshot still comes back
// readable (smaller) rather than not at all, with no local image decoding.
//
// A source that errors, comes back empty, exceeds the cap, or doesn't sniff as a
// renderable image is skipped; failure is only reported once all are exhausted.
// fullResHint names the way to get the untouched file when we had to shrink.
func inlineImageResult(ctx context.Context, out map[string]any, sources []imageSource, fetch fetchFunc, fullResHint string) (*mcp.CallToolResult, any, error) {
	if len(sources) == 0 {
		out["note"] = "no downloadable image URL available for this attachment"
		return textResult(out)
	}
	for _, s := range sources {
		data, err := fetch(ctx, s.URL, inlineImageMaxBytes+1)
		if err != nil || len(data) == 0 || len(data) > inlineImageMaxBytes {
			continue
		}
		mime := sniffImageMimetype(data)
		if mime == "" {
			continue
		}
		out["returned_bytes"] = len(data)
		out["returned_mimetype"] = mime
		if !s.Original {
			out["thumbnail"] = true
			out["note"] = "original exceeded the inline size cap; returned the largest smaller rendition that fits (" + fullResHint + " for the full-resolution file)"
		}
		return textResultWith(out, &mcp.ImageContent{Data: data, MIMEType: mime})
	}
	out["note"] = fmt.Sprintf("could not return this image inline (cap %d bytes, no rendition fit); %s to download it", inlineImageMaxBytes, fullResHint)
	return textResult(out)
}

// saveDownloadedFile fetches a URL and writes it to path, returning the byte
// count. The mimetype-agnostic escape hatch behind every save_to: PDFs,
// archives, and full-resolution images all land on disk for the caller to open.
func saveDownloadedFile(ctx context.Context, path, rawURL string, fetch fetchFunc) (int, error) {
	data, err := fetch(ctx, rawURL, maxUploadBytes+1)
	if err != nil {
		return 0, err
	}
	if len(data) > maxUploadBytes {
		return 0, fmt.Errorf("file is larger than the %d-byte transfer limit", maxUploadBytes)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	return len(data), nil
}
