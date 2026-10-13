package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// liveMissingGrace is how long the live credential must stay absent before
// the daemon stops attributing to the last known account. Claude Code
// rewrites the item on login and token refresh, so a short absence is normal.
const liveMissingGrace = 10 * time.Minute

// ActiveResolver is ResolveActiveAccount with memory. A transient keychain
// read failure, or a live item that is briefly absent mid-rewrite, returns the
// last resolved account instead of "no active account", so sampling and the
// status snapshot ride through the blip. State changes only when a readable
// credential matches no managed account (immediately), or when the item stays
// absent for liveMissingGrace. It keeps the account row, never credential
// bytes.
type ActiveResolver struct {
	Store    *store.Store
	Provider provider.Provider
	Claude   *claudeconfig.Store

	now func() time.Time // test seam; defaults to time.Now

	mu           sync.Mutex
	last         store.Account
	haveLast     bool
	missingSince time.Time
}

// Resolve has the same contract as ResolveActiveAccount.
func (r *ActiveResolver) Resolve(ctx context.Context) (store.Account, bool, error) {
	return r.resolveWith(ctx, resolveActive)
}

type resolveFunc func(context.Context, *store.Store, provider.Provider, *claudeconfig.Store) (store.Account, bool, bool, error)

func (r *ActiveResolver) resolveWith(ctx context.Context, resolve resolveFunc) (store.Account, bool, error) {
	acct, found, liveEmpty, err := resolve(ctx, r.Store, r.Provider, r.Claude)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	switch {
	case err != nil && errors.Is(err, secret.ErrUnavailable) && r.haveLast:
		return r.last, true, nil
	case err != nil:
		return store.Account{}, false, err
	case found:
		r.last, r.haveLast, r.missingSince = acct, true, time.Time{}
		return acct, true, nil
	case liveEmpty && r.haveLast:
		if r.missingSince.IsZero() {
			r.missingSince = now
		}
		if now.Sub(r.missingSince) < liveMissingGrace {
			return r.last, true, nil
		}
		fallthrough
	default:
		r.haveLast, r.missingSince = false, time.Time{}
		return store.Account{}, false, nil
	}
}
