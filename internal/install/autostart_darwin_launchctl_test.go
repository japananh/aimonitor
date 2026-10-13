//go:build darwin

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeLaunchctl scripts launchctl for EnableAutostart so no test touches the
// real launchd domain. bootout leaves the service "loaded" for unloadLag print
// calls, like launchd's asynchronous removal.
type fakeLaunchctl struct {
	calls         []string
	loaded        bool
	unloadLag     int // print calls that still report loaded after bootout
	inProgressFor int // bootstrap calls that fail with 37 before succeeding
	pauses        int
}

func (f *fakeLaunchctl) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args[0])
	switch args[0] {
	case "print":
		if !f.loaded {
			return nil, errors.New("exit status 113")
		}
		if f.unloadLag > 0 {
			f.unloadLag--
			if f.unloadLag == 0 {
				f.loaded = false
			}
		}
		return nil, nil
	case "bootout":
		if f.unloadLag == 0 {
			f.loaded = false
		}
	case "bootstrap":
		if f.inProgressFor > 0 {
			f.inProgressFor--
			return []byte("Bootstrap failed: 37: Operation already in progress"), exitError(37)
		}
		f.loaded = true
	}
	return nil, nil
}

func (f *fakeLaunchctl) count(verb string) int {
	n := 0
	for _, c := range f.calls {
		if c == verb {
			n++
		}
	}
	return n
}

func exitError(code int) error {
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}

func useFakeLaunchctl(t *testing.T, f *fakeLaunchctl) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	prevRun, prevPause := runLaunchctl, pause
	runLaunchctl = f.run
	pause = func(time.Duration) { f.pauses++ }
	t.Cleanup(func() { runLaunchctl, pause = prevRun, prevPause })
}

func TestEnableAutostart_FreshInstallBootstraps(t *testing.T) {
	f := &fakeLaunchctl{}
	useFakeLaunchctl(t, f)

	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err != nil {
		t.Fatalf("EnableAutostart: %v", err)
	}
	if !f.loaded || f.count("bootstrap") != 1 {
		t.Errorf("loaded=%v bootstrap calls=%d, want loaded after one bootstrap", f.loaded, f.count("bootstrap"))
	}
}

// A second identical call must leave a running daemon alone: bouncing it is
// how the "Start daemon" button once killed a healthy daemon.
func TestEnableAutostart_UnchangedAndLoadedIsNoOp(t *testing.T) {
	f := &fakeLaunchctl{}
	useFakeLaunchctl(t, f)
	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err != nil {
		t.Fatalf("first EnableAutostart: %v", err)
	}
	f.calls = nil

	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err != nil {
		t.Fatalf("second EnableAutostart: %v", err)
	}
	if f.count("bootout") != 0 || f.count("bootstrap") != 0 {
		t.Errorf("calls = %v, want no bootout/bootstrap for an unchanged, loaded service", f.calls)
	}
}

func TestEnableAutostart_ChangedPlistReloads(t *testing.T) {
	f := &fakeLaunchctl{}
	useFakeLaunchctl(t, f)
	if err := EnableAutostart("/old/aimonitor"); err != nil {
		t.Fatalf("first EnableAutostart: %v", err)
	}
	f.calls = nil

	if err := EnableAutostart("/new/aimonitor"); err != nil {
		t.Fatalf("second EnableAutostart: %v", err)
	}
	if f.count("bootout") != 1 || f.count("bootstrap") != 1 || !f.loaded {
		t.Errorf("calls = %v loaded=%v, want one bootout + one bootstrap", f.calls, f.loaded)
	}
	path, _ := LaunchAgentPath()
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "/new/aimonitor") {
		t.Errorf("plist not rewritten:\n%s", b)
	}
}

// bootout is asynchronous: the service must be gone before bootstrap runs,
// otherwise launchd answers 37 and the daemon is left stopped.
func TestEnableAutostart_WaitsForBootoutToFinish(t *testing.T) {
	f := &fakeLaunchctl{loaded: true, unloadLag: 3}
	useFakeLaunchctl(t, f)

	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err != nil {
		t.Fatalf("EnableAutostart: %v", err)
	}
	if f.pauses == 0 {
		t.Error("expected to wait for the service to unload before bootstrapping")
	}
	if !f.loaded {
		t.Error("service should be loaded after EnableAutostart")
	}
}

func TestEnableAutostart_RetriesBootstrapOnEALREADY(t *testing.T) {
	f := &fakeLaunchctl{inProgressFor: 3}
	useFakeLaunchctl(t, f)

	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err != nil {
		t.Fatalf("EnableAutostart: %v", err)
	}
	if f.count("bootstrap") != 4 || !f.loaded {
		t.Errorf("bootstrap calls=%d loaded=%v, want 4 attempts then loaded", f.count("bootstrap"), f.loaded)
	}
}

func TestEnableAutostart_GivesUpAfterAttempts(t *testing.T) {
	f := &fakeLaunchctl{inProgressFor: 1000}
	useFakeLaunchctl(t, f)

	if err := EnableAutostart("/opt/homebrew/bin/aimonitor"); err == nil {
		t.Fatal("expected an error when bootstrap never succeeds")
	}
	if f.count("bootstrap") != bootstrapAttempts {
		t.Errorf("bootstrap calls = %d, want %d", f.count("bootstrap"), bootstrapAttempts)
	}
}

func TestBootstrapInProgress(t *testing.T) {
	if !bootstrapInProgress(exitError(37), nil) {
		t.Error("exit 37 should count as in-progress")
	}
	if !bootstrapInProgress(errors.New("x"), []byte("Bootstrap failed: 37: Operation already in progress")) {
		t.Error("message match should count as in-progress")
	}
	if bootstrapInProgress(exitError(5), []byte("Bootstrap failed: 5: Input/output error")) {
		t.Error("an I/O error is not retriable")
	}
}
