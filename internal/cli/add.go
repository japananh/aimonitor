package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	var label, email string
	var adoptCurrent bool
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a Claude account (multi-account capture or adopt-current)",
		Long: `Two modes:

  --adopt-current (fast path)
    Stash whatever is currently in Claude Code-credentials under a chosen
    label, without driving a new login. Use this once on first install to
    register your existing Claude Code account.

  default (multi-account capture)
    1. Stash the current Claude Code-credentials blob in memory.
    2. Print instructions: open another terminal, run 'claude', type
       '/login', and complete the OAuth flow for the new account.
    3. Poll the keychain every 2s for a byte change. When detected,
       capture the new credential bytes.
    4. Move the captured bytes into an aimonitor-namespaced keychain
       entry under <label>.
    5. Restore the original blob so the previously-active Claude Code
       account stays active.

The previously-active credential is NEVER lost: we restore it before
returning. Worst case (transient keychain failure during restore), we
print a manual-recovery hint pointing at 'aimonitor switch'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runAdd(ctx, cmd, s, p, label, email, adoptCurrent)
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "label to assign to the new account (prompted if omitted)")
	cmd.Flags().StringVar(&email, "email", "", "Anthropic email for this account (informational)")
	cmd.Flags().BoolVar(&adoptCurrent, "adopt-current", false, "stash the existing Claude Code-credentials slot without driving a new login")
	return cmd
}

func runAdd(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, label, email string, adoptCurrent bool) error {
	if label == "" {
		var err error
		label, err = promptLine(cmd, "Label for the new account (e.g. personal, work): ")
		if err != nil {
			return fmt.Errorf("read label: %w", err)
		}
		label = strings.TrimSpace(label)
		if label == "" {
			return errors.New("label is required")
		}
	}

	// Reject collisions early before we capture anything.
	if _, err := s.GetAccountByLabel(ctx, label); err == nil {
		return fmt.Errorf("an account with label %q already exists; pick another or `aimonitor remove %s` first", label, label)
	} else if !errors.Is(err, store.ErrAccountNotFound) {
		return err
	}

	var cred provider.Credential
	var err error
	if adoptCurrent {
		fmt.Fprintf(cmd.OutOrStdout(), "Adopting current Claude Code-credentials as %q…\n", label)
		cred, err = claude.AdoptCurrent(ctx)
	} else {
		// p.OnboardingFlow is the legacy entry point and is no-op now
		// (Claude Code 2.x dropped `claude login` as a subcommand).
		// Use the new poll-the-slot capture flow instead.
		cred, err = claude.CaptureNew(ctx, cmd.OutOrStdout(), claude.CaptureOpts{NewLabel: label})
	}
	if err != nil {
		return err
	}
	defer cred.Zero()

	ref := uuid.NewString()
	if err := claude.StashCredential(ctx, ref, cred); err != nil {
		return fmt.Errorf("stash credential: %w", err)
	}

	acct, err := s.CreateAccount(ctx, store.Account{
		Label:      label,
		Email:      email,
		KeyringRef: ref,
	})
	if err != nil {
		// Roll back the stash so we don't leak orphan keyring entries.
		_ = claude.DeleteStash(ctx, ref)
		return fmt.Errorf("create account row: %w", err)
	}

	// Silence the unused-import lint when --adopt-current is the only path
	// hit in tests (provider is fetched via withRuntime but not used here).
	_ = p

	fmt.Fprintf(cmd.OutOrStdout(), "Account %q added (id=%d). Use `aimonitor switch %s` to make it active.\n", acct.Label, acct.ID, acct.Label)
	return nil
}

func promptLine(cmd *cobra.Command, prompt string) (string, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
