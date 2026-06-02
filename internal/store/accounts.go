package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Account is a row in the accounts table. KeyringRef is the service name
// the keyring entry lives under — typically `aimonitor-<uuid>` produced
// by the provider's stash routines.
//
// Email + OrganizationUUID together form the account's Claude identity
// (the same address in two orgs is two distinct accounts). They are
// captured from ~/.claude.json's oauthAccount at add/switch time and
// used to patch that file on a switch so Claude Code's active-account
// metadata tracks the swapped keychain tokens.
type Account struct {
	ID               int64
	Provider         string
	Label            string
	Email            string
	OrganizationUUID string
	OrganizationName string
	KeyringRef       string
	CreatedAt        time.Time
	LastUsedAt       time.Time // zero value if never used
}

// accountColumns is the canonical SELECT list, shared by every read so
// the column order can't drift from the scan order. COALESCE guards the
// nullable/added columns so pre-0003 rows scan cleanly.
const accountColumns = `id, provider, label, COALESCE(email,''), COALESCE(organization_uuid,''), COALESCE(organization_name,''), keyring_ref, created_at, COALESCE(last_used_at, 0)`

// ErrAccountNotFound is returned by GetAccountByLabel / GetAccountByID
// when no matching row exists.
var ErrAccountNotFound = errors.New("store: account not found")

// CreateAccount inserts a row and returns the populated Account with its
// generated ID. Returns an error wrapping the SQLite UNIQUE-constraint
// failure when Label collides with an existing row.
func (s *Store) CreateAccount(ctx context.Context, a Account) (Account, error) {
	if a.Label == "" {
		return Account{}, errors.New("CreateAccount: empty Label")
	}
	if a.KeyringRef == "" {
		return Account{}, errors.New("CreateAccount: empty KeyringRef")
	}
	if a.Provider == "" {
		a.Provider = "claude"
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}

	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO accounts (provider, label, email, organization_uuid, organization_name, keyring_ref, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.Provider, a.Label, a.Email, a.OrganizationUUID, a.OrganizationName, a.KeyringRef, a.CreatedAt.UnixMilli(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Account{}, fmt.Errorf("account label %q already exists", a.Label)
		}
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Account{}, fmt.Errorf("LastInsertId: %w", err)
	}
	a.ID = id
	return a, nil
}

// GetAccountByLabel returns the account with the given Label, or
// ErrAccountNotFound.
func (s *Store) GetAccountByLabel(ctx context.Context, label string) (Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE label = ?`, label))
}

// GetAccountByID returns the account with the given ID, or
// ErrAccountNotFound.
func (s *Store) GetAccountByID(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id))
}

// GetAccountByIdentity returns the account matching (email, organization_uuid),
// or ErrAccountNotFound. Used to detect when an `aimonitor add` is really
// re-adding an already-registered Claude account (same address + org).
// An empty email never matches — identity is unknown for legacy rows that
// predate 0003, so they don't spuriously collide.
func (s *Store) GetAccountByIdentity(ctx context.Context, email, orgUUID string) (Account, error) {
	if email == "" {
		return Account{}, ErrAccountNotFound
	}
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE email = ? AND COALESCE(organization_uuid,'') = ?`,
		email, orgUUID))
}

// ListAccounts returns every account ordered by created_at ascending.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Account
	for rows.Next() {
		a, err := scanAccountRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccountLastUsed sets last_used_at on the given account to ts.
func (s *Store) UpdateAccountLastUsed(ctx context.Context, id int64, ts time.Time) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE accounts SET last_used_at = ? WHERE id = ?`,
		ts.UnixMilli(), id,
	)
	if err != nil {
		return fmt.Errorf("update last_used_at: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// RenameAccount changes an account's label. Returns ErrAccountNotFound if
// no row matches oldLabel; returns an error wrapping the UNIQUE failure
// if newLabel collides.
func (s *Store) RenameAccount(ctx context.Context, oldLabel, newLabel string) error {
	if newLabel == "" {
		return errors.New("RenameAccount: empty newLabel")
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE accounts SET label = ? WHERE label = ?`,
		newLabel, oldLabel,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("account label %q already exists", newLabel)
		}
		return fmt.Errorf("rename: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// DeleteAccount removes the account row. Cascades to usage_samples and
// probe_results via the schema's ON DELETE CASCADE; the caller is
// responsible for removing the keyring entry separately.
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// rowScanner is the Scan surface shared by *sql.Row and *sql.Rows, so
// single-row and list reads decode through one place and can't drift in
// column order.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAccountRow decodes one row in accountColumns order.
func scanAccountRow(r rowScanner) (Account, error) {
	var a Account
	var createdAt, lastUsedAt int64
	err := r.Scan(&a.ID, &a.Provider, &a.Label, &a.Email, &a.OrganizationUUID, &a.OrganizationName, &a.KeyringRef, &createdAt, &lastUsedAt)
	if err != nil {
		return Account{}, err
	}
	a.CreatedAt = time.UnixMilli(createdAt)
	if lastUsedAt != 0 {
		a.LastUsedAt = time.UnixMilli(lastUsedAt)
	}
	return a, nil
}

// scanAccount decodes a single-row query, mapping the no-rows case to
// ErrAccountNotFound.
func scanAccount(row *sql.Row) (Account, error) {
	a, err := scanAccountRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// UpdateAccountIdentity refreshes the email/org identity captured for an
// account. Called when a re-add (or a switch that learns fresher
// identity from ~/.claude.json) needs to backfill or correct the row.
func (s *Store) UpdateAccountIdentity(ctx context.Context, id int64, email, orgUUID, orgName string) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE accounts SET email = ?, organization_uuid = ?, organization_name = ? WHERE id = ?`,
		email, orgUUID, orgName, id,
	)
	if err != nil {
		return fmt.Errorf("update account identity: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// isUniqueViolation peeks at the error message to detect SQLite's UNIQUE
// constraint failure. modernc.org/sqlite doesn't ship typed errors for
// this, so a string match is the pragmatic option.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
