package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// JSONL is the schema we extract from a Claude Code transcript line. We
// parse the fields we care about and tolerate everything else — Claude
// Code's JSONL format is liberal and we don't want to break when new
// fields appear.
//
// File layout (one JSON object per line):
//
//	{"type":"assistant","timestamp":"2026-05-13T08:00:00.000Z",
//	 "sessionId":"...","requestId":"...",
//	 "message":{
//	   "model":"claude-opus-4-7",
//	   "usage":{
//	     "input_tokens":100,"output_tokens":50,
//	     "cache_creation_input_tokens":0,"cache_read_input_tokens":0
//	   }
//	 }
//	}
//
// User messages, summary entries, and any unknown shapes are skipped: they
// don't carry usage data.
type JSONL struct {
	Type string `json:"type"`
	// RequestID is the Anthropic API request that produced this line
	// (top-level, e.g. "req_011Cc6..."). Paired with Message.ID it forms
	// the dedup key for usage_samples: Claude Code writes the SAME
	// usage-bearing line multiple times during streaming/retries, so naive
	// summation over-counts tokens ~2.6x. See ParseLine.
	RequestID string  `json:"requestId"`
	Timestamp string  `json:"timestamp"`
	Message   message `json:"message"`
}

type message struct {
	// ID is the Anthropic message id (e.g. "msg_01NXd5..."), the other
	// half of the (request_id, message_id) dedup key.
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *usage `json:"usage"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// Sample is one usage data point extracted from a JSONL line. Fields map
// 1:1 to the usage_samples SQLite table.
//
// MessageID + RequestID together identify the logical API response. They
// are the dedup key: streaming and retries write several JSONL lines for
// one response with identical token counts, so persisting on
// (message_id, request_id) with INSERT OR IGNORE keeps exactly one row.
type Sample struct {
	Ts           time.Time
	MessageID    string
	RequestID    string
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	Model        string
}

// ParseLine returns (sample, true) when the line is a usage-bearing
// assistant entry, or (zero, false) when the line should be skipped
// (different type, missing usage, malformed JSON). It never returns an
// error: a single malformed line in a transcript is not a stop-the-world
// condition, and the watcher just keeps going.
//
// If you want to know about malformed lines, use ParseLineStrict.
func ParseLine(line []byte) (Sample, bool) {
	s, ok, _ := parseLine(line)
	return s, ok
}

// ParseLineStrict is like ParseLine but returns the underlying JSON error
// when one occurs. Used by tests and by the watcher's debug log.
func ParseLineStrict(line []byte) (Sample, bool, error) {
	return parseLine(line)
}

func parseLine(line []byte) (Sample, bool, error) {
	if len(line) == 0 {
		return Sample{}, false, nil
	}
	var l JSONL
	if err := json.Unmarshal(line, &l); err != nil {
		return Sample{}, false, fmt.Errorf("json: %w", err)
	}
	if l.Type != "assistant" || l.Message.Usage == nil {
		return Sample{}, false, nil
	}

	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		// Fall back to RFC3339 (no nanoseconds) before giving up.
		ts, err = time.Parse(time.RFC3339, l.Timestamp)
		if err != nil {
			return Sample{}, false, fmt.Errorf("timestamp %q: %w", l.Timestamp, err)
		}
	}

	u := l.Message.Usage
	return Sample{
		Ts:           ts,
		MessageID:    l.Message.ID,
		RequestID:    l.RequestID,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CacheRead:    u.CacheReadInputTokens,
		CacheWrite:   u.CacheCreationInputTokens,
		Model:        l.Message.Model,
	}, true, nil
}

// ParseReader scans every line of r and yields a Sample per usage-bearing
// assistant entry. The bytesRead callback fires after each successful
// line read with the cumulative byte offset, letting the watcher persist
// resumable progress per-line rather than per-batch.
//
// ParseReader returns the total bytes consumed and the first I/O error,
// if any. JSON parse errors per-line do NOT stop the scan — they are
// silently skipped, matching ParseLine semantics. Use ParseLineStrict on
// individual lines if you need per-line diagnostics.
func ParseReader(r io.Reader, onSample func(Sample), onProgress func(bytesRead int64)) (int64, error) {
	br := bufio.NewReader(r)
	var consumed int64
	for {
		line, err := br.ReadBytes('\n')
		consumed += int64(len(line))
		// Strip the trailing newline for cleaner JSON parsing.
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			if s, ok := ParseLine(line); ok && onSample != nil {
				onSample(s)
			}
		}
		if onProgress != nil {
			onProgress(consumed)
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
	}
}
