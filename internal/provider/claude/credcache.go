package claude

import (
	"sync"
	"time"
)

// credCache is a small in-memory TTL cache keyed by (service, account)
// pairs. Used to amortise the fork+exec cost of /usr/bin/security across
// the daemon's hot status-poll loop. Not LRU and not size-bounded: the
// total number of distinct keys is the active credential plus one per
// configured account (typically ≤ 5), so unbounded growth is not a
// concern in practice.
type credCache struct {
	mu    sync.Mutex
	items map[string]credCacheEntry
	ttl   time.Duration
	now   func() time.Time
}

type credCacheEntry struct {
	data    []byte
	expires time.Time
}

func newCredCache(ttl time.Duration) *credCache {
	return &credCache{
		items: map[string]credCacheEntry{},
		ttl:   ttl,
		now:   time.Now,
	}
}

// cacheKey constructs a stable composite key from (service, account). A
// NUL byte separates the two fields so that distinct pairs cannot collide
// even if one field's value contains the literal "/" we'd otherwise use.
func cacheKey(service, account string) string {
	return service + "\x00" + account
}

// get returns the cached bytes (cloned) and true if the key is present
// and unexpired. Expired entries are pruned on access.
//
// The returned slice is interior to the cache; callers must clone before
// mutating or zeroing. keychainOps wraps every cache return with
// cloneBytes() to enforce this at the call sites.
func (c *credCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if c.now().After(e.expires) {
		delete(c.items, key)
		return nil, false
	}
	return e.data, true
}

// put stores a fresh copy of data under key with TTL credCacheTTL from now.
// Always clones — never holds the caller's slice — so cache state is
// independent of the caller's mutation lifecycle.
func (c *credCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	c.items[key] = credCacheEntry{
		data:    cp,
		expires: c.now().Add(c.ttl),
	}
}

// invalidate removes a single key. Cheap no-op when the key is absent.
// Used after Delete operations to keep the cache consistent.
func (c *credCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
