package daemon

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/store"
)

// watcherHarness returns a watcher backed by a real (temp) store, with
// notifications captured and the clock pinned.
func watcherHarness(t *testing.T) (*ExternalSwitchWatcher, *store.Store, *[]string, *time.Time) {
	t.Helper()
	s := openStore(t)
	now := time.Now()
	var notified []string
	w := &ExternalSwitchWatcher{
		Store:  s,
		Stderr: io.Discard,
		Now:    func() time.Time { return now },
		Notify: func(title, _ string) { notified = append(notified, title) },
	}
	return w, s, &notified, &now
}

// An own switch (recent manual/autoswitch audit row) must never be
// flagged external, even across multiple observations.
func TestExternalWatch_OwnSwitchAttributed(t *testing.T) {
	w, s, notified, now := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "A") // baseline
	_ = s.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts: *now, FromLabel: "A", ToLabel: "B", Trigger: store.TriggerManual,
	})
	w.Observe(ctx, 2, "B")
	w.Observe(ctx, 2, "B")

	if len(*notified) != 0 {
		t.Errorf("own switch was flagged external: %v", *notified)
	}
	if !w.LastExternalAt().IsZero() {
		t.Errorf("LastExternalAt set for an own switch")
	}
}

// A label change with no explaining audit row is external — but only
// after the one-observation grace (the first sighting must NOT flag, so
// an own switch whose audit row lands a moment later isn't misclassified).
func TestExternalWatch_UnattributedChangeFlagsAfterGrace(t *testing.T) {
	w, s, notified, _ := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "A") // baseline
	w.Observe(ctx, 2, "B") // first sighting: pending, not flagged yet
	if len(*notified) != 0 {
		t.Fatalf("flagged before grace elapsed")
	}
	w.Observe(ctx, 2, "B") // second sighting: external
	if len(*notified) != 1 {
		t.Fatalf("want 1 notification, got %v", *notified)
	}
	if w.LastExternalAt().IsZero() {
		t.Errorf("LastExternalAt not recorded")
	}

	// The detection itself is audited with the external trigger…
	rows, err := s.ListSwitchAudit(ctx, 5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %v (err %v), want exactly 1", rows, err)
	}
	if rows[0].Trigger != store.TriggerExternal || rows[0].ToLabel != "B" || rows[0].FromLabel != "A" {
		t.Errorf("audit row = %+v", rows[0])
	}

	// …and that external row must NOT legitimize a later unexplained
	// change back to the same label (external rows are excluded from
	// attribution).
	w.Observe(ctx, 3, "C")
	w.Observe(ctx, 3, "C") // external #2 (audited; notification rate-limited)
	w.Observe(ctx, 2, "B")
	w.Observe(ctx, 2, "B") // would be wrongly "own" if external rows counted
	rows, _ = s.ListSwitchAudit(ctx, 10)
	if len(rows) != 3 {
		t.Errorf("want 3 external audit rows after ping-pong, got %d", len(rows))
	}
}

// The audit-row race: the publisher can observe the new label in the gap
// between the live-slot write and the audit insert. The grace observation
// must pick the row up and classify the switch as own.
func TestExternalWatch_AuditRowLandsDuringGrace(t *testing.T) {
	w, s, notified, now := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "A")
	w.Observe(ctx, 2, "B") // pending — audit row not written yet
	_ = s.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts: *now, ToLabel: "B", Trigger: store.TriggerAutoswitch,
	})
	w.Observe(ctx, 2, "B") // grace check finds the row → own
	if len(*notified) != 0 {
		t.Errorf("race window misclassified own switch as external: %v", *notified)
	}
}

// A stale audit row (older than the attribution window) must not explain
// a fresh change to the same label.
func TestExternalWatch_StaleAuditRowDoesNotAttribute(t *testing.T) {
	w, s, notified, now := watcherHarness(t)
	ctx := context.Background()

	_ = s.InsertSwitchAudit(ctx, store.SwitchAuditRecord{
		Ts: now.Add(-2 * time.Hour), ToLabel: "B", Trigger: store.TriggerManual,
	})
	w.Observe(ctx, 1, "A")
	w.Observe(ctx, 2, "B")
	w.Observe(ctx, 2, "B")
	if len(*notified) != 1 {
		t.Errorf("2h-old audit row wrongly attributed a fresh change: %v", *notified)
	}
}

// Unresolved observations (transient resolution failures) are ignored
// entirely: no baseline reset, no change events in either direction.
func TestExternalWatch_EmptyLabelIgnored(t *testing.T) {
	w, _, notified, _ := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "A")
	w.Observe(ctx, 0, "") // hiccup
	w.Observe(ctx, 1, "A")
	if len(*notified) != 0 {
		t.Errorf("flap through empty label produced events: %v", *notified)
	}
}

// Notifications are rate-limited; audit rows are not.
func TestExternalWatch_NotifyCooldown(t *testing.T) {
	w, s, notified, _ := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "A")
	w.Observe(ctx, 2, "B")
	w.Observe(ctx, 2, "B") // external #1 → notify
	w.Observe(ctx, 3, "C")
	w.Observe(ctx, 3, "C") // external #2, inside cooldown → no notify
	if len(*notified) != 1 {
		t.Errorf("want 1 notification under cooldown, got %d", len(*notified))
	}
	rows, _ := s.ListSwitchAudit(ctx, 10)
	if len(rows) != 2 {
		t.Errorf("want 2 audit rows regardless of cooldown, got %d", len(rows))
	}
}

// Renaming the active account changes its label but not its identity.
// Label-tracking misread that as an external switch (no audit row explains
// a rename) and fired the "external account switch" notification.
func TestExternalWatch_RenameIsNotExternal(t *testing.T) {
	w, s, notified, _ := watcherHarness(t)
	ctx := context.Background()

	w.Observe(ctx, 1, "Gem 3")    // baseline
	w.Observe(ctx, 1, "Gemini 3") // renamed — same account ID
	w.Observe(ctx, 1, "Gemini 3")
	if len(*notified) != 0 {
		t.Errorf("rename flagged as external switch: %v", *notified)
	}
	if rows, _ := s.ListSwitchAudit(ctx, 5); len(rows) != 0 {
		t.Errorf("rename produced audit rows: %v", rows)
	}
	if !w.LastExternalAt().IsZero() {
		t.Errorf("LastExternalAt set by a rename")
	}

	// A real external change after the rename is still caught, and the
	// notification names the CURRENT label.
	w.Observe(ctx, 2, "Other")
	w.Observe(ctx, 2, "Other")
	if len(*notified) != 1 {
		t.Errorf("real external switch after rename missed: %v", *notified)
	}
	rows, _ := s.ListSwitchAudit(ctx, 5)
	if len(rows) != 1 || rows[0].FromLabel != "Gemini 3" {
		t.Errorf("audit row should be from the renamed label, got %+v", rows)
	}
}
