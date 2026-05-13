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
type Account struct {
	ID         int64
	Provider   string
	Label      string
	Email      string
	KeyringRef string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero value if never used
}

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
		`INSERT INTO accounts (provider, label, email, keyring_ref, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.Provider, a.Label, a.Email, a.KeyringRef, a.CreatedAt.UnixMilli(),
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
		`SELECT id, provider, label, COALESCE(email,''), keyring_ref, created_at, COALESCE(last_used_at, 0)
		 FROM accounts WHERE label = ?`, label))
}

// GetAccountByID returns the account with the given ID, or
// ErrAccountNotFound.
func (s *Store) GetAccountByID(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT id, provider, label, COALESCE(email,''), keyring_ref, created_at, COALESCE(last_used_at, 0)
		 FROM accounts WHERE id = ?`, id))
}

// ListAccounts returns every account ordered by created_at ascending.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, provider, label, COALESCE(email,''), keyring_ref, created_at, COALESCE(last_used_at, 0)
		 FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Account
	for rows.Next() {
		var a Account
		var createdAt, lastUsedAt int64
		if err := rows.Scan(&a.ID, &a.Provider, &a.Label, &a.Email, &a.KeyringRef, &createdAt, &lastUsedAt); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(createdAt)
		if lastUsedAt != 0 {
			a.LastUsedAt = time.UnixMilli(lastUsedAt)
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

func scanAccount(row *sql.Row) (Account, error) {
	var a Account
	var createdAt, lastUsedAt int64
	err := row.Scan(&a.ID, &a.Provider, &a.Label, &a.Email, &a.KeyringRef, &createdAt, &lastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.CreatedAt = time.UnixMilli(createdAt)
	if lastUsedAt != 0 {
		a.LastUsedAt = time.UnixMilli(lastUsedAt)
	}
	return a, nil
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
