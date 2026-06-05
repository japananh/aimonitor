// Package mcpserver implements `aimonitor mcp serve` — a stdio MCP server
// exposing Slack and ClickUp tools to Claude Code — plus the credential
// plumbing (`aimonitor mcp connect/disconnect/status`) behind it.
//
// Design notes (docs kept out of the repo deliberately):
//   - One approval layer: Claude Code's own per-tool permission prompts.
//     No write-gate, no sockets, no widget IPC.
//   - Tokens live in the OS keyring under aimonitor's own service names;
//     `connect` migrates claude-bar's entries when present so replacing
//     that tool needs no re-setup.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strings"

	"github.com/japananh/aimonitor/internal/secret"
)

// Service identifies an integration this MCP server can expose.
type Service string

const (
	ServiceSlack   Service = "slack"
	ServiceClickUp Service = "clickup"
)

// Services lists every supported integration, in display order.
var Services = []Service{ServiceSlack, ServiceClickUp}

// ParseService validates a user-supplied service name.
func ParseService(s string) (Service, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "slack":
		return ServiceSlack, nil
	case "clickup":
		return ServiceClickUp, nil
	default:
		return "", fmt.Errorf("unknown service %q (want slack or clickup)", s)
	}
}

// keyringService is the keychain service name for our copy of a token.
// Account is the OS username, matching the rest of aimonitor's entries.
func keyringService(svc Service) string {
	return "aimonitor-mcp:" + string(svc)
}

// claudeBarService is where claude-bar keeps the same token
// ("claude-bar-mcp:shared:<svc>", account = OS username, raw token payload).
// Read-only: we never write to or delete claude-bar's entries.
func claudeBarService(svc Service) string {
	return "claude-bar-mcp:shared:" + string(svc)
}

// CredStore reads/writes integration tokens in the OS keyring.
type CredStore struct {
	Ring secret.Keyring
	User string
}

// NewCredStore builds the production store (OS keyring + current user).
func NewCredStore() (*CredStore, error) {
	ring, err := secret.Default()
	if err != nil {
		return nil, fmt.Errorf("init keyring: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("os user: %w", err)
	}
	return &CredStore{Ring: ring, User: u.Username}, nil
}

// Token returns the stored token for svc, or "" (no error) when absent.
func (c *CredStore) Token(svc Service) (string, error) {
	b, err := c.Ring.Get(keyringService(svc), c.User)
	if errors.Is(err, secret.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s token: %w", svc, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Store saves the token for svc, overwriting any previous one.
func (c *CredStore) Store(svc Service, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("empty %s token", svc)
	}
	if err := c.Ring.Set(keyringService(svc), c.User, []byte(token)); err != nil {
		return fmt.Errorf("store %s token: %w", svc, err)
	}
	return nil
}

// Delete removes the stored token for svc. Missing entries are not an error.
func (c *CredStore) Delete(svc Service) error {
	err := c.Ring.Delete(keyringService(svc), c.User)
	if err != nil && !errors.Is(err, secret.ErrNotFound) {
		return fmt.Errorf("delete %s token: %w", svc, err)
	}
	return nil
}

// MigrateFromClaudeBar copies svc's token from claude-bar's keychain entry
// into ours, verifying it first. Returns ("", nil) when claude-bar has no
// entry — callers fall back to prompting for a pasted token. claude-bar's
// entry is left untouched so both tools keep working during the transition.
func (c *CredStore) MigrateFromClaudeBar(ctx context.Context, svc Service, verify func(ctx context.Context, token string) (string, error)) (identity string, err error) {
	b, err := c.Ring.Get(claudeBarService(svc), c.User)
	if errors.Is(err, secret.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		// A read failure (e.g. the user denied the keychain ACL prompt) is
		// reported so the CLI can fall back to paste with an explanation.
		return "", fmt.Errorf("read claude-bar's %s entry: %w", svc, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", nil
	}
	ident, err := verify(ctx, token)
	if err != nil {
		return "", fmt.Errorf("claude-bar's %s token failed verification: %w", svc, err)
	}
	if err := c.Store(svc, token); err != nil {
		return "", err
	}
	return ident, nil
}
