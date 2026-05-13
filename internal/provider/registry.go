package provider

import (
	"fmt"
	"sort"
	"sync"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Provider{}
)

// Register associates a Provider with a name. It is intended to be called
// from package init() functions of concrete provider implementations.
//
// It panics on duplicate registration: provider names are stable identifiers
// and silent collisions would be a configuration-corrupting bug.
func Register(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := p.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("provider %q registered twice", name))
	}
	registry[name] = p
}

// Lookup returns the Provider previously registered under name.
func Lookup(name string) (Provider, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", name)
	}
	return p, nil
}

// Names returns every registered provider name, sorted lexically. Used by
// `aimonitor doctor` and `aimonitor list`.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
