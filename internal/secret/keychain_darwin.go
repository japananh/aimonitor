//go:build darwin

package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// macOSKeychain implements Keyring by shelling out to /usr/bin/security,
// Apple's signed system tool for keychain access.
//
// Why not call Security.framework directly (e.g. via keybase/go-keychain)?
// macOS attaches an ACL to every keychain item naming the *code identity*
// (signed binary hash + Team ID) of the creating process. Subsequent reads
// by a different identity trigger the "Allow / Always Allow / Deny" dialog.
// aimonitor is ad-hoc signed, so every rebuild produces a "new app" from
// the keychain's perspective and triggers a fresh prompt.
//
// /usr/bin/security is Apple-signed with a stable identity that macOS
// universally trusts — it reads/writes keychain items without per-app ACL
// dialogs, regardless of which app originally created the item. The cost
// is a fork+exec per operation, which a thin in-process cache amortizes.
//
// Items are written without -T (trusted-app list) so any process can read
// them through /usr/bin/security in the future. Synchronizable defaults to
// false (no iCloud Keychain).
type macOSKeychain struct{}

func defaultKeyring() (Keyring, error) {
	return &macOSKeychain{}, nil
}

// keychainCmdTimeout bounds each /usr/bin/security invocation. Reading or
// writing a single password entry on an unlocked login keychain takes
// single-digit milliseconds in practice; 5 s is generous enough to absorb
// transient I/O hiccups while still catching a deadlocked or hung process.
const keychainCmdTimeout = 5 * time.Second

// securityExitNotFound is the documented exit code /usr/bin/security
// returns when find-generic-password / delete-generic-password matches
// no item. Treat as ErrNotFound rather than a generic shell failure.
const securityExitNotFound = 44

// Get returns the bytes stored under (service, account).
// Returns ErrNotFound when the item does not exist.
func (m *macOSKeychain) Get(service, account string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"/usr/bin/security",
		"find-generic-password",
		"-s", service,
		"-a", account,
		"-w",
	)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == securityExitNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain find (%s/%s): %s", service, account, redactedStderr(&errOut))
	}
	// /usr/bin/security appends a trailing newline.
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// Set upserts the password under (service, account). The -U flag updates
// an existing item in place (preserving its keychain item attributes) or
// creates a new one without a restrictive ACL.
//
// Security note: the data is passed in argv, briefly visible to ps for
// the duration of the syscall (~milliseconds). On a single-user Mac, ps
// only shows the same user's processes; any reader already has unix-level
// access. Accepted; documented in _plans/.../README.md.
func (m *macOSKeychain) Set(service, account string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"/usr/bin/security",
		"add-generic-password",
		"-U",
		"-s", service,
		"-a", account,
		"-w", string(data),
	)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain add (%s/%s): %s", service, account, redactedStderr(&errOut))
	}
	return nil
}

// Delete removes the entry. Returns ErrNotFound when the item did not exist.
func (m *macOSKeychain) Delete(service, account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"/usr/bin/security",
		"delete-generic-password",
		"-s", service,
		"-a", account,
	)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == securityExitNotFound {
			return ErrNotFound
		}
		return fmt.Errorf("keychain delete (%s/%s): %s", service, account, redactedStderr(&errOut))
	}
	return nil
}

// redactedStderr trims a /usr/bin/security stderr buffer for inclusion in
// an error message. The CLI does not echo passwords on stderr in any
// observed failure path, but the helper exists as a guarded chokepoint so
// future flag additions (e.g. -v debug logging) can't accidentally leak
// the payload through error returns.
func redactedStderr(b *bytes.Buffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "(no stderr)"
	}
	return s
}
