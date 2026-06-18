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

func TestConfigAudit_InsertListAndFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	recs := []ConfigAuditRecord{
		{Key: "mcp.slack.enabled", OldValue: "", NewValue: "false", Source: "cli"},
		{Key: "mcp.slack.enabled", OldValue: "false", NewValue: "true", Source: "cli"},
		{Key: "mcp.clickup.read_only", OldValue: "", NewValue: "true", Source: "import"},
	}
	for i, r := range recs {
		r.Ts = time.Now().Add(time.Duration(i) * time.Second)
		if err := s.InsertConfigAudit(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	all, err := s.ListConfigAudit(ctx, "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d rows, want 3", len(all))
	}
	// Newest first.
	if all[0].Key != "mcp.clickup.read_only" || all[0].Source != "import" {
		t.Errorf("got[0] = %+v, want clickup read_only/import", all[0])
	}

	// Filter by key + verify old/new + unset mapping.
	slack, err := s.ListConfigAudit(ctx, "mcp.slack.enabled", 10)
	if err != nil {
		t.Fatalf("List(key): %v", err)
	}
	if len(slack) != 2 {
		t.Fatalf("slack rows = %d, want 2", len(slack))
	}
	if slack[0].OldValue != "false" || slack[0].NewValue != "true" {
		t.Errorf("slack[0] = %+v, want old=false new=true", slack[0])
	}
	if slack[1].OldValue != "" { // previously-unset persists as "" (NULL)
		t.Errorf("slack[1].OldValue = %q, want empty", slack[1].OldValue)
	}
}

func TestConfigAudit_KeyRequired(t *testing.T) {
	s := openTestStore(t)
	if err := s.InsertConfigAudit(context.Background(), ConfigAuditRecord{NewValue: "x"}); err == nil {
		t.Error("missing key: want error")
	}
}
