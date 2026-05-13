//go:build linux

package secret

import (
	"errors"
	"fmt"

	gokeyring "github.com/zalando/go-keyring"
)

// libsecretKeyring implements Keyring against the freedesktop.org Secret
// Service DBus API (typically backed by GNOME Keyring or KWallet). Uses
// zalando/go-keyring, which is pure-Go (godbus + DBus), so no CGO.
//
// On systems without a running Secret Service provider, every operation
// fails with a dbus connection error from the underlying library.
type libsecretKeyring struct{}

func defaultKeyring() (Keyring, error) {
	return &libsecretKeyring{}, nil
}

func (l *libsecretKeyring) Get(service, account string) ([]byte, error) {
	v, err := gokeyring.Get(service, account)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("libsecret get (%s/%s): %w", service, account, err)
	}
	return []byte(v), nil
}

func (l *libsecretKeyring) Set(service, account string, data []byte) error {
	if err := gokeyring.Set(service, account, string(data)); err != nil {
		return fmt.Errorf("libsecret set (%s/%s): %w", service, account, err)
	}
	return nil
}

func (l *libsecretKeyring) Delete(service, account string) error {
	err := gokeyring.Delete(service, account)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("libsecret delete (%s/%s): %w", service, account, err)
	}
	return nil
}
