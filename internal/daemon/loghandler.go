package daemon

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// logrusHandler is a slog.Handler that renders records in logrus's
// TTY/FullTimestamp text style rather than slog's logfmt:
//
//	INFO[2026-06-08T01:23:45+07:00] daemon started                               pid=4242
//	WARN[2026-06-08T01:23:46+07:00] usage throttled                              status=429 wait=5m0s
//
// Layout: a 4-char upper-case level, the full timestamp in [RFC3339], the
// message (left-padded to 44 cols when fields follow, so keys align), then
// space-separated key=value attrs (quoted when the value needs it). Output is
// plain — no ANSI colors — because the daemon log is a file and reads the same
// in a terminal.
//
// Why a custom handler and not slog.TextHandler: TextHandler can only emit
// `time=… level=… msg=…` logfmt; this format is what the team reads logs in.
type logrusHandler struct {
	mu  *sync.Mutex // shared across clones so writes to one w serialize
	w   io.Writer
	lvl slog.Leveler

	// preformatted holds "key=value" strings contributed by WithAttrs, with
	// any active group prefix already applied. group is the WithGroup prefix
	// (e.g. "swap.") applied to record-level attrs at Handle time. aimonitor
	// doesn't currently use either, but supporting them keeps the handler a
	// correct drop-in.
	preformatted []string
	group        string
}

// newLogrusHandler builds a handler writing to w at INFO and above.
func newLogrusHandler(w io.Writer) *logrusHandler {
	return &logrusHandler{mu: &sync.Mutex{}, w: w, lvl: slog.LevelInfo}
}

func (h *logrusHandler) Enabled(_ context.Context, l slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.lvl != nil {
		minLevel = h.lvl.Level()
	}
	return l >= minLevel
}

func (h *logrusHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(levelText(r.Level))
	b.WriteByte('[')
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	b.WriteString(ts.Format(time.RFC3339))
	b.WriteString("] ")

	fields := make([]string, 0, r.NumAttrs()+len(h.preformatted))
	fields = append(fields, h.preformatted...)
	r.Attrs(func(a slog.Attr) bool {
		appendField(h.group, a, &fields)
		return true
	})

	if len(fields) > 0 {
		// Pad the message so attrs line up across lines, logrus-style. Only
		// when there ARE attrs — a bare message keeps no trailing padding.
		msg := r.Message
		if len(msg) < 44 {
			msg += strings.Repeat(" ", 44-len(msg))
		}
		b.WriteString(msg)
		b.WriteByte(' ')
		b.WriteString(strings.Join(fields, " "))
	} else {
		b.WriteString(r.Message)
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *logrusHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	nh := h.clone()
	for _, a := range as {
		appendField(h.group, a, &nh.preformatted)
	}
	return nh
}

func (h *logrusHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	nh.group = h.group + name + "."
	return nh
}

func (h *logrusHandler) clone() *logrusHandler {
	pre := make([]string, len(h.preformatted))
	copy(pre, h.preformatted)
	return &logrusHandler{
		mu:           h.mu, // shared lock + writer
		w:            h.w,
		lvl:          h.lvl,
		preformatted: pre,
		group:        h.group,
	}
}

// levelText returns logrus's 4-char upper-case level code: INFO, WARN, ERRO,
// DEBU. Falls back to the full upper string for exotic custom levels.
func levelText(l slog.Level) string {
	s := strings.ToUpper(l.String())
	if len(s) >= 4 {
		return s[:4]
	}
	return s
}

// appendField renders one attr as key=value (group-prefixed, value quoted
// when needed) and appends it to out. Groups recurse with a dotted prefix.
func appendField(prefix string, a slog.Attr, out *[]string) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		gp := prefix
		if a.Key != "" {
			gp = prefix + a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			appendField(gp, ga, out)
		}
		return
	}
	if a.Key == "" {
		return
	}
	*out = append(*out, prefix+a.Key+"="+quoteValue(a.Value.String()))
}

// quoteValue quotes s the way logrus does — only when it would otherwise be
// ambiguous: empty, or containing a space, quote, or '='. Plain tokens stay
// unquoted so common lines read cleanly.
func quoteValue(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}
