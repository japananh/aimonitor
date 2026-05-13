// Package config holds aimonitor's user-facing configuration: thresholds
// for the auto-switch tripwires, autoswitch on/off, cool-down, etc.
// Values live on disk in a YAML file at the platform's XDG config dir;
// this package validates and parses them.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk shape of aimonitor's user config. YAML tags use
// snake_case to match conventional dotfile conventions.
type Config struct {
	// AutoSwitch enables the auto-switch engine. Default false — users
	// opt in only after they've tested manual switch and trust the
	// probe-gated decisions.
	AutoSwitch bool `yaml:"autoswitch"`

	// Thresholds is the ascending-int tripwire list (see thresholds.go
	// for validation rules). Default [40, 60, 100].
	Thresholds []int `yaml:"thresholds"`

	// AutoSwitchCooldownSeconds is the minimum gap between two auto-switch
	// decisions. Prevents thrashing.
	AutoSwitchCooldownSeconds int `yaml:"autoswitch_cooldown_seconds"`

	// AutoStart toggles the OS-level autostart (LaunchAgent on macOS,
	// systemd --user unit on Linux). This is a hint — actual install/
	// uninstall of the unit happens in the install package.
	AutoStart bool `yaml:"autostart"`
}

// DefaultPath returns the platform-appropriate config-file location.
//   - $XDG_CONFIG_HOME/aimonitor/config.yaml when XDG_CONFIG_HOME is set.
//   - ~/.config/aimonitor/config.yaml otherwise (works on both macOS and Linux).
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "aimonitor", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "aimonitor", "config.yaml"), nil
}

// Load reads the YAML at path and returns a validated Config. When the
// file does not exist, returns DefaultConfig() (no error). When the file
// exists but is malformed, returns the parse error so the caller can
// surface it instead of silently masking a broken config.
//
// The returned Config has gone through validation: Thresholds must be a
// valid ascending list, cooldown must be non-negative.
func Load(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	c := DefaultConfig() // fill defaults first so missing fields stay sane
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return c, nil
}

// Save writes c to path, creating the parent directory at 0700 and the
// file at 0600 if needed. Validation runs first.
func Save(path string, c Config) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Validate enforces invariants on c. Used by both Load and Save so we
// fail fast on bad on-disk state and refuse to persist bad in-memory state.
func (c Config) Validate() error {
	if err := ValidateThresholds(c.Thresholds); err != nil {
		return err
	}
	if c.AutoSwitchCooldownSeconds < 0 {
		return fmt.Errorf("autoswitch_cooldown_seconds must be >= 0, got %d", c.AutoSwitchCooldownSeconds)
	}
	return nil
}
