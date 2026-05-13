package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.yaml")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := DefaultConfig()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	in := Config{
		AutoSwitch:                true,
		Thresholds:                []int{30, 50, 80},
		AutoSwitchCooldownSeconds: 120,
		AutoStart:                 true,
	}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("got %+v, want %+v", out, in)
	}
}

func TestLoad_PartialFileMergesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// User-edited config that only sets autoswitch.
	if err := os.WriteFile(path, []byte("autoswitch: true\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.AutoSwitch {
		t.Errorf("autoswitch should be true")
	}
	// Other fields should retain defaults.
	def := DefaultConfig()
	if !reflect.DeepEqual(got.Thresholds, def.Thresholds) {
		t.Errorf("Thresholds = %v, want default %v", got.Thresholds, def.Thresholds)
	}
	if got.AutoSwitchCooldownSeconds != def.AutoSwitchCooldownSeconds {
		t.Errorf("cooldown = %d, want default %d", got.AutoSwitchCooldownSeconds, def.AutoSwitchCooldownSeconds)
	}
}

func TestSave_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, Config{Thresholds: []int{60, 40}}); err == nil {
		t.Error("invalid thresholds: want error from Save")
	}
}

func TestValidate(t *testing.T) {
	good := DefaultConfig()
	if err := good.Validate(); err != nil {
		t.Errorf("default Validate: %v", err)
	}

	bad := DefaultConfig()
	bad.AutoSwitchCooldownSeconds = -1
	if err := bad.Validate(); err == nil {
		t.Error("negative cooldown: want error")
	}
}

func TestDefaultPath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != "/tmp/xdg/aimonitor/config.yaml" {
		t.Errorf("got %q", got)
	}
}

func TestCooldownDuration(t *testing.T) {
	c := DefaultConfig()
	if c.CooldownDuration().Seconds() != 60 {
		t.Errorf("default cooldown = %v", c.CooldownDuration())
	}

	c.AutoSwitchCooldownSeconds = 0
	if c.CooldownDuration().Seconds() != 60 {
		t.Errorf("zero cooldown should fall back to 60s, got %v", c.CooldownDuration())
	}

	c.AutoSwitchCooldownSeconds = 5
	if c.CooldownDuration().Seconds() != 5 {
		t.Errorf("got %v", c.CooldownDuration())
	}
}
