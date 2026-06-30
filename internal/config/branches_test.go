package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// These tests fill the remaining validation/threshold/IO-error branches that
// config_test.go and thresholds_test.go don't reach. All hermetic: temp dirs
// and t.Setenv only, no real config touched.

func TestLoad_EmptyPathUsesXDGDefault_Missing(t *testing.T) {
	// path=="" → DefaultPath() (XDG branch) → file absent → DefaultConfig.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if !reflect.DeepEqual(got, DefaultConfig()) {
		t.Errorf("Load(\"\") on missing file = %+v, want default", got)
	}
}

func TestSave_EmptyPathUsesXDGDefault(t *testing.T) {
	// path=="" → DefaultPath() → MkdirAll + WriteFile under the temp XDG dir.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := Save("", DefaultConfig()); err != nil {
		t.Fatalf("Save(\"\"): %v", err)
	}
	want := filepath.Join(dir, "aimonitor", "config.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected config written at %q: %v", want, err)
	}
}

func TestDefaultPath_HomeBranch(t *testing.T) {
	// XDG unset → ~/.config/aimonitor/config.yaml.
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(home, ".config", "aimonitor", "config.yaml")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Not a YAML mapping — fails Unmarshal into the Config struct.
	if err := os.WriteFile(path, []byte("\t- not: a mapping\n: : :"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("malformed YAML: want parse error from Load")
	}
}

func TestLoad_InvalidThresholdsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("thresholds: [60, 40]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("descending thresholds: want validation error from Load")
	}
	if !errors.Is(err, ErrInvalidThresholds) {
		t.Errorf("error should wrap ErrInvalidThresholds, got %v", err)
	}
}

func TestLoad_ReadErrorOnDirectory(t *testing.T) {
	// Pointing Load at a directory triggers a non-ErrNotExist read error.
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Error("Load on a directory: want read error")
	}
}

func TestSave_RejectsInvalidThresholds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, Config{Thresholds: []int{0}}); err == nil {
		t.Error("threshold 0: want validation error from Save")
	}
}

func TestSave_MkdirError(t *testing.T) {
	// Parent path is an existing regular file, so MkdirAll(dir/...) fails.
	dir := t.TempDir()
	fileAsParent := filepath.Join(dir, "afile")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fileAsParent, "config.yaml") // afile is not a dir
	if err := Save(target, DefaultConfig()); err == nil {
		t.Error("MkdirAll under a regular file: want error from Save")
	}
}

func TestValidateThresholds_Empty(t *testing.T) {
	// ParseThresholds short-circuits empty input before reaching
	// ValidateThresholds, so the empty-list branch is only reachable by
	// calling Validate directly.
	err := ValidateThresholds([]int{})
	if err == nil {
		t.Fatal("ValidateThresholds([]): want error")
	}
	if !errors.Is(err, ErrInvalidThresholds) {
		t.Errorf("error should wrap ErrInvalidThresholds, got %v", err)
	}
}
