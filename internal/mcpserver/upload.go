package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
)

// maxUploadBytes caps a from-disk upload read into memory. Real deliverables
// (CSVs, logs, reports) are well under this; the limit just guards against a
// runaway path. Slack/ClickUp accept far larger, but the MCP server holds the
// bytes in memory during the upload, so keep it sane.
const maxUploadBytes = 50 << 20 // 50 MiB

// resolveUploadContent returns the bytes to upload and the effective filename
// for the file-upload tools. Exactly one source must be given:
//
//   - path: read the file from disk server-side. This is the right choice for
//     anything non-trivial — inline content has to pass through the model's
//     token output, so a large file (a 175 KB CSV, say) gets silently truncated
//     mid-stream and the recipient gets a partial file. Reading from disk avoids
//     the model's output budget entirely, and preserves binary bytes exactly.
//   - content: inline text, for small snippets already in hand.
//
// When path is used and filename is empty, the file's base name is used.
func resolveUploadContent(path, content, filename string) (data []byte, name string, err error) {
	switch {
	case path != "" && content != "":
		return nil, "", fmt.Errorf("provide either path or content, not both")
	case path != "":
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", path, err)
		}
		if info.IsDir() {
			return nil, "", fmt.Errorf("%s is a directory, not a file", path)
		}
		if info.Size() > maxUploadBytes {
			return nil, "", fmt.Errorf("file %s is %d bytes, over the %d-byte upload limit", path, info.Size(), maxUploadBytes)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", path, err)
		}
		if name = filename; name == "" {
			name = filepath.Base(path)
		}
		return b, name, nil
	case content != "":
		if filename == "" {
			return nil, "", fmt.Errorf("filename is required when uploading inline content")
		}
		return []byte(content), filename, nil
	default:
		return nil, "", fmt.Errorf("provide path (a file on disk) or content (inline text)")
	}
}
