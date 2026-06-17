package mcpserver

import (
	"strings"
	"testing"
)

func TestSlackEnvelopeCheck_MissingScope(t *testing.T) {
	e := slackEnvelope{OK: false, Error: "missing_scope", Needed: "users:read", Provided: "search:read,chat:write"}
	err := e.check()
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	// The actionable message must name the missing scope, the fix, and what
	// the token currently has.
	for _, want := range []string{`"users:read"`, "OAuth & Permissions", "aimonitor mcp connect slack", "search:read,chat:write"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestSlackEnvelopeCheck_OtherErrorsUnchanged(t *testing.T) {
	// A non-scope error keeps the plain "slack: <error>" form.
	if got := (slackEnvelope{Error: "channel_not_found"}).check(); got == nil || got.Error() != "slack: channel_not_found" {
		t.Errorf("check() = %v, want 'slack: channel_not_found'", got)
	}
	// missing_scope with no `needed` (shouldn't happen, but be safe) falls
	// back to the plain form rather than printing an empty scope.
	if got := (slackEnvelope{Error: "missing_scope"}).check(); got == nil || got.Error() != "slack: missing_scope" {
		t.Errorf("check() = %v, want 'slack: missing_scope'", got)
	}
	if err := (slackEnvelope{OK: true}).check(); err != nil {
		t.Errorf("ok=true should be nil, got %v", err)
	}
}

func TestSlackScopesCSV(t *testing.T) {
	csv := SlackScopesCSV()
	for _, want := range []string{"users:read", "search:read", "chat:write", "files:write"} {
		if !strings.Contains(csv, want) {
			t.Errorf("SlackScopesCSV() = %q missing %q", csv, want)
		}
	}
}
