package cli

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// bundleVersion is the export format's major version. Import refuses a bundle
// it doesn't understand rather than silently mis-restoring.
const bundleVersion = 1

// pbkdf2Iters is the PBKDF2-HMAC-SHA256 work factor for the token passphrase.
// 600k matches current OWASP guidance for PBKDF2-SHA256.
const pbkdf2Iters = 600_000

// exportBundle is the on-disk shape of `aimonitor config export`. Settings and
// account identities are plaintext (safe to share); credentials, when present
// (--include-tokens), are encrypted under a passphrase in Tokens.
type exportBundle struct {
	Version    int               `json:"version"`
	ExportedAt string            `json:"exported_at,omitempty"`
	Settings   map[string]string `json:"settings"`
	Accounts   []exportAccount   `json:"accounts"`
	Tokens     *encryptedTokens  `json:"tokens,omitempty"`
}

// exportAccount is an account's shareable identity — no secrets. Enough to
// recreate the row once a credential is supplied (via the encrypted Tokens
// blob, or a later `aimonitor add`).
type exportAccount struct {
	Label            string `json:"label"`
	Email            string `json:"email,omitempty"`
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
}

// encryptedTokens is the passphrase-encrypted credential blob: PBKDF2-SHA256
// to derive a 32-byte key, AES-256-GCM to seal. The plaintext is a JSON
// map[label]base64(stash-bytes). Salt and nonce are random per export.
type encryptedTokens struct {
	KDF        string `json:"kdf"` // "pbkdf2-sha256"
	Iter       int    `json:"iter"`
	Salt       string `json:"salt"`       // base64
	Nonce      string `json:"nonce"`      // base64
	Ciphertext string `json:"ciphertext"` // base64
}

// exportedSettingKeys is the behavioral preferences worth carrying between
// machines. Deliberately NOT update.skipped_version (machine-local) or MCP
// connection state (per-machine tokens).
func exportedSettingKeys() []string {
	return []string{
		daemon.SettingsKeyAutoSwapEnabled,
		daemon.SettingsKeyAutoSwapThreshold,
		daemon.SettingsKeyAutoSwapThreshold7d,
		daemon.SettingsKeyAutoSwapGrace,
		daemon.SettingsKeyNotifyEnabled,
		daemon.SettingsKeyNotifyWarnPct,
		daemon.SettingsKeyNotifyCritPct,
	}
}

func newConfigExportCmd() *cobra.Command {
	var includeTokens bool
	var outPath, passFile string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export settings (and optionally encrypted credentials) to a portable bundle",
		Long: `Write a JSON bundle of aimonitor's behavioral settings and account
identities, for replicating your setup on another machine.

By default NO credentials are included — the bundle is safe to share, but the
restored accounts need a credential before they can be used.

With --include-tokens, each account's OAuth credential is bundled too,
encrypted under a passphrase (PBKDF2-SHA256 + AES-256-GCM). Set the passphrase
via AIMONITOR_PASSPHRASE or --passphrase-file. THIS FILE THEN CONTAINS LIVE
CLAUDE CREDENTIALS — treat it like a password.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				return runConfigExport(ctx, cmd, s, includeTokens, outPath, passFile)
			})
		},
	}
	cmd.Flags().BoolVar(&includeTokens, "include-tokens", false, "also bundle credentials, encrypted under a passphrase")
	cmd.Flags().StringVar(&outPath, "out", "", "write to this file instead of stdout")
	cmd.Flags().StringVar(&passFile, "passphrase-file", "", "read the token passphrase from this file (else $AIMONITOR_PASSPHRASE)")
	return cmd
}

func newConfigImportCmd() *cobra.Command {
	var passFile string
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Restore settings (and credentials, if present) from an export bundle",
		Long: `Restore aimonitor settings from a bundle written by "config export".

Settings are always restored. Accounts are restored only when the bundle
carries encrypted credentials (exported with --include-tokens) — supply the
passphrase via AIMONITOR_PASSPHRASE or --passphrase-file. Token-less bundles
just list the account identities for you to re-add manually; no broken,
credential-less rows are created.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				return runConfigImport(ctx, cmd, s, args[0], passFile)
			})
		},
	}
	cmd.Flags().StringVar(&passFile, "passphrase-file", "", "read the token passphrase from this file (else $AIMONITOR_PASSPHRASE)")
	return cmd
}

func runConfigExport(ctx context.Context, cmd *cobra.Command, s *store.Store, includeTokens bool, outPath, passFile string) error {
	accts, err := s.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	bundle := exportBundle{
		Version:    bundleVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Settings:   map[string]string{},
	}
	for _, k := range exportedSettingKeys() {
		v, err := getStoreSetting(ctx, k)
		if err != nil {
			return fmt.Errorf("read setting %s: %w", k, err)
		}
		bundle.Settings[k] = v
	}
	for _, a := range accts {
		bundle.Accounts = append(bundle.Accounts, exportAccount{
			Label:            a.Label,
			Email:            a.Email,
			OrganizationUUID: a.OrganizationUUID,
			OrganizationName: a.OrganizationName,
		})
	}

	if includeTokens {
		pass, err := resolvePassphrase(passFile)
		if err != nil {
			return err
		}
		tokenMap := map[string]string{}
		for _, a := range accts {
			stash, err := claude.RetrieveStash(ctx, a.KeyringRef)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: skip token for %q: %v\n", a.Label, err)
				continue
			}
			tokenMap[a.Label] = base64.StdEncoding.EncodeToString(stash.Bytes)
			stash.Zero()
		}
		plain, err := json.Marshal(tokenMap)
		if err != nil {
			return fmt.Errorf("marshal tokens: %w", err)
		}
		enc, err := encryptTokens(plain, pass)
		if err != nil {
			return err
		}
		bundle.Tokens = enc
	}

	out, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bundle: %w", err)
	}
	if outPath == "" {
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
	// 0600: a token bundle holds credentials; even the plaintext one is the
	// user's config, not world-readable.
	if err := os.WriteFile(outPath, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Exported %d settings, %d accounts%s to %s\n",
		len(bundle.Settings), len(bundle.Accounts),
		map[bool]string{true: " (with encrypted tokens)", false: ""}[includeTokens], outPath)
	return nil
}

func runConfigImport(ctx context.Context, cmd *cobra.Command, s *store.Store, path, passFile string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var bundle exportBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("parse bundle: %w", err)
	}
	if bundle.Version != bundleVersion {
		return fmt.Errorf("unsupported bundle version %d (this build understands %d)", bundle.Version, bundleVersion)
	}

	// 1. Settings — always restored. Validate each through the same path as
	// `config set` so a hand-edited bundle can't write a garbage value.
	var setN, skipN int
	for k, v := range bundle.Settings {
		norm, err := validateStoreValue(k, v)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "skip setting %s: %v\n", k, err)
			skipN++
			continue
		}
		if err := s.PutSetting(ctx, k, norm); err != nil {
			return fmt.Errorf("write setting %s: %w", k, err)
		}
		setN++
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Restored %d settings (%d skipped).\n", setN, skipN)

	// 2. Accounts — only when credentials are present, so we never create a
	// credential-less, unusable row.
	if bundle.Tokens == nil {
		if len(bundle.Accounts) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(),
				"\nBundle has no credentials — these %d accounts must be re-added with `aimonitor add`:\n", len(bundle.Accounts))
			for _, a := range bundle.Accounts {
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s (%s)\n", a.Label, a.Email)
			}
		}
		return nil
	}

	pass, err := resolvePassphrase(passFile)
	if err != nil {
		return err
	}
	plain, err := decryptTokens(bundle.Tokens, pass)
	if err != nil {
		return err
	}
	var tokenMap map[string]string
	if err := json.Unmarshal(plain, &tokenMap); err != nil {
		return fmt.Errorf("parse decrypted tokens: %w", err)
	}

	var added, refreshed, failed int
	for _, a := range bundle.Accounts {
		b64tok, ok := tokenMap[a.Label]
		if !ok {
			fmt.Fprintf(cmd.ErrOrStderr(), "skip %q: no credential in bundle\n", a.Label)
			failed++
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(b64tok)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "skip %q: decode credential: %v\n", a.Label, err)
			failed++
			continue
		}
		cred := provider.Credential{Bytes: blob}

		existing, dErr := s.GetAccountByIdentity(ctx, a.Email, a.OrganizationUUID)
		switch {
		case dErr == nil:
			if err := claude.StashCredential(ctx, existing.KeyringRef, cred); err != nil {
				cred.Zero()
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %q: refresh stash: %v\n", a.Label, err)
				failed++
				continue
			}
			cred.Zero()
			_ = s.UpdateAccountIdentity(ctx, existing.ID, a.Email, a.OrganizationUUID, a.OrganizationName)
			refreshed++
		case errors.Is(dErr, store.ErrAccountNotFound):
			ref := uuid.NewString()
			if err := claude.StashCredential(ctx, ref, cred); err != nil {
				cred.Zero()
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %q: stash credential: %v\n", a.Label, err)
				failed++
				continue
			}
			cred.Zero()
			if _, err := s.CreateAccount(ctx, store.Account{
				Label:            a.Label,
				Email:            a.Email,
				OrganizationUUID: a.OrganizationUUID,
				OrganizationName: a.OrganizationName,
				KeyringRef:       ref,
			}); err != nil {
				_ = claude.DeleteStash(ctx, ref)
				fmt.Fprintf(cmd.ErrOrStderr(), "skip %q: create account: %v\n", a.Label, err)
				failed++
				continue
			}
			added++
		default:
			cred.Zero()
			return fmt.Errorf("look up %q: %w", a.Label, dErr)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Accounts: %d added, %d refreshed, %d failed.\n", added, refreshed, failed)
	return nil
}

// resolvePassphrase sources the token passphrase from --passphrase-file, else
// $AIMONITOR_PASSPHRASE. No interactive prompt (keeps the CLI dependency-free
// and scriptable); a clear error tells the user how to provide one.
func resolvePassphrase(passFile string) (string, error) {
	if passFile != "" {
		b, err := os.ReadFile(passFile)
		if err != nil {
			return "", fmt.Errorf("read passphrase file: %w", err)
		}
		p := strings.TrimRight(string(b), "\r\n")
		if p == "" {
			return "", errors.New("passphrase file is empty")
		}
		return p, nil
	}
	if p := os.Getenv("AIMONITOR_PASSPHRASE"); p != "" {
		return p, nil
	}
	return "", errors.New("a passphrase is required: set $AIMONITOR_PASSPHRASE or pass --passphrase-file <path>")
}

func encryptTokens(plain []byte, passphrase string) (*encryptedTokens, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, pbkdf2Iters, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	return &encryptedTokens{
		KDF:        "pbkdf2-sha256",
		Iter:       pbkdf2Iters,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

func decryptTokens(t *encryptedTokens, passphrase string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(t.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(t.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(t.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	iter := t.Iter
	if iter <= 0 {
		iter = pbkdf2Iters
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decrypt failed — wrong passphrase or corrupted bundle")
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return gcm, nil
}
