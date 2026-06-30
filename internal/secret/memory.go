package secret

import "sync"

// MemoryKeyring is an in-memory Keyring for tests across packages — it lets a
// test inject a fake OS keyring (e.g. via claude.SetKeyringForTest) so it never
// touches the real macOS Keychain / libsecret. It is NEVER returned by
// Default() and no production code constructs it, so the shipped binary always
// uses the platform keyring.
type MemoryKeyring struct {
	mu    sync.Mutex
	store map[string][]byte
}

// NewMemoryKeyring returns an empty in-memory keyring.
func NewMemoryKeyring() *MemoryKeyring {
	return &MemoryKeyring{store: map[string][]byte{}}
}

func (m *MemoryKeyring) key(service, account string) string {
	return service + "\x00" + account
}

// Get returns a copy of the bytes under (service, account), or ErrNotFound.
func (m *MemoryKeyring) Get(service, account string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.store[m.key(service, account)]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// Set stores a copy of data under (service, account), overwriting any existing.
func (m *MemoryKeyring) Set(service, account string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := make([]byte, len(data))
	copy(b, data)
	m.store[m.key(service, account)] = b
	return nil
}

// Delete removes (service, account); returns ErrNotFound if it was absent.
func (m *MemoryKeyring) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(service, account)
	if _, ok := m.store[k]; !ok {
		return ErrNotFound
	}
	delete(m.store, k)
	return nil
}
