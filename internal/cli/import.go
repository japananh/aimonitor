package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// claudeBarBackupServiceFmt is claude-bar's per-account Keychain service
// name (backend/internal/adapter/keychain/backup_credential_store.go:
// "csw-backup:%d:%s" = number, email). The account field is the OS user.
const claudeBarBackupServiceFmt = "csw-backup:%d:%s"

// cbRegistry mirrors the subset of claude-bar's registry.json we read:
// the per-account identity. Credentials live in the keychain, not here.
type cbRegistry struct {
	Accounts map[string]cbAccount `json:"accounts"`
}

type cbAccount struct {
	Number           int    `json:"number"`
	Email            string `json:"email"`
	OrganizationName string `json:"organizationName"`
	OrganizationUUID string `json:"organizationUuid"`
	Nickname         string `json:"nickname"`
}

func newImportCmd() *cobra.Command {
	var keepAutoSwap bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import accounts that claude-bar (ClaudeSwapWidget) already detected",
		Long: `Import every account from claude-bar's registry into aimonitor, reading
each account's OAuth credential from its claude-bar Keychain backup
("csw-backup:<n>:<email>") and re-stashing it under aimonitor's namespace.

Accounts are matched by identity (email + organization): one already
registered in aimonitor has its credential and label refreshed rather than
duplicated, so importing is safe to re-run.

By default this disables auto-swap (so aimonitor won't fight claude-bar
over the active account); pass --keep-auto-swap to leave it on. The live
account is never changed — import only registers accounts for monitoring.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				return runImport(ctx, cmd, s, keepAutoSwap)
			})
		},
	}
	cmd.Flags().BoolVar(&keepAutoSwap, "keep-auto-swap", false,
		"keep auto-swap enabled (default disables it so aimonitor won't compete with claude-bar)")
	return cmd
}

func runImport(ctx context.Context, cmd *cobra.Command, s *store.Store, keepAutoSwap bool) error {
	regPath, err := claudeBarRegistryPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(regPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("claude-bar registry not found at %s — is ClaudeSwapWidget installed and set up?", regPath)
		}
		return fmt.Errorf("read claude-bar registry: %w", err)
	}
	var reg cbRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return fmt.Errorf("parse claude-bar registry: %w", err)
	}
	if len(reg.Accounts) == 0 {
		return errors.New("claude-bar registry has no accounts to import")
	}

	ring, err := secret.Default()
	if err != nil {
		return fmt.Errorf("open keyring: %w", err)
	}
	usr, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve OS user: %w", err)
	}

	// Deterministic order by claude-bar account number.
	accounts := make([]cbAccount, 0, len(reg.Accounts))
	for _, a := range reg.Accounts {
		accounts = append(accounts, a)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Number < accounts[j].Number })

	var added, refreshed, failed int
	for _, a := range accounts {
		if a.Email == "" {
			continue
		}
		label := a.Nickname
		if label == "" {
			label = emailLocalPart(a.Email)
		}
		service := fmt.Sprintf(claudeBarBackupServiceFmt, a.Number, a.Email)
		blob, err := ring.Get(service, usr.Username)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: read keychain %q: %v\n", a.Email, service, err)
			failed++
			continue
		}
		cred := provider.Credential{Bytes: blob}

		existing, dErr := s.GetAccountByIdentity(ctx, a.Email, a.OrganizationUUID)
		switch {
		case dErr == nil:
			// Already registered: refresh credential + identity, relabel to
			// the claude-bar nickname.
			if err := claude.StashCredential(ctx, existing.KeyringRef, cred); err != nil {
				cred.Zero()
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: refresh stash: %v\n", a.Email, err)
				failed++
				continue
			}
			cred.Zero()
			_ = s.UpdateAccountIdentity(ctx, existing.ID, a.Email, a.OrganizationUUID, a.OrganizationName)
			if existing.Label != label {
				if rErr := s.RenameAccount(ctx, existing.Label, label); rErr != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s (kept label %q; rename to %q failed: %v)\n",
						a.Email, existing.Label, label, rErr)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s (relabeled %q → %q)\n", a.Email, existing.Label, label)
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s (%q)\n", a.Email, label)
			}
			refreshed++

		case errors.Is(dErr, store.ErrAccountNotFound):
			ref := uuid.NewString()
			if err := claude.StashCredential(ctx, ref, cred); err != nil {
				cred.Zero()
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: stash credential: %v\n", a.Email, err)
				failed++
				continue
			}
			cred.Zero()
			if _, err := s.CreateAccount(ctx, store.Account{
				Label:            label,
				Email:            a.Email,
				OrganizationUUID: a.OrganizationUUID,
				OrganizationName: a.OrganizationName,
				KeyringRef:       ref,
			}); err != nil {
				_ = claude.DeleteStash(ctx, ref)
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: create account: %v\n", a.Email, err)
				failed++
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported %s as %q\n", a.Email, label)
			added++

		default:
			cred.Zero()
			return fmt.Errorf("look up %s: %w", a.Email, dErr)
		}
	}

	if !keepAutoSwap {
		if err := s.PutSetting(ctx, daemon.SettingsKeyAutoSwapEnabled, "false"); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: disable auto-swap: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(),
				"\nAuto-swap disabled so aimonitor won't compete with claude-bar over the active account.\nRe-enable any time: `aimonitor config set auto_swap.enabled true`\n")
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nImport complete: %d added, %d refreshed, %d failed.\n", added, refreshed, failed)
	return nil
}

// claudeBarRegistryPath is ~/Library/Application Support/claude-swap-widget/registry.json.
func claudeBarRegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "claude-swap-widget", "registry.json"), nil
}

// emailLocalPart returns the part before '@', used as a fallback label when
// a claude-bar account has no nickname.
func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}
