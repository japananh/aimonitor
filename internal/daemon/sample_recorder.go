package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

const (
	// sampleRecorderActiveTTL bounds how often the recorder re-resolves the
	// active account. OnSample fires once per usage line — hundreds in a
	// burst during an active session — and resolving touches the OS keyring,
	// so we cache the answer briefly. The active account only changes on a
	// switch (and a swap has its own post-switch cooldown), so a few seconds
	// of staleness misattributes at most a handful of lines right at a
	// switch boundary.
	sampleRecorderActiveTTL = 5 * time.Second

	// sampleRecorderStaleThreshold is the maximum age of a JSONL sample we
	// will persist. Live tailing fires within milliseconds of a line being
	// written, so fresh samples pass easily. The guard drops:
	//   - Fresh-install replay: offsets start at 0, so the watcher re-reads
	//     every historical line in the transcript tree. Those carry OLD
	//     timestamps and would all be misattributed to whoever is active
	//     now, so we skip them. (An upgrade keeps offsets at EOF — nothing
	//     replays, and from-now-on capture is automatic.)
	//   - Long daemon downtime: only genuinely recent catch-up writes are
	//     attributed; older ones are skipped.
	sampleRecorderStaleThreshold = time.Hour

	// samplePruneInterval is how many successful inserts pass between
	// opportunistic retention prunes. A long-lived daemon (no restart for
	// weeks) still trims usage_samples without paying a DELETE per insert.
	samplePruneInterval = 2000
)

// SampleRecorder persists per-message token samples to usage_samples,
// attributing each to the account active when the line was written. It is
// the bridge from the watcher's OnSample (which knows only the file + token
// counts) to a durable, account-keyed token history that powers the
// daily/hourly breakdown.
//
// Attribution is "active now": the watcher fires in near-real-time, so the
// account holding the live credential is the one that produced the message.
// We deliberately do NOT reconstruct history from switch_audit — v1 is
// from-now-on only (enforced by the stale-threshold guard).
type SampleRecorder struct {
	store         *store.Store
	resolveActive func(ctx context.Context) (store.Account, bool, error)
	clock         func() time.Time
	stale         time.Duration
	ttl           time.Duration
	onError       func(error)

	mu        sync.Mutex
	cached    store.Account
	cachedOK  bool
	cachedExp time.Time
	inserts   int
}

// NewSampleRecorder wires a recorder. resolveActive is the shared
// active-account resolver (ResolveActiveAccount bound to the live
// provider + claude.json); onError may be nil.
func NewSampleRecorder(
	st *store.Store,
	resolveActive func(ctx context.Context) (store.Account, bool, error),
	onError func(error),
) *SampleRecorder {
	return &SampleRecorder{
		store:         st,
		resolveActive: resolveActive,
		clock:         time.Now,
		stale:         sampleRecorderStaleThreshold,
		ttl:           sampleRecorderActiveTTL,
		onError:       onError,
	}
}

// Record persists one sample, or silently drops it when it is too old to
// attribute or when no active account resolves. Safe to call from the
// watcher's single event goroutine; the internal cache is mutex-guarded so
// it also tolerates concurrent callers.
func (r *SampleRecorder) Record(ctx context.Context, ev SampleEvent) {
	now := r.clock()
	if ts := ev.Sample.Ts; !ts.IsZero() && now.Sub(ts) > r.stale {
		return // replayed history / long downtime — can't attribute correctly
	}
	acct, ok := r.activeAccount(ctx, now)
	if !ok {
		return // no resolvable active account → nothing to attribute to
	}

	inserted, err := r.store.InsertUsageSample(ctx, acct.ID, store.TokenSample{
		Ts:         ev.Sample.Ts,
		MessageID:  ev.Sample.MessageID,
		RequestID:  ev.Sample.RequestID,
		Input:      ev.Sample.InputTokens,
		Output:     ev.Sample.OutputTokens,
		CacheRead:  ev.Sample.CacheRead,
		CacheWrite: ev.Sample.CacheWrite,
		Model:      ev.Sample.Model,
		Project:    projectFromPath(ev.Path),
	})
	if err != nil {
		r.report(err)
		return
	}
	if inserted {
		r.maybePrune(ctx)
	}
}

// activeAccount returns the cached active account, re-resolving when the
// cache has expired. A resolution error is cached as "not found" for the
// TTL so a persistent failure can't hammer the keyring once per line.
func (r *SampleRecorder) activeAccount(ctx context.Context, now time.Time) (store.Account, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.cachedExp) {
		return r.cached, r.cachedOK
	}
	acct, ok, err := r.resolveActive(ctx)
	if err != nil {
		r.report(err)
		acct, ok = store.Account{}, false
	}
	r.cached, r.cachedOK, r.cachedExp = acct, ok, now.Add(r.ttl)
	return r.cached, r.cachedOK
}

// maybePrune trims old rows every samplePruneInterval inserts. Counter is
// mutex-guarded; the DELETE is a single indexed scan so running it inline is
// cheap at this cadence.
func (r *SampleRecorder) maybePrune(ctx context.Context) {
	r.mu.Lock()
	r.inserts++
	due := r.inserts%samplePruneInterval == 0
	r.mu.Unlock()
	if !due {
		return
	}
	if _, err := r.store.PruneUsageSamples(ctx, store.UsageSamplesRetention); err != nil {
		r.report(err)
	}
}

// projectFromPath extracts the Claude Code project from a JSONL transcript
// path: the parent directory's name under ~/.claude/projects, which encodes
// the project's working directory (e.g. ".../projects/-Users-nana-foo/x.jsonl"
// → "-Users-nana-foo"). Returns "" when the path has no parent component.
func projectFromPath(path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Base(filepath.Dir(path))
	if dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	return dir
}

func (r *SampleRecorder) report(err error) {
	if r.onError != nil {
		r.onError(err)
	}
}
