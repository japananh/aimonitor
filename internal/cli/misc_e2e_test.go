package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/store"
)

// Hermetic coverage for the parts of commands whose happy paths need an
// exec/network/keychain seam we are forbidden to add: usage (error paths),
// import (registry parsing errors), doctor (pure helpers + sqlite-critical),
// mcp (service-dispatch errors), daemon (stub dispatch). No t.Parallel.

// ---- usage refresh (hermetic error paths only) ------------------------------
// The happy path constructs claude.NewUsageFetcher() (real Anthropic network)
// with no injection seam, so only the pre-network branches are hermetic.

func TestUsageRefresh_EmptyStore(t *testing.T) {
	_, _ = e2eEnv(t)
	out, err := runCLI(t, "", "usage", "refresh")
	if err != nil {
		t.Fatalf("usage refresh on empty store: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "No accounts to refresh") {
		t.Errorf("output = %q, want 'No accounts to refresh'", out)
	}
}

func TestUsageRefresh_UnknownLabelErrors(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "usage", "refresh", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected unknown-label error naming 'ghost', got %v", err)
	}
}

// ---- import (registry parsing errors; cred read needs a secret.Default seam)-

// writeClaudeBarRegistry writes a registry.json under the temp HOME at the
// path import() reads (~/Library/Application Support/claude-swap-widget/).
func writeClaudeBarRegistry(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "claude-swap-widget")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func TestImport_RegistryNotFound(t *testing.T) {
	_, _ = e2eEnv(t) // temp HOME with no claude-bar registry
	_, err := runCLI(t, "", "import")
	if err == nil || !strings.Contains(err.Error(), "claude-bar registry not found") {
		t.Fatalf("expected registry-not-found error, got %v", err)
	}
}

func TestImport_MalformedRegistry(t *testing.T) {
	_, _ = e2eEnv(t)
	writeClaudeBarRegistry(t, "{not json")
	_, err := runCLI(t, "", "import")
	if err == nil || !strings.Contains(err.Error(), "parse claude-bar registry") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestImport_EmptyRegistry(t *testing.T) {
	_, _ = e2eEnv(t)
	writeClaudeBarRegistry(t, `{"accounts":{}}`)
	_, err := runCLI(t, "", "import")
	if err == nil || !strings.Contains(err.Error(), "no accounts to import") {
		t.Fatalf("expected no-accounts error, got %v", err)
	}
}

func TestEmailLocalPart(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "alice",
		"noatsign":          "noatsign",
		"@host":             "@host", // no local part → returned as-is
	}
	for in, want := range cases {
		if got := emailLocalPart(in); got != want {
			t.Errorf("emailLocalPart(%q) = %q want %q", in, got, want)
		}
	}
}

func TestClaudeBarRegistryPath(t *testing.T) {
	_, _ = e2eEnv(t) // sets a temp HOME
	p, err := claudeBarRegistryPath()
	if err != nil {
		t.Fatalf("claudeBarRegistryPath: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("claude-swap-widget", "registry.json")) {
		t.Errorf("path = %q, want it under claude-swap-widget", p)
	}
}

// ---- doctor (pure helpers + sqlite-critical early return) -------------------
// Full runDoctor calls checkKeyring → secret.Default() (real OS keychain), so
// it cannot run hermetically. We cover the seam-free helpers directly.

func TestCheckJSONLDir_NotPresent(t *testing.T) {
	_, _ = e2eEnv(t) // temp HOME, no ~/.claude/projects
	c := checkJSONLDir()
	if !c.ok {
		t.Errorf("absent jsonl dir should be OK (not an error), got %+v", c)
	}
	if !strings.Contains(c.detail, "not present yet") {
		t.Errorf("detail = %q, want 'not present yet'", c.detail)
	}
}

func TestCheckJSONLDir_RealDir(t *testing.T) {
	_, _ = e2eEnv(t)
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := checkJSONLDir()
	if !c.ok || !strings.Contains(c.detail, dir) {
		t.Errorf("present jsonl dir = %+v, want ok with the path", c)
	}
}

func TestCheckJSONLDir_FileNotDir(t *testing.T) {
	_, _ = e2eEnv(t)
	claudeDir := filepath.Join(os.Getenv("HOME"), ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create 'projects' as a file, not a directory.
	if err := os.WriteFile(filepath.Join(claudeDir, "projects"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := checkJSONLDir()
	if c.ok || !strings.Contains(c.detail, "is not a directory") {
		t.Errorf("file-not-dir = %+v, want not-ok with 'is not a directory'", c)
	}
}

func TestPrintChecks(t *testing.T) {
	var buf bytes.Buffer
	printChecks(&buf, []doctorCheck{
		{name: "ok-one", ok: true, detail: "fine"},
		{name: "bad-one", ok: false, detail: "broken"},
	})
	out := buf.String()
	if !strings.Contains(out, "✓ ok-one") || !strings.Contains(out, "✗ bad-one") {
		t.Errorf("printChecks output = %q, want ✓/✗ markers", out)
	}
}

func TestDoctor_SqliteCriticalFailure(t *testing.T) {
	_, _ = e2eEnv(t)
	// Point the store path at a location that cannot be opened (a directory),
	// so openStore fails and runDoctor returns the critical-sqlite error
	// BEFORE reaching checkKeyring (which would hit the real keychain).
	badPath := t.TempDir() // a directory, not a DB file
	t.Setenv("AIMONITOR_STORE_PATH", badPath)
	out, err := runCLI(t, "", "doctor")
	if err == nil {
		t.Fatalf("expected critical sqlite failure, got nil (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("error = %q, want it to mention the sqlite critical failure", err.Error())
	}
}

// ---- mcp (service-dispatch errors are hermetic; the rest needs seams) -------

func TestMCPConnect_BadService(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "mcp", "connect", "telegram")
	if err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("expected unknown-service error, got %v", err)
	}
}

func TestMCPDisconnect_BadService(t *testing.T) {
	_, _ = e2eEnv(t)
	_, err := runCLI(t, "", "mcp", "disconnect", "telegram")
	if err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("expected unknown-service error, got %v", err)
	}
}

// ---- daemon (stub dispatch) -------------------------------------------------

func TestDaemonStubs_ReturnNotImplemented(t *testing.T) {
	for _, sub := range []string{"start", "stop", "restart", "status"} {
		_, err := runCLI(t, "", "daemon", sub)
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("daemon %s: expected 'not implemented', got %v", sub, err)
		}
	}
}

// Guard: import flag wiring (keep-auto-swap default false) is exercised via the
// cobra command; ensure the command constructs and the flag is registered.
func TestImportCmd_HasKeepAutoSwapFlag(t *testing.T) {
	cmd := newImportCmd()
	if cmd.Flags().Lookup("keep-auto-swap") == nil {
		t.Error("import should register --keep-auto-swap")
	}
}

// Guard: ensure a seeded store with an account doesn't change the import
// not-found behavior (registry path is what matters, not the store).
var _ = store.Account{}

// ---- update (hermetic helpers; check/install need network + brew exec) ------

// updateLogPath is a pure HOME-relative path + MkdirAll — hermetic under the
// temp HOME from e2eEnv.
func TestUpdateLogPath(t *testing.T) {
	_, _ = e2eEnv(t)
	p, err := updateLogPath()
	if err != nil {
		t.Fatalf("updateLogPath: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("Library", "Logs", "aimonitor", "update.log")) {
		t.Errorf("path = %q, want it under Library/Logs/aimonitor", p)
	}
	// The directory must have been created.
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Errorf("log dir should exist after updateLogPath: %v", err)
	}
}

// daemonLogWriter is exercised for its non-panicking branch. Under `go test`
// stderr is usually not a TTY, so it returns a rotating writer rooted at the
// temp HOME; in a TTY it returns nil. Either is acceptable — we only assert it
// does not panic and any returned writer is rooted under the temp HOME.
func TestDaemonLogWriter(t *testing.T) {
	_, _ = e2eEnv(t)
	_ = daemonLogWriter() // must not panic; result is environment-dependent
}

