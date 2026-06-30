package version

import "testing"

// The version package exposes only build-time-injected string vars with
// compiled-in defaults. There are no functions to exercise, so coverage of
// "statements" is N/A; this test just pins the documented defaults that ship
// when no -ldflags are passed (go run / plain go build).
func TestDefaults(t *testing.T) {
	if Version != "dev" {
		t.Errorf("Version default = %q, want %q", Version, "dev")
	}
	if Commit != "none" {
		t.Errorf("Commit default = %q, want %q", Commit, "none")
	}
	if BuildDate != "unknown" {
		t.Errorf("BuildDate default = %q, want %q", BuildDate, "unknown")
	}
}
