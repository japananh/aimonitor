package mcpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/japananh/aimonitor/internal/store"
)

// Settings keys (SQLite settings table — same store the daemon uses, so
// `aimonitor config set` takes effect without any restart of the widget;
// the MCP server reads them once at serve start since tool lists are
// fixed per MCP session anyway).
const (
	SettingsKeySlackEnabled    = "mcp.slack.enabled"
	SettingsKeyClickUpEnabled  = "mcp.clickup.enabled"
	SettingsKeySentryEnabled   = "mcp.sentry.enabled"
	SettingsKeySlackReadOnly   = "mcp.slack.read_only"
	SettingsKeyClickUpReadOnly = "mcp.clickup.read_only"
	SettingsKeySentryReadOnly  = "mcp.sentry.read_only"
	// Sentry is org-scoped and host-configurable (unlike the single-tenant
	// Slack/ClickUp tokens): the org slug and API host live in settings.
	SettingsKeySentryOrg     = "mcp.sentry.org"
	SettingsKeySentryBaseURL = "mcp.sentry.base_url"
	// SettingsKeyDisabledTools is a comma-separated list of tool names to
	// hide from tools/list (saves Claude context for tools you never use).
	SettingsKeyDisabledTools = "mcp.disabled_tools"
)

// Config is the effective MCP-server configuration.
type Config struct {
	Enabled  map[Service]bool
	ReadOnly map[Service]bool
	Disabled map[string]bool // tool name → hidden

	// Sentry-specific (org-scoped, host-configurable). SentryBaseURL is the
	// normalized API root (…/api/0); empty means the sentry.io default.
	SentryOrg     string
	SentryBaseURL string
}

// LoadConfig reads the mcp.* settings with defaults (everything enabled,
// nothing read-only, nothing disabled).
func LoadConfig(ctx context.Context, s *store.Store) (Config, error) {
	cfg := Config{
		Enabled:  map[Service]bool{ServiceSlack: true, ServiceClickUp: true, ServiceSentry: true},
		ReadOnly: map[Service]bool{ServiceSlack: false, ServiceClickUp: false, ServiceSentry: false},
		Disabled: map[string]bool{},
	}
	boolKey := func(key string, def bool) (bool, error) {
		v, err := s.GetSetting(ctx, key)
		if errors.Is(err, store.ErrSettingNotFound) {
			return def, nil
		}
		if err != nil {
			return def, err
		}
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return def, nil // bad value → default, never a hard failure
		}
		return b, nil
	}
	var err error
	if cfg.Enabled[ServiceSlack], err = boolKey(SettingsKeySlackEnabled, true); err != nil {
		return cfg, err
	}
	if cfg.Enabled[ServiceClickUp], err = boolKey(SettingsKeyClickUpEnabled, true); err != nil {
		return cfg, err
	}
	if cfg.ReadOnly[ServiceSlack], err = boolKey(SettingsKeySlackReadOnly, false); err != nil {
		return cfg, err
	}
	if cfg.ReadOnly[ServiceClickUp], err = boolKey(SettingsKeyClickUpReadOnly, false); err != nil {
		return cfg, err
	}
	if cfg.Enabled[ServiceSentry], err = boolKey(SettingsKeySentryEnabled, true); err != nil {
		return cfg, err
	}
	if cfg.ReadOnly[ServiceSentry], err = boolKey(SettingsKeySentryReadOnly, false); err != nil {
		return cfg, err
	}
	strKey := func(key string) (string, error) {
		v, gerr := s.GetSetting(ctx, key)
		if errors.Is(gerr, store.ErrSettingNotFound) {
			return "", nil
		}
		if gerr != nil {
			return "", gerr
		}
		return strings.TrimSpace(v), nil
	}
	if cfg.SentryOrg, err = strKey(SettingsKeySentryOrg); err != nil {
		return cfg, err
	}
	rawBase, err := strKey(SettingsKeySentryBaseURL)
	if err != nil {
		return cfg, err
	}
	cfg.SentryBaseURL = normalizeSentryBase(rawBase)
	if v, gerr := s.GetSetting(ctx, SettingsKeyDisabledTools); gerr == nil {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				cfg.Disabled[name] = true
			}
		}
	} else if !errors.Is(gerr, store.ErrSettingNotFound) {
		return cfg, gerr
	}
	return cfg, nil
}

// normalizeSentryBase turns a configured host (mcp.sentry.base_url) into the
// API root the client calls. "" stays "" (→ the sentry.io default); a bare
// host gets /api/0 appended; a value that already ends in /api/0 is left as-is.
func normalizeSentryBase(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasSuffix(raw, "/api/0") {
		return raw
	}
	return raw + "/api/0"
}
