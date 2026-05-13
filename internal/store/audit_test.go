package store

import (
	"context"
	"testing"
	"time"
)

func TestSwitchAudit_InsertAndList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rows := []SwitchAuditRecord{
		{ToLabel: "work", Trigger: TriggerFirstRun},
		{FromLabel: "work", ToLabel: "personal", Trigger: TriggerManual},
		{FromLabel: "personal", ToLabel: "work", Trigger: TriggerAutoswitch,
			Reason: "tripwire 60 crossed", FromProbedRemaining: 1000, ToProbedRemaining: 50000},
	}
	for i, r := range rows {
		r.Ts = time.Now().Add(time.Duration(i) * time.Second)
		if err := s.InsertSwitchAudit(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := s.ListSwitchAudit(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	// Newest first.
	if got[0].Trigger != TriggerAutoswitch {
		t.Errorf("got[0].Trigger = %q, want autoswitch", got[0].Trigger)
	}
	if got[0].FromProbedRemaining != 1000 || got[0].ToProbedRemaining != 50000 {
		t.Errorf("probed remaining: %d -> %d", got[0].FromProbedRemaining, got[0].ToProbedRemaining)
	}
	// First-run row has no FromLabel.
	if got[2].FromLabel != "" || got[2].Trigger != TriggerFirstRun {
		t.Errorf("got[2] first-run: %+v", got[2])
	}
}

func TestSwitchAudit_Validation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.InsertSwitchAudit(ctx, SwitchAuditRecord{ToLabel: "x"}); err == nil {
		t.Error("missing trigger: want error")
	}
	if err := s.InsertSwitchAudit(ctx, SwitchAuditRecord{Trigger: TriggerManual}); err == nil {
		t.Error("missing to_label: want error")
	}
}

func TestSwitchAudit_LimitDefault(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		if err := s.InsertSwitchAudit(ctx, SwitchAuditRecord{
			ToLabel: "x", Trigger: TriggerManual,
			Ts: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListSwitchAudit(ctx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 20 {
		t.Errorf("default limit: got %d rows, want 20", len(got))
	}
}
