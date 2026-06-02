package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/install"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

// applyAutostart bridges the YAML flip to the OS-level service manager
// (LaunchAgent on macOS; Linux gets a real systemd writer in Phase 5).
func applyAutostart(enable bool) error {
	if enable {
		return install.EnableAutostart("")
	}
	return install.DisableAutostart()
}

// configKeys is the canonical set of keys `aimonitor config` exposes.
// Two storage backends sit behind them:
//   - autostart lives in the YAML config (and drives the LaunchAgent).
//   - the auto_swap.* keys live in the SQLite settings table, which is
//     where the daemon's AutoSwapper actually reads them — so a `config
//     set` takes effect on the running daemon's next tick, no restart.
var configKeys = []string{
	"autostart",
	daemon.SettingsKeyAutoSwapEnabled,
	daemon.SettingsKeyAutoSwapThreshold,
	daemon.SettingsKeyAutoSwapGrace,
}

// deprecatedKeys maps retired keys to their replacement. They drove the
// old tripwire AutoSwitcher, which no longer runs (its sample handler was
// gutted when the Limits-driven AutoSwapper landed). Surfacing a pointed
// error beats silently accepting a write that does nothing.
var deprecatedKeys = map[string]string{
	"autoswitch":                  "auto_swap.enabled",
	"thresholds":                  "auto_swap.threshold_pct",
	"autoswitch_cooldown_seconds": "(removed — the auto-swap cooldown is fixed internally)",
}

// isStoreKey reports whether key is backed by the SQLite settings table
// (the auto_swap.* family) rather than the YAML config.
func isStoreKey(key string) bool {
	switch key {
	case daemon.SettingsKeyAutoSwapEnabled,
		daemon.SettingsKeyAutoSwapThreshold,
		daemon.SettingsKeyAutoSwapGrace:
		return true
	}
	return false
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Get or set aimonitor configuration values",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "get <key>",
			Short: "Print a configuration value",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				v, err := getConfigValue(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			},
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Update a configuration value",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return setConfigValue(cmd, args[0], args[1])
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "Print every configuration key and its current value",
			RunE: func(cmd *cobra.Command, args []string) error {
				for _, k := range configKeys {
					v, err := getConfigValue(cmd.Context(), k)
					if err != nil {
						v = "(error: " + err.Error() + ")"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", k, v)
				}
				return nil
			},
		},
	)
	return cmd
}

func getConfigValue(ctx context.Context, key string) (string, error) {
	if repl, dep := deprecatedKeys[key]; dep {
		return "", deprecatedKeyErr(key, repl)
	}
	if isStoreKey(key) {
		return getStoreSetting(ctx, key)
	}
	cfg, err := config.Load("")
	if err != nil {
		return "", err
	}
	switch key {
	case "autostart":
		return strconv.FormatBool(cfg.AutoStart), nil
	default:
		return "", unknownConfigKey(key)
	}
}

func setConfigValue(cmd *cobra.Command, key, value string) error {
	if repl, dep := deprecatedKeys[key]; dep {
		return deprecatedKeyErr(key, repl)
	}
	if isStoreKey(key) {
		if err := setStoreSetting(cmd.Context(), key, value); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s\n", key, value)
		return nil
	}

	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	switch key {
	case "autostart":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		cfg.AutoStart = b
		if err := config.Save("", cfg); err != nil {
			return err
		}
		// Always (re)apply the OS-level side effect, even when the YAML
		// value is unchanged. EnableAutostart/DisableAutostart are
		// idempotent (bootout+bootstrap / remove), and the cask postflight
		// depends on `config set autostart true` re-registering the
		// LaunchAgent after an upgrade's uninstall step removed it — at
		// that point YAML autostart is already true, so a change-gated
		// apply would skip it and leave the daemon stopped.
		if err := applyAutostart(b); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: autostart %v failed: %v\n", b, err)
		}
	default:
		return unknownConfigKey(key)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s\n", key, value)
	return nil
}

// getStoreSetting reads a SQLite-backed setting, returning the daemon's
// default when the key has never been written.
func getStoreSetting(ctx context.Context, key string) (string, error) {
	s, err := openConfigStore()
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()

	v, err := s.GetSetting(ctx, key)
	if errors.Is(err, store.ErrSettingNotFound) {
		return storeKeyDefault(key), nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// setStoreSetting validates value for key, then upserts it. Validation
// normalises the stored form (e.g. bool → "true"/"false") so the daemon's
// parser always sees a canonical value.
func setStoreSetting(ctx context.Context, key, value string) error {
	norm, err := validateStoreValue(key, value)
	if err != nil {
		return err
	}
	s, err := openConfigStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return s.PutSetting(ctx, key, norm)
}

func validateStoreValue(key, value string) (string, error) {
	switch key {
	case daemon.SettingsKeyAutoSwapEnabled:
		b, err := parseBool(value)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	case daemon.SettingsKeyAutoSwapThreshold:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", fmt.Errorf("%s: not a number: %q", key, value)
		}
		if f <= 0 || f > 100 {
			return "", fmt.Errorf("%s: must be in (0, 100], got %v", key, f)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case daemon.SettingsKeyAutoSwapGrace:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("%s: not an integer: %q", key, value)
		}
		if n < 0 {
			return "", fmt.Errorf("%s: must be >= 0, got %d", key, n)
		}
		return strconv.Itoa(n), nil
	}
	return "", unknownConfigKey(key)
}

func storeKeyDefault(key string) string {
	switch key {
	case daemon.SettingsKeyAutoSwapEnabled:
		return strconv.FormatBool(daemon.DefaultAutoSwapEnabled)
	case daemon.SettingsKeyAutoSwapThreshold:
		return strconv.FormatFloat(daemon.DefaultAutoSwapThreshold, 'f', -1, 64)
	case daemon.SettingsKeyAutoSwapGrace:
		return strconv.Itoa(daemon.DefaultAutoSwapGraceSec)
	}
	return ""
}

func openConfigStore() (*store.Store, error) {
	p, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(p)
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean: %q (use true|false)", s)
}

func unknownConfigKey(key string) error {
	return errors.New("unknown config key: " + key + " (try `aimonitor config list`)")
}

func deprecatedKeyErr(key, replacement string) error {
	return fmt.Errorf("config key %q is deprecated; use %s instead", key, replacement)
}
