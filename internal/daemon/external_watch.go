package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

const (
	// externalAttributionWindow is how recent a non-external switch_audit
	// row must be to attribute an observed active-account change to
	// aimonitor itself. Own switches insert their audit row milliseconds
	// after writing the live slot, and the watcher observes on a ~2s
	// cadence with a one-observation grace — 30s is generous.
	externalAttributionWindow = 30 * time.Second

	// externalNotifyCooldown rate-limits the user-facing notification.
	// Two auto-switchers fighting can flip the slot every few minutes;
	// every flip is still audited, but the banner fires at most once per
	// cooldown so Notification Center isn't flooded.
	externalNotifyCooldown = 5 * time.Minute
)

// ExternalSwitchWatcher detects active-account changes that aimonitor did
// not perform: another credential manager (or a manual `claude /login`)
// rewrote the live slot. Detection is by attribution gap — the active
// label changed, and no recent non-external switch_audit row explains it.
// Only aimonitor's own switches write manual/autoswitch rows, so an
// unexplained change can only be an external actor. This is tool-agnostic
// by construction: nothing here knows about any specific other app.
//
// On detection it logs, writes a TriggerExternal audit row (giving
// `aimonitor log` the full history and any future deference logic a
// persistent, restart-proof signal), and posts a rate-limited
// notification.
type ExternalSwitchWatcher struct {
	Store *store.Store

	// Notify posts the user-facing banner. Nil → macOS notification via
	// osascript. Injectable for tests.
	Notify func(title, body string)

	// Stderr receives operational lines. Nil → os.Stderr.
	Stderr io.Writer

	// Now is the clock, injectable for tests. Nil → time.Now.
	Now func() time.Time

	// Identity is tracked by account ID, not label: a rename changes the
	// label of the SAME account, which label-tracking misread as an
	// external switch (no audit row explains a rename). IDs are stable
	// across renames; labels are kept only for human-facing messages.
	mu           sync.Mutex
	initialized  bool
	lastID       int64
	lastLabel    string
	pendingID    int64 // change seen once but not yet attributed — grace
	lastExternal time.Time
	lastNotified time.Time
}

// Observe feeds the watcher one resolution of the active account (the
// StatusPublisher calls it every publish tick). Unresolved observations
// (id <= 0 or empty label) are ignored entirely — a transient resolution
// failure must not register as a change in either direction.
func (w *ExternalSwitchWatcher) Observe(ctx context.Context, id int64, label string) {
	if id <= 0 || label == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.initialized {
		// First sighting after daemon start: baseline only. Changes that
		// happened while the daemon was down can't be attributed either
		// way, so they're absorbed silently.
		w.initialized = true
		w.lastID = id
		w.lastLabel = label
		return
	}
	if id == w.lastID {
		// Same account. Track label drift (a rename) silently — same
		// identity, nothing switched.
		w.lastLabel = label
		w.pendingID = 0
		return
	}

	// The active account changed. Attribute it: a recent audit row from
	// aimonitor's own switch paths (manual CLI/widget, auto-swap) claims
	// the change as ours.
	if w.ownSwitchTo(ctx, label) {
		w.lastID = id
		w.lastLabel = label
		w.pendingID = 0
		return
	}

	// Unattributed. Hold for one more observation before declaring it
	// external: an own switch writes its audit row milliseconds AFTER the
	// live slot, so a publish tick can land in between. By the next tick
	// (~2s) a genuine own switch is always attributable.
	if w.pendingID != id {
		w.pendingID = id
		return
	}

	// Second consecutive unattributed sighting — external.
	w.flagExternal(ctx, w.lastLabel, label)
	w.lastID = id
	w.lastLabel = label
	w.pendingID = 0
}

// LastExternalAt returns when the most recent external switch was
// detected (zero when none has been). Published in the daemon status so
// the widget and any deference logic can see it.
func (w *ExternalSwitchWatcher) LastExternalAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastExternal
}

// ownSwitchTo reports whether aimonitor itself recently switched to
// label. Caller holds w.mu.
func (w *ExternalSwitchWatcher) ownSwitchTo(ctx context.Context, label string) bool {
	rec, err := w.Store.LatestSwitchTo(ctx, label)
	if err != nil {
		return false // no row (or read failure): not attributable to us
	}
	return w.now().Sub(rec.Ts) <= externalAttributionWindow
}

// flagExternal records and surfaces one detected external switch. Caller
// holds w.mu.
func (w *ExternalSwitchWatcher) flagExternal(ctx context.Context, from, to string) {
	now := w.now()
	w.lastExternal = now

	fmt.Fprintf(w.stderr(), "external-switch: active account changed %q → %q outside aimonitor\n", from, to)

	if err := w.Store.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts:        now,
		FromLabel: from,
		ToLabel:   to,
		Trigger:   store.TriggerExternal,
		Reason:    "live credential changed outside aimonitor (another credential manager or `claude /login`)",
	}); err != nil {
		fmt.Fprintf(w.stderr(), "external-switch: record audit: %v\n", err)
	}

	if now.Sub(w.lastNotified) >= externalNotifyCooldown {
		w.lastNotified = now
		w.notify("External account switch detected",
			fmt.Sprintf("Another app switched the active account to %q. aimonitor has followed it.", to))
	}
}

func (w *ExternalSwitchWatcher) notify(title, body string) {
	if w.Notify != nil {
		w.Notify(title, body)
		return
	}
	notifyMacOS(title, body)
}

func (w *ExternalSwitchWatcher) stderr() io.Writer {
	if w.Stderr != nil {
		return w.Stderr
	}
	return os.Stderr
}

func (w *ExternalSwitchWatcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
