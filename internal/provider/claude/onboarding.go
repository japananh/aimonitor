package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/japananh/aimonitor/internal/provider"
)

// ClaudeCLI is the path of the Claude Code CLI binary. Override via env
// var `AIMONITOR_CLAUDE_BIN` for testing, otherwise we trust PATH lookup.
var ClaudeCLI = func() string {
	if v := os.Getenv("AIMONITOR_CLAUDE_BIN"); v != "" {
		return v
	}
	return "claude"
}

// onboardingDeps abstracts the two pieces of the world the onboarding
// flow needs: keychain ops and a way to invoke the external `claude
// login` command. Tests substitute fakes.
type onboardingDeps struct {
	keys  *keychainOps
	login func(ctx context.Context) error
}

// runClaudeLogin invokes `claude login` synchronously, inheriting stdio
// so the user can see the OAuth URL and complete the browser flow. We do
// NOT trust the exit code — the byte-diff check in runOnboarding is
// authoritative. We do, however, surface a non-zero exit so the caller
// can show a useful message in the cancelled case.
func runClaudeLogin(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, ClaudeCLI(), "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude login: %w", err)
	}
	return nil
}

// runOnboarding executes the documented Phase 2 flow:
//
//  1. Read & stash (in memory) the current `Claude Code-credentials` blob.
//     The blob may be empty if this is the user's first Claude Code login.
//  2. Invoke `claude login` and wait. The exit code is NOT trusted.
//  3. Read the slot again. Compare bytes against the stash.
//     - If bytes changed and are non-empty: OAuth succeeded.
//     - If bytes unchanged: the user cancelled (or `claude` is broken).
//  4. Restore the original stash to Claude Code's slot so the
//     previously-active account stays active.
//  5. Return the new credential to the caller, which writes it into the
//     aimonitor namespace under a chosen accountID.
//
// The returned Credential's Bytes are owned by the caller and should be
// zeroed when no longer needed.
func runOnboarding(ctx context.Context, deps onboardingDeps) (provider.Credential, error) {
	// 1. Stash current state. We tolerate the slot being empty (first-time
	//    Claude Code user adopting aimonitor will hit that case).
	stash, err := deps.keys.readActive(ctx)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("stash: %w", err)
	}

	// 2. Invoke claude login.
	loginErr := deps.login(ctx)
	// Don't return loginErr immediately — even on non-zero exit the blob
	// might have been written successfully (we don't trust exit codes).
	// We DO short-circuit the rest if `claude` isn't installed at all.
	if loginErr != nil {
		var pathErr *exec.Error
		if errors.As(loginErr, &pathErr) {
			return provider.Credential{}, fmt.Errorf("claude CLI not found in PATH — is Claude Code installed? Original error: %w", loginErr)
		}
	}

	// 3. Read the slot again.
	after, err := deps.keys.readActive(ctx)
	if err != nil {
		// Best-effort restore in case the failure was transient.
		if len(stash.Bytes) > 0 {
			_ = deps.keys.writeActive(ctx, stash)
		}
		return provider.Credential{}, fmt.Errorf("re-read after claude login: %w", err)
	}

	changed := !bytes.Equal(stash.Bytes, after.Bytes) && len(after.Bytes) > 0
	if !changed {
		// 4a. Cancelled / unchanged. Nothing to restore (slot is already
		//     as we found it). Return the underlying login error if any.
		if loginErr != nil {
			return provider.Credential{}, fmt.Errorf("OAuth not completed: %w", loginErr)
		}
		return provider.Credential{}, errors.New("OAuth not completed: credential slot unchanged after `claude login` returned 0")
	}

	// 4b. Success. The slot now holds the NEW account's blob. Restore the
	//     stash so the previously-active account stays active. If the
	//     stash was empty (first Claude Code login), there's nothing to
	//     restore — leave the new blob in place.
	if len(stash.Bytes) > 0 {
		if err := deps.keys.writeActive(ctx, stash); err != nil {
			// Restore failed. We have the new blob in our return value,
			// but Claude Code's slot is now wrong. Surface this loudly:
			// the caller can choose to either accept the new blob as
			// active OR retry the restore.
			return after, fmt.Errorf("captured new credential but failed to restore previous active: %w (manual fix: `aimonitor switch <previous>`)", err)
		}
	}

	return after, nil
}
