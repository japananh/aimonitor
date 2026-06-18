package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLine_AssistantWithUsage(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-05-13T08:00:01.123Z","sessionId":"abc","message":{"model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":200}}}`)

	s, ok := ParseLine(line)
	if !ok {
		t.Fatal("expected ok=true for assistant+usage")
	}
	want := Sample{
		Ts:           time.Date(2026, 5, 13, 8, 0, 1, 123_000_000, time.UTC),
		InputTokens:  100,
		OutputTokens: 50,
		CacheRead:    200,
		CacheWrite:   10,
		Model:        "claude-opus-4-7",
	}
	if s != want {
		t.Errorf("got %+v\nwant %+v", s, want)
	}
}

func TestParseLine_CapturesDedupKey(t *testing.T) {
	// message.id and top-level requestId are the dedup key for usage_samples.
	line := []byte(`{"type":"assistant","timestamp":"2026-06-01T03:13:30.123Z","requestId":"req_011Cbb","message":{"id":"msg_01Cxf","model":"claude-opus-4-8","usage":{"input_tokens":6382,"output_tokens":618}}}`)
	s, ok := ParseLine(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if s.MessageID != "msg_01Cxf" {
		t.Errorf("MessageID = %q, want msg_01Cxf", s.MessageID)
	}
	if s.RequestID != "req_011Cbb" {
		t.Errorf("RequestID = %q, want req_011Cbb", s.RequestID)
	}
}

func TestParseLine_Skips(t *testing.T) {
	cases := map[string]string{
		"empty":                 ``,
		"user message":          `{"type":"user","timestamp":"2026-05-13T08:00:00.000Z","message":{"content":"hi"}}`,
		"summary":               `{"type":"summary","timestamp":"2026-05-13T08:00:00.000Z"}`,
		"assistant no usage":    `{"type":"assistant","timestamp":"2026-05-13T08:00:00.000Z","message":{"model":"claude-opus-4-7"}}`,
		"assistant nil message": `{"type":"assistant","timestamp":"2026-05-13T08:00:00.000Z"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseLine([]byte(line)); ok {
				t.Errorf("expected ok=false, got ok=true")
			}
		})
	}
}

func TestParseLine_RFC3339WithoutNanoseconds(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-05-13T08:00:01Z","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`)
	s, ok := ParseLine(line)
	if !ok {
		t.Fatal("ok=true expected")
	}
	if s.Ts.Nanosecond() != 0 {
		t.Errorf("nanoseconds should be zero; got %d", s.Ts.Nanosecond())
	}
}

func TestParseLineStrict_MalformedJSON(t *testing.T) {
	_, ok, err := ParseLineStrict([]byte(`this is not json`))
	if ok {
		t.Error("malformed JSON: ok should be false")
	}
	if err == nil {
		t.Error("malformed JSON: err should be non-nil")
	}
}

func TestParseLineStrict_BadTimestamp(t *testing.T) {
	_, ok, err := ParseLineStrict([]byte(`{"type":"assistant","timestamp":"yesterday","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`))
	if ok {
		t.Error("bad timestamp: ok should be false")
	}
	if err == nil {
		t.Error("bad timestamp: err should be non-nil")
	}
}

func TestParseReader_FixtureFile(t *testing.T) {
	path := filepath.Join("testdata", "sample.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var samples []Sample
	var lastProgress int64
	consumed, err := ParseReader(f, func(s Sample) {
		samples = append(samples, s)
	}, func(p int64) {
		lastProgress = p
	})
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}

	// Compare consumed against the file size — should match.
	info, _ := os.Stat(path)
	if consumed != info.Size() {
		t.Errorf("consumed %d, file size %d", consumed, info.Size())
	}
	if lastProgress != info.Size() {
		t.Errorf("lastProgress %d, file size %d", lastProgress, info.Size())
	}

	// The fixture has 8 lines: user, assistant(usage), user, assistant(usage),
	// summary, assistant(no usage), garbage, assistant(usage). Three samples.
	if len(samples) != 3 {
		t.Errorf("got %d samples, want 3", len(samples))
	}

	// First sample matches r1.
	if samples[0].InputTokens != 100 || samples[0].OutputTokens != 50 || samples[0].CacheRead != 200 {
		t.Errorf("samples[0] mismatch: %+v", samples[0])
	}
	// Last sample matches r3.
	if samples[2].InputTokens != 50 || samples[2].OutputTokens != 25 {
		t.Errorf("samples[2] mismatch: %+v", samples[2])
	}
}

func TestParseReader_NoTrailingNewline(t *testing.T) {
	// A transcript still being written may not end with \n. The reader
	// must still consume the last byte and emit the last sample.
	data := bytes.Join([][]byte{
		[]byte(`{"type":"assistant","timestamp":"2026-05-13T08:00:01Z","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`),
	}, []byte("\n"))
	// No trailing \n.

	var n int
	consumed, err := ParseReader(bytes.NewReader(data), func(s Sample) { n++ }, nil)
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d samples, want 1", n)
	}
	if consumed != int64(len(data)) {
		t.Errorf("consumed %d, want %d", consumed, len(data))
	}
}

func TestParseReader_HandlesGarbageLinesSilently(t *testing.T) {
	// Inline string with non-JSON lines should be skipped, not returned as error.
	data := strings.Join([]string{
		`garbage`,
		`{"type":"assistant","timestamp":"2026-05-13T08:00:01Z","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`,
		`more garbage`,
		``,
	}, "\n")

	var samples []Sample
	if _, err := ParseReader(bytes.NewReader([]byte(data)), func(s Sample) {
		samples = append(samples, s)
	}, nil); err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1", len(samples))
	}
}
