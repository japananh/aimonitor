package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// ClaudeCLI is the path of the Claude Code CLI binary. Override via env
// var `AIMONITOR_CLAUDE_BIN` for testing, otherwise we trust PATH lookup.
//
// Retained as a package-level hook so future onboarding modes (e.g.
// `claude /login` shelled-out) can find the binary the same way.
var ClaudeCLI = func() string {
	if v := os.Getenv("AIMONITOR_CLAUDE_BIN"); v != "" {
		return v
	}
	return "claude"
}

// DefaultCaptureTimeout caps how long CaptureNew waits for the user to
// drive a fresh login. Five minutes is enough for a slow OAuth dance
// over a tethered phone, short enough that an idle shell doesn't pin
// the user's keychain indefinitely.
const DefaultCaptureTimeout = 5 * time.Minute

// DefaultPollInterval is the cadence at which CaptureNew re-reads the
// Claude Code-credentials slot looking for a byte change.
const DefaultPollInterval = 2 * time.Second

// onboardingDeps abstracts the two pieces of the world the onboarding
// flow needs: keychain ops and a clock the tests can fast-forward.
// Retained from the v1 onboarding shape so existing tests still compile.
type onboardingDeps struct {
	keys  *keychainOps
	login func(ctx context.Context) error // legacy, only used by runOnboarding shim
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// AdoptCurrent reads whatever credential is currently in the
// Claude Code-credentials slot and returns a copy. The slot itself is
// left untouched — this is the fast path for users who already have a
// Claude Code login and just want to register it as an aimonitor
// account without driving a new OAuth flow.
//
// Returns an error if the slot is empty (nothing to adopt).
//
// The returned Credential's Bytes are owned by the caller and should
// be zeroed when no longer needed.
func AdoptCurrent(ctx context.Context) (provider.Credential, error) {
	k, err := newKeychainOps()
	if err != nil {
		return provider.Credential{}, err
	}
	current, err := k.readActive(ctx)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read Claude Code-credentials: %w", err)
	}
	if len(current.Bytes) == 0 {
		return provider.Credential{}, errors.New("no credential to adopt: the Claude Code-credentials slot is empty — log into Claude Code first (`claude` + `/login`), then re-run `aimonitor add --adopt-current`")
	}
	return current, nil
}

// CaptureNew implements the multi-account capture flow with no
// assumption about HOW the user drives the new login. The contract:
//
//  1. Stash the current Claude Code-credentials slot in memory.
//  2. Tell the user (via `out`) to log into a different account using
//     whatever mechanism their Claude Code version supports
//     (interactive `claude` + `/login`, `claude auth login`, etc.).
//  3. Poll the slot every PollInterval. When the bytes change AND the
//     new bytes are non-empty AND differ from the stash, treat that as
//     a successful new login.
//  4. Restore the original stash so the previously-active account stays
//     active in Claude Code.
//  5. Return the captured new credential to the caller.
//
// On timeout, return ErrCaptureTimeout. On ctx.Cancelled, restore the
// stash (best-effort) and return ctx.Err().
//
// This design has zero coupling to Claude Code's CLI shape. It works
// whether `claude login` is a subcommand, a slash command, or removed
// entirely in a future version.
func CaptureNew(ctx context.Context, out io.Writer, opts CaptureOpts) (provider.Credential, error) {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultCaptureTimeout
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.NewLabel == "" {
		opts.NewLabel = "the new account"
	}

	k, err := newKeychainOps()
	if err != nil {
		return provider.Credential{}, err
	}
	deps := onboardingDeps{
		keys:  k,
		now:   time.Now,
		sleep: contextSleep,
	}
	return captureNewWithDeps(ctx, out, deps, opts)
}

// CaptureOpts configures CaptureNew. Zero values are filled in with the
// package-level defaults.
type CaptureOpts struct {
	// NewLabel is the human-readable label for the account being added.
	// Used only in the instructional text we print to `out`.
	NewLabel string

	// Timeout caps how long we wait for a byte change. Default 5 min.
	Timeout time.Duration

	// PollInterval is the keychain-read cadence. Default 2 s.
	PollInterval time.Duration
}

// ErrCaptureTimeout is returned by CaptureNew when the timeout elapses
// before the user completes a new login.
var ErrCaptureTimeout = errors.New("timed out waiting for a new Claude login")

func captureNewWithDeps(ctx context.Context, out io.Writer, deps onboardingDeps, opts CaptureOpts) (provider.Credential, error) {
	// 1. Stash. Tolerate an empty slot — that means the user has no
	//    current login; we'll capture the first one they create.
	stash, err := deps.keys.readActive(ctx)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("stash current credential: %w", err)
	}

	// 2. Instructions. Print exactly the steps the user needs to take.
	//    We don't try to spawn `claude` ourselves; in Claude Code 2.x
	//    that ends up sending "login" as a prompt to the LLM, which is
	//    why the original auto-driven flow broke.
	fmt.Fprintf(out, "Adding %q. Your current Claude Code login is safe — we'll restore it when done.\n", opts.NewLabel)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "In ANOTHER terminal, log into the new account:")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  1. Run:    claude")
	fmt.Fprintln(out, "  2. Type:   /login")
	fmt.Fprintln(out, "  3. Complete the OAuth flow in your browser.")
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Polling Claude Code's keychain every %s. Press Ctrl+C to abort.\n", opts.PollInterval)
	fmt.Fprintln(out, "")

	// 3. Poll. On any iteration, three outcomes:
	//    - bytes differ from stash AND non-empty: success.
	//    - context cancelled: best-effort restore, return ctx.Err().
	//    - timeout reached: best-effort restore, return ErrCaptureTimeout.
	deadline := deps.now().Add(opts.Timeout)
	for {
		if err := ctx.Err(); err != nil {
			restoreStash(ctx, deps, stash)
			return provider.Credential{}, err
		}
		if deps.now().After(deadline) {
			restoreStash(ctx, deps, stash)
			return provider.Credential{}, ErrCaptureTimeout
		}

		after, err := deps.keys.readActive(ctx)
		if err != nil {
			// Transient keychain read error — sleep and retry rather
			// than bail. The user might have the keychain locked
			// momentarily during the OAuth dance.
			if sleepErr := deps.sleep(ctx, opts.PollInterval); sleepErr != nil {
				restoreStash(ctx, deps, stash)
				return provider.Credential{}, sleepErr
			}
			continue
		}

		changed := !bytes.Equal(stash.Bytes, after.Bytes) && len(after.Bytes) > 0
		if changed {
			fmt.Fprintln(out, "✓ Detected new credential.")
			// 4. Restore the stash. If the original slot was empty
			//    (first-time user), there's nothing to restore — leave
			//    the new blob in place as the active credential.
			if len(stash.Bytes) > 0 {
				if err := deps.keys.writeActive(ctx, stash); err != nil {
					// Couldn't restore. The new account is still
					// captured (we have the bytes), but Claude Code's
					// slot now holds the new blob. Surface this loudly.
					return after, fmt.Errorf("captured new credential but failed to restore previous active: %w (manual fix: `aimonitor switch <previous-label>` once you've registered it)", err)
				}
			}
			return after, nil
		}

		if err := deps.sleep(ctx, opts.PollInterval); err != nil {
			restoreStash(ctx, deps, stash)
			return provider.Credential{}, err
		}
	}
}

func restoreStash(ctx context.Context, deps onboardingDeps, stash provider.Credential) {
	if len(stash.Bytes) == 0 {
		return
	}
	_ = deps.keys.writeActive(ctx, stash)
}

// contextSleep blocks for d or until ctx is cancelled, whichever comes
// first. Returns ctx.Err() on cancel, nil on completion. Used as the
// default sleep in CaptureNew so Ctrl-C is immediately responsive.
func contextSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// runOnboarding is the legacy onboarding entry point kept so existing
// tests (TestRunOnboarding_*) still compile against the same signature.
// In production it's no longer called — internal/cli/add.go selects
// between AdoptCurrent and CaptureNew based on the --adopt-current flag.
//
// The contract is unchanged: stash → call deps.login → byte-diff →
// restore. Useful only for the test harness that injects a fake login.
func runOnboarding(ctx context.Context, deps onboardingDeps) (provider.Credential, error) {
	stash, err := deps.keys.readActive(ctx)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("stash: %w", err)
	}

	loginErr := deps.login(ctx)

	after, err := deps.keys.readActive(ctx)
	if err != nil {
		if len(stash.Bytes) > 0 {
			_ = deps.keys.writeActive(ctx, stash)
		}
		return provider.Credential{}, fmt.Errorf("re-read after login: %w", err)
	}

	changed := !bytes.Equal(stash.Bytes, after.Bytes) && len(after.Bytes) > 0
	if !changed {
		if loginErr != nil {
			return provider.Credential{}, fmt.Errorf("OAuth not completed: %w", loginErr)
		}
		return provider.Credential{}, errors.New("OAuth not completed: credential slot unchanged after login returned 0")
	}

	if len(stash.Bytes) > 0 {
		if err := deps.keys.writeActive(ctx, stash); err != nil {
			return after, fmt.Errorf("captured new credential but failed to restore previous active: %w", err)
		}
	}

	return after, nil
}
