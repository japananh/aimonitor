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
	var label string
	var email string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a Claude account (runs `claude login`, then stashes the resulting credential)",
		Long: `Onboarding flow:
  1. Stash the current Claude Code-credentials blob in memory.
  2. Invoke 'claude login' and wait for OAuth completion.
  3. Read the newly-written blob.
  4. Move it into an aimonitor-namespaced Keychain entry under <label>.
  5. Restore the original blob so the previously-active account stays active.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runAdd(ctx, cmd, s, p, label, email)
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "label to assign to the new account (prompted if omitted)")
	cmd.Flags().StringVar(&email, "email", "", "Anthropic email for this account (informational)")
	return cmd
}

func runAdd(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, label, email string) error {
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

	// Reject collisions early before we burn a `claude login` flow.
	if _, err := s.GetAccountByLabel(ctx, label); err == nil {
		return fmt.Errorf("an account with label %q already exists; pick another or `aimonitor remove %s` first", label, label)
	} else if !errors.Is(err, store.ErrAccountNotFound) {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Launching `claude login` — complete the OAuth flow in your browser…\n")
	cred, err := p.OnboardingFlow(ctx)
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
