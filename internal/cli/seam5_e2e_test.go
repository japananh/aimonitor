package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Coverage for the PRE-network region of usage refresh and a few stash/create
// failure mop-ups. The usage fetch needs an access token from the stash, so an
// account with NO stash fails at the stash read BEFORE any HTTP — fully
// hermetic. The network success / active-refresh paths stay uncovered (no
// client injection seam; covered at the daemon level). No t.Parallel.

// ---- usage refresh: pre-network failure (no stash) --------------------------

// TestUsageRefresh_AllNoStashFail: two accounts with rows but no stash both fail
// at the stash read (before the fetcher), so the run reports them failed and
// the no-skip summary line. Empty live slot → ResolveActiveAccount finds no
// active account (no network).
func TestUsageRefresh_AllNoStashFail(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "a1"}, nil)
	seedAccount(t, dbPath, store.Account{Label: "a2"}, nil)

	out, err := runCLI(t, "", "usage", "refresh")
	if err != nil {
		t.Fatalf("usage refresh (no stash): %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "2 failed") {
		t.Errorf("both no-stash accounts should fail before any network\n%s", out)
	}
	// No skip → the non-skip summary form.
	if strings.Contains(out, "skipped") {
		t.Errorf("no account is active+skipped here\n%s", out)
	}
}

// TestUsageRefresh_InactiveSkipsActive: with --inactive, the active account
// (stash byte-matches the live slot) is skipped BEFORE RefreshActiveUsage, and
// the inactive no-stash account fails at the stash read. Covers the skip branch
// + the skip-summary form.
func TestUsageRefresh_InactiveSkipsActive(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	blob := validBlob("sk-active")
	seedAccount(t, dbPath, store.Account{Label: "live"}, blob)
	seedAccount(t, dbPath, store.Account{Label: "idle"}, nil) // no stash → fails
	// Make "live" the active account: live slot == its stash.
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, blob); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}

	out, err := runCLI(t, "", "usage", "refresh", "--inactive")
	if err != nil {
		t.Fatalf("usage refresh --inactive: %v (output: %q)", err, out)
	}
	if !strings.Contains(out, "skipped (active)") {
		t.Errorf("the active account should be skipped\n%s", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("the inactive no-stash account should fail\n%s", out)
	}
}

// TestUsageRefreshOne_NoStashErrors: `usage refresh <label>` on an account with
// no stash returns a non-nil error (the single-account FAIL contract), covering
// runUsageRefreshOne's body up to the fetch.
func TestUsageRefreshOne_NoStashErrors(t *testing.T) {
	_, dbPath := e2eEnv(t)
	seedAccount(t, dbPath, store.Account{Label: "solo"}, nil)

	_, err := runCLI(t, "", "usage", "refresh", "solo")
	if err == nil || !strings.Contains(err.Error(), "refresh \"solo\"") {
		t.Fatalf("expected a per-label refresh error, got %v", err)
	}
}

// ---- remove: DeleteStash failure --------------------------------------------

// TestRemove_DeleteStashFails: the stash delete fails (erroring claude seam), so
// remove keeps the registry row (a deleted row + leaked stash is the worse,
// invisible outcome) and returns the delete error.
func TestRemove_DeleteStashFails(t *testing.T) {
	ring, dbPath := e2eEnv(t)
	ctx := context.Background()
	seedAccount(t, dbPath, store.Account{Label: "stuck"}, validBlob("sk-stuck"))
	// A different account is active so "stuck" is provably inactive (the
	// isActiveAccount read uses Get, which still works on the erroring ring).
	if err := ring.Set(claude.ClaudeCodeService, claude.KeychainUserForTest, validBlob("sk-other")); err != nil {
		t.Fatalf("seed live slot: %v", err)
	}
	// Override the claude seam so DeleteStash fails (Get still delegates).
	t.Cleanup(claude.SetKeyringForTest(&errKeyring{delegate: ring, failDelete: true}))

	_, err := runCLI(t, "", "remove", "stuck", "--yes")
	if err == nil || !strings.Contains(err.Error(), "delete keyring stash") {
		t.Fatalf("expected a delete-stash error, got %v", err)
	}
	// Row preserved for a retry.
	s := openStoreAt(t, dbPath)
	if _, err := s.GetAccountByLabel(ctx, "stuck"); err != nil {
		t.Errorf("the registry row should be kept when the stash delete fails: %v", err)
	}
}
