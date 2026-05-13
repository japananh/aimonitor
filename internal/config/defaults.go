package config

import "time"

// DefaultConfig returns the config aimonitor starts with on a fresh
// install. Used by Load() when the YAML file is missing.
func DefaultConfig() Config {
	return Config{
		AutoSwitch:               false,
		Thresholds:               []int{40, 60, 100},
		AutoSwitchCooldownSeconds: 60,
		AutoStart:                false,
	}
}

// CooldownDuration returns the auto-switch cool-down as a time.Duration.
func (c Config) CooldownDuration() time.Duration {
	if c.AutoSwitchCooldownSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.AutoSwitchCooldownSeconds) * time.Second
}
