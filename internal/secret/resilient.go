package secret

import (
	"errors"
	"sync"
	"time"
)

// retryDelays spaces the retries after a failed read: 5s, 10s, 15s (about
// 30s in total), then every 30s until a read succeeds. Between attempts reads
// fail fast, so a hung keychain costs one 5s timeout per attempt instead of
// one per caller per poll.
var retryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second}

// EventKind says what a Resilient keyring is reporting.
type EventKind int

const (
	// EventDegraded fires once, on the first failed read of an item.
	EventDegraded EventKind = iota
	// EventRecovered fires when an item reads again after being degraded.
	EventRecovered
)

// Event is passed to the Resilient hook. It never carries secret bytes.
type Event struct {
	Kind             EventKind
	Service, Account string
	Err              error         // the read error (EventDegraded)
	Down             time.Duration // time since the first failure (EventRecovered)
	Failures         int           // failed attempts so far (EventRecovered)
}

// Resilient wraps a Keyring so a transient read failure (ErrUnavailable) is
// retried on a short backoff schedule instead of on every poll, and is
// reported once through hook rather than once per call. Not-found and other
// errors pass straight through; Set and Delete are not touched.
type Resilient struct {
	inner Keyring
	hook  func(Event)
	now   func() time.Time

	mu    sync.Mutex
	items map[string]*itemState
}

type itemState struct {
	since    time.Time
	failures int
	nextTry  time.Time
	lastErr  error
}

// NewResilient wraps inner. hook may be nil.
func NewResilient(inner Keyring, hook func(Event)) *Resilient {
	return &Resilient{inner: inner, hook: hook, now: time.Now, items: map[string]*itemState{}}
}

// Get implements Keyring.
func (r *Resilient) Get(service, account string) ([]byte, error) {
	key := service + "\x00" + account
	r.mu.Lock()
	if st := r.items[key]; st != nil && r.now().Before(st.nextTry) {
		err := st.lastErr
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()

	data, err := r.inner.Get(service, account)

	r.mu.Lock()
	st := r.items[key]
	switch {
	case err != nil && errors.Is(err, ErrUnavailable):
		now := r.now()
		first := st == nil
		if first {
			st = &itemState{since: now}
			r.items[key] = st
		}
		st.failures++
		st.nextTry = now.Add(retryDelays[min(st.failures-1, len(retryDelays)-1)])
		st.lastErr = err
		r.mu.Unlock()
		if first {
			r.emit(Event{Kind: EventDegraded, Service: service, Account: account, Err: err})
		}
		return nil, err
	default:
		// Success and not-found both prove the keychain answered.
		delete(r.items, key)
		r.mu.Unlock()
		if st != nil {
			r.emit(Event{Kind: EventRecovered, Service: service, Account: account,
				Down: r.now().Sub(st.since), Failures: st.failures})
		}
		return data, err
	}
}

// Set implements Keyring.
func (r *Resilient) Set(service, account string, data []byte) error {
	return r.inner.Set(service, account, data)
}

// Delete implements Keyring.
func (r *Resilient) Delete(service, account string) error {
	return r.inner.Delete(service, account)
}

func (r *Resilient) emit(e Event) {
	if r.hook != nil {
		r.hook(e)
	}
}

// faultKeyring fails every Get with a transient error until failFor has
// elapsed since creation, then delegates. Debug aid for exercising the
// degraded path without touching the real keychain.
type faultKeyring struct {
	Keyring
	until time.Time
}

// NewFaultKeyring returns inner, except that Get fails with a transient
// *ReadError for the first failFor after creation.
func NewFaultKeyring(inner Keyring, failFor time.Duration) Keyring {
	return &faultKeyring{Keyring: inner, until: time.Now().Add(failFor)}
}

func (f *faultKeyring) Get(service, account string) ([]byte, error) {
	if time.Now().Before(f.until) {
		return nil, &ReadError{Service: service, Account: account, Reason: "injected fault"}
	}
	return f.Keyring.Get(service, account)
}
