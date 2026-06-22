package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/install"
	"github.com/japananh/aimonitor/internal/mcpserver"
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

// Update-related settings, SQLite-backed like the auto_swap.* family. The
// menu-bar widget reads/writes these to gate its update checks; the daemon
// does not consume them.
const (
	// SettingsKeyAutoUpdateEnabled gates the widget's automatic update
	// checks (check-and-notify; never an unattended install).
	SettingsKeyAutoUpdateEnabled = "auto_update.enabled"
	// SettingsKeyUpdateSkippedVersion records a release the user chose to
	// skip, so the widget stops prompting for that one version.
	SettingsKeyUpdateSkippedVersion = "update.skipped_version"
)

// defaultAutoUpdateEnabled is the fallback when auto_update.enabled is
// unset: checking for updates is safe (no install), so default it on.
const defaultAutoUpdateEnabled = true

// configKeys is the canonical set of keys `aimonitor config` exposes.
// Two storage backends sit behind them:
//   - autostart lives in the YAML config (and drives the LaunchAgent).
//   - the auto_swap.* / auto_update.* / update.* keys live in the SQLite
//     settings table, which is where the daemon's AutoSwapper and the
//     menu-bar widget read them — so a `config set` takes effect without a
//     restart.
var configKeys = []string{
	"autostart",
	daemon.SettingsKeyAutoSwapEnabled,
	daemon.SettingsKeyAutoSwapThreshold,
	daemon.SettingsKeyAutoSwapThreshold7d,
	daemon.SettingsKeyAutoSwapGrace,
	daemon.SettingsKeyAutoSwapExcluded,
	daemon.SettingsKeyNotifyEnabled,
	daemon.SettingsKeyNotifyWarnPct,
	daemon.SettingsKeyNotifyCritPct,
	daemon.SettingsKeyDailySummaryEnabled,
	SettingsKeyAutoUpdateEnabled,
	SettingsKeyUpdateSkippedVersion,
	mcpserver.SettingsKeySlackEnabled,
	mcpserver.SettingsKeyClickUpEnabled,
	mcpserver.SettingsKeySlackReadOnly,
	mcpserver.SettingsKeyClickUpReadOnly,
	mcpserver.SettingsKeyDisabledTools,
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
		daemon.SettingsKeyAutoSwapThreshold7d,
		daemon.SettingsKeyAutoSwapGrace,
		daemon.SettingsKeyAutoSwapExcluded,
		daemon.SettingsKeyNotifyEnabled,
		daemon.SettingsKeyNotifyWarnPct,
		daemon.SettingsKeyNotifyCritPct,
		daemon.SettingsKeyDailySummaryEnabled,
		SettingsKeyAutoUpdateEnabled,
		SettingsKeyUpdateSkippedVersion,
		mcpserver.SettingsKeySlackEnabled,
		mcpserver.SettingsKeyClickUpEnabled,
		mcpserver.SettingsKeySlackReadOnly,
		mcpserver.SettingsKeyClickUpReadOnly,
		mcpserver.SettingsKeyDisabledTools:
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
		newConfigAuditCmd(),
		newConfigExportCmd(),
		newConfigImportCmd(),
	)
	return cmd
}

// newConfigAuditCmd shows recent configuration changes recorded in
// config_audit — useful for tracing when/how a setting flipped (e.g. an MCP
// integration's `enabled` going to false).
func newConfigAuditCmd() *cobra.Command {
	var limit int
	var key string
	c := &cobra.Command{
		Use:   "audit",
		Short: "Show recent configuration changes (from `config set` and import)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openConfigStore()
			if err != nil {
				return err
			}
			defer s.Close()
			rows, err := s.ListConfigAudit(cmd.Context(), key, limit)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No configuration changes recorded yet.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WHEN\tKEY\tOLD\tNEW\tSOURCE")
			for _, r := range rows {
				old := r.OldValue
				if old == "" {
					old = "(unset)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					r.Ts.Local().Format("2006-01-02 15:04:05"), r.Key, old, r.NewValue, r.Source)
			}
			return w.Flush()
		},
	}
	c.Flags().IntVar(&limit, "limit", 50, "max rows to show")
	c.Flags().StringVar(&key, "key", "", "only show changes to this key")
	return c
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
	defer s.Close()

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
	defer s.Close()
	// Capture the prior value so the change is traceable. "" when unset (a
	// not-found GetSetting error counts as unset).
	old, _ := s.GetSetting(ctx, key)
	if err := s.PutSetting(ctx, key, norm); err != nil {
		return err
	}
	// Best-effort audit — never fail a successful write because the audit row
	// couldn't be recorded.
	_ = s.InsertConfigAudit(ctx, store.ConfigAuditRecord{Key: key, OldValue: old, NewValue: norm, Source: "cli"})
	return nil
}

func validateStoreValue(key, value string) (string, error) {
	switch key {
	case daemon.SettingsKeyAutoSwapEnabled:
		b, err := parseBool(value)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	case daemon.SettingsKeyAutoSwapThreshold, daemon.SettingsKeyAutoSwapThreshold7d:
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
	case daemon.SettingsKeyAutoSwapExcluded:
		// Comma-separated account IDs to exclude as auto-swap targets.
		// Normalise: parse each as an int64, drop blanks/dupes, sort
		// ascending. Empty is valid and means "exclude nothing".
		seen := map[int64]bool{}
		var ids []int64
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			id, perr := strconv.ParseInt(p, 10, 64)
			if perr != nil {
				return "", fmt.Errorf("%s: not an account id: %q", key, p)
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.FormatInt(id, 10)
		}
		return strings.Join(parts, ","), nil
	case daemon.SettingsKeyNotifyEnabled:
		b, err := parseBool(value)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	case daemon.SettingsKeyNotifyWarnPct, daemon.SettingsKeyNotifyCritPct:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", fmt.Errorf("%s: not a number: %q", key, value)
		}
		if f <= 0 || f > 100 {
			return "", fmt.Errorf("%s: must be in (0, 100], got %v", key, f)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case daemon.SettingsKeyDailySummaryEnabled,
		SettingsKeyAutoUpdateEnabled,
		mcpserver.SettingsKeySlackEnabled,
		mcpserver.SettingsKeyClickUpEnabled,
		mcpserver.SettingsKeySlackReadOnly,
		mcpserver.SettingsKeyClickUpReadOnly:
		b, err := parseBool(value)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	case mcpserver.SettingsKeyDisabledTools:
		// Free-form comma-separated tool names; normalise whitespace.
		parts := []string{}
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, ","), nil
	case SettingsKeyUpdateSkippedVersion:
		// A version tag the widget should stop prompting for. Free-form
		// (a tag string); empty clears it.
		return strings.TrimSpace(value), nil
	}
	return "", unknownConfigKey(key)
}

func storeKeyDefault(key string) string {
	switch key {
	case daemon.SettingsKeyAutoSwapEnabled:
		return strconv.FormatBool(daemon.DefaultAutoSwapEnabled)
	case daemon.SettingsKeyAutoSwapThreshold:
		return strconv.FormatFloat(daemon.DefaultAutoSwapThreshold5h, 'f', -1, 64)
	case daemon.SettingsKeyAutoSwapThreshold7d:
		return strconv.FormatFloat(daemon.DefaultAutoSwapThreshold7d, 'f', -1, 64)
	case daemon.SettingsKeyAutoSwapGrace:
		return strconv.Itoa(daemon.DefaultAutoSwapGraceSec)
	case daemon.SettingsKeyAutoSwapExcluded:
		// Empty = exclude nothing (every account is an eligible target).
		return ""
	case daemon.SettingsKeyNotifyEnabled:
		return strconv.FormatBool(daemon.DefaultNotifyEnabled)
	case daemon.SettingsKeyNotifyWarnPct:
		return strconv.FormatFloat(daemon.DefaultNotifyWarnPct, 'f', -1, 64)
	case daemon.SettingsKeyNotifyCritPct:
		return strconv.FormatFloat(daemon.DefaultNotifyCritPct, 'f', -1, 64)
	case daemon.SettingsKeyDailySummaryEnabled:
		return strconv.FormatBool(daemon.DefaultDailySummaryEnabled)
	case SettingsKeyAutoUpdateEnabled:
		return strconv.FormatBool(defaultAutoUpdateEnabled)
	case mcpserver.SettingsKeySlackEnabled, mcpserver.SettingsKeyClickUpEnabled:
		return "true"
	case mcpserver.SettingsKeySlackReadOnly, mcpserver.SettingsKeyClickUpReadOnly:
		return "false"
	case mcpserver.SettingsKeyDisabledTools:
		return ""
	case SettingsKeyUpdateSkippedVersion:
		return ""
	}
	return ""
}

// openConfigStore opens the settings store for `config` and `mcp` operations.
// It delegates to openStore so it honors AIMONITOR_STORE_PATH — previously it
// always used DefaultPath, silently ignoring the override. That inconsistency
// was a footgun: a `config set` run with AIMONITOR_STORE_PATH pointed at a temp
// DB still wrote the REAL store (e.g. flipping mcp.<svc>.enabled), instead of
// staying isolated like the rest of the CLI.
func openConfigStore() (*store.Store, error) {
	return openStore()
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
