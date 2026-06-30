package cli

import (
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Last reachable mop-ups. No t.Parallel.

// TestStatus_ActiveAccountHasNoStash: an account row exists but carries no
// stash, while the live slot is active. findMatchingAccount's RetrieveStash
// fails for that row → the `continue` branch → no match → status reports the
// active credential is unrecognized. Distinct from TestStatus_ActiveNotStashed
// (which has a stash that simply doesn't byte-match).
func TestStatus_ActiveAccountHasNoStash(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	// Row with a KeyringRef but NO stashed bytes (blob nil).
	seedAccount(t, dbPath, store.Account{Label: "stashless", KeyringRef: "ref-stashless"}, nil)
	// A live credential is active.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-live")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "status")
	if err != nil {
		t.Fatalf("status: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "not one of aimonitor's stashed accounts") {
		t.Errorf("status should report the active credential is unrecognized\n%s", out)
	}
}

// TestList_WithStashlessAccount: `list` over an account whose stash is missing
// exercises list's stash-miss path (stashMatches returns false on a read error).
func TestList_WithStashlessAccount(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "withstash"}, validBlob("sk-have"))
	seedAccount(t, dbPath, store.Account{Label: "nostash", KeyringRef: "ref-nostash"}, nil)
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-have")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "list")
	if err != nil {
		t.Fatalf("list: %v (output: %q)", err, out)
	}
	for _, want := range []string{"withstash", "nostash"} {
		if !strings.Contains(out, want) {
			t.Errorf("list should show %q\n%s", want, out)
		}
	}
}
