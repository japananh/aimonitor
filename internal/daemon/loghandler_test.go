package daemon

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// White-box tests for logrusHandler's WithAttrs / WithGroup / clone and the
// appendField group-recursion + quoteValue branches that the format test
// doesn't exercise.

// TestLogHandler_WithAttrsAndGroup: WithGroup prefixes record-level attr keys,
// WithAttrs preformats persistent attrs (also group-prefixed), and both are
// emitted on every line.
func TestLogHandler_WithAttrsAndGroup(t *testing.T) {
	var buf bytes.Buffer
	h := newLogrusHandler(&buf)
	l := slog.New(h).WithGroup("swap").With("component", "switcher")

	l.Info("switched", "from", "A", "to", "B")

	got := strings.TrimSpace(buf.String())
	// WithAttrs-contributed key is group-prefixed.
	if !strings.Contains(got, "swap.component=switcher") {
		t.Errorf("missing preformatted group-prefixed attr: %q", got)
	}
	// Record-level attrs are group-prefixed too.
	if !strings.Contains(got, "swap.from=A") || !strings.Contains(got, "swap.to=B") {
		t.Errorf("record attrs not group-prefixed: %q", got)
	}
}

// TestLogHandler_EmptyWithAttrsAndGroupReturnSelf: an empty WithAttrs / empty
// WithGroup are no-ops that return the same handler (no clone).
func TestLogHandler_EmptyWithAttrsAndGroupReturnSelf(t *testing.T) {
	h := newLogrusHandler(&bytes.Buffer{})
	if h.WithAttrs(nil) != h {
		t.Errorf("WithAttrs(nil) should return the receiver unchanged")
	}
	if h.WithGroup("") != h {
		t.Errorf("WithGroup(\"\") should return the receiver unchanged")
	}
}

// TestLogHandler_NestedGroupAttr: a slog.Group value recurses with a dotted
// prefix; an empty-key attr inside is dropped.
func TestLogHandler_NestedGroupAttr(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(newLogrusHandler(&buf))

	l.Info("nested",
		slog.Group("http", slog.Int("status", 429), slog.String("", "dropped")),
	)
	got := buf.String()
	if !strings.Contains(got, "http.status=429") {
		t.Errorf("nested group attr not dotted: %q", got)
	}
	if strings.Contains(got, "dropped") {
		t.Errorf("empty-key attr should be dropped: %q", got)
	}
}

// TestLogHandler_QuoteValues: values with spaces/equals/quotes are quoted; an
// empty value renders as "".
func TestLogHandler_QuoteValues(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(newLogrusHandler(&buf))

	l.Info("msg", "spaced", "two words", "empty", "", "plain", "tok")
	got := buf.String()
	if !strings.Contains(got, `spaced="two words"`) {
		t.Errorf("value with space not quoted: %q", got)
	}
	if !strings.Contains(got, `empty=""`) {
		t.Errorf("empty value not rendered as quoted empty: %q", got)
	}
	// A plain token stays unquoted.
	if !strings.Contains(got, "plain=tok") {
		t.Errorf("plain token should stay unquoted: %q", got)
	}
}

// TestLogHandler_Disabled: a handler at INFO drops DEBUG records.
func TestLogHandler_Disabled(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(newLogrusHandler(&buf))
	l.Debug("should not appear")
	if buf.Len() != 0 {
		t.Errorf("DEBUG record should be dropped at INFO level, got %q", buf.String())
	}
	// And WARN/ERROR levels render their short codes.
	l.Warn("warned")
	l.Error("errored")
	got := buf.String()
	if !strings.Contains(got, "WARN[") || !strings.Contains(got, "ERRO[") {
		t.Errorf("level codes wrong: %q", got)
	}
}
