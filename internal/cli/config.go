package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/spf13/cobra"
)

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
				cfg, err := config.Load("")
				if err != nil {
					return err
				}
				v, err := getConfigField(cfg, args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			},
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Update a configuration value (writes ~/.config/aimonitor/config.yaml)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load("")
				if err != nil {
					return err
				}
				updated, err := setConfigField(cfg, args[0], args[1])
				if err != nil {
					return err
				}
				if err := config.Save("", updated); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s\n", args[0], args[1])
				return nil
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "Print every configuration key and its current value",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load("")
				if err != nil {
					return err
				}
				for _, k := range configKeys {
					v, _ := getConfigField(cfg, k)
					fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", k, v)
				}
				return nil
			},
		},
	)
	return cmd
}

// configKeys is the canonical set of config keys aimonitor exposes via
// `aimonitor config`. New fields belong here AND in the get/set switches
// below — keeping them in one slice avoids the get/set/list trio drifting.
var configKeys = []string{
	"autoswitch",
	"thresholds",
	"autoswitch_cooldown_seconds",
	"autostart",
}

func getConfigField(c config.Config, key string) (string, error) {
	switch key {
	case "autoswitch":
		return strconv.FormatBool(c.AutoSwitch), nil
	case "thresholds":
		return config.FormatThresholds(c.Thresholds), nil
	case "autoswitch_cooldown_seconds":
		return strconv.Itoa(c.AutoSwitchCooldownSeconds), nil
	case "autostart":
		return strconv.FormatBool(c.AutoStart), nil
	default:
		return "", unknownConfigKey(key)
	}
}

func setConfigField(c config.Config, key, value string) (config.Config, error) {
	switch key {
	case "autoswitch":
		b, err := parseBool(value)
		if err != nil {
			return c, err
		}
		c.AutoSwitch = b
	case "thresholds":
		ts, err := config.ParseThresholds(value)
		if err != nil {
			return c, err
		}
		c.Thresholds = ts
	case "autoswitch_cooldown_seconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return c, fmt.Errorf("autoswitch_cooldown_seconds: not an integer: %w", err)
		}
		c.AutoSwitchCooldownSeconds = n
	case "autostart":
		b, err := parseBool(value)
		if err != nil {
			return c, err
		}
		c.AutoStart = b
	default:
		return c, unknownConfigKey(key)
	}
	return c, nil
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
