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
	SettingsKeySlackReadOnly   = "mcp.slack.read_only"
	SettingsKeyClickUpReadOnly = "mcp.clickup.read_only"
	// SettingsKeyDisabledTools is a comma-separated list of tool names to
	// hide from tools/list (saves Claude context for tools you never use).
	SettingsKeyDisabledTools = "mcp.disabled_tools"
)

// Config is the effective MCP-server configuration.
type Config struct {
	Enabled  map[Service]bool
	ReadOnly map[Service]bool
	Disabled map[string]bool // tool name → hidden
}

// LoadConfig reads the mcp.* settings with defaults (everything enabled,
// nothing read-only, nothing disabled).
func LoadConfig(ctx context.Context, s *store.Store) (Config, error) {
	cfg := Config{
		Enabled:  map[Service]bool{ServiceSlack: true, ServiceClickUp: true},
		ReadOnly: map[Service]bool{ServiceSlack: false, ServiceClickUp: false},
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
