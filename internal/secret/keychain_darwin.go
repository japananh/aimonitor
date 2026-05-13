//go:build darwin

package secret

import (
	"errors"
	"fmt"

	"github.com/keybase/go-keychain"
)

// macOSKeychain implements Keyring against the macOS login keychain via
// Security.framework (through keybase/go-keychain). This file requires
// CGO and is only compiled on darwin.
//
// Items are stored as kSecClassGenericPassword with kSecAttrAccessible =
// AccessibleWhenUnlocked, meaning they're only readable while the user's
// login keychain is unlocked. SetSynchronizable(No) keeps them off iCloud
// Keychain.
type macOSKeychain struct{}

func defaultKeyring() (Keyring, error) {
	return &macOSKeychain{}, nil
}

func (m *macOSKeychain) Get(service, account string) ([]byte, error) {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(service)
	q.SetAccount(account)
	q.SetMatchLimit(keychain.MatchLimitOne)
	q.SetReturnData(true)

	results, err := keychain.QueryItem(q)
	if err != nil {
		if errors.Is(err, keychain.ErrorItemNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain query (%s/%s): %w", service, account, err)
	}
	if len(results) == 0 {
		return nil, ErrNotFound
	}
	// keybase/go-keychain returns a fresh slice already; safe to return as-is.
	return results[0].Data, nil
}

func (m *macOSKeychain) Set(service, account string, data []byte) error {
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount(account)
	item.SetData(data)
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlocked)

	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		// Update path: same query as Get, then UpdateItem with new data.
		q := keychain.NewItem()
		q.SetSecClass(keychain.SecClassGenericPassword)
		q.SetService(service)
		q.SetAccount(account)

		update := keychain.NewItem()
		update.SetData(data)
		if err := keychain.UpdateItem(q, update); err != nil {
			return fmt.Errorf("keychain update (%s/%s): %w", service, account, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("keychain add (%s/%s): %w", service, account, err)
	}
	return nil
}

func (m *macOSKeychain) Delete(service, account string) error {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(service)
	q.SetAccount(account)

	err := keychain.DeleteItem(q)
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("keychain delete (%s/%s): %w", service, account, err)
	}
	return nil
}
