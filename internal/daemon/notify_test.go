package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

// newNotifier wires a ThresholdNotifier whose notifications are captured into
// the returned slice pointer instead of hitting osascript.
func newNotifier(t *testing.T, s *store.Store) (*ThresholdNotifier, *[]string) {
	t.Helper()
	var got []string
	n := &ThresholdNotifier{
		Store:  s,
		Notify: func(title, _ string) { got = append(got, title) },
	}
	return n, &got
}

// disableAutoSwap turns auto-swap off so the threshold notifier is the active
// signal (when auto-swap is on it deliberately stays silent).
func disableAutoSwap(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.PutSetting(context.Background(), SettingsKeyAutoSwapEnabled, "false"); err != nil {
		t.Fatal(err)
	}
}

func TestNotify_WarnThenCrit_OncePerLevel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	disableAutoSwap(t, s)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "gem", KeyringRef: "r"})
	reset := time.Now().Add(2 * time.Hour)
	n, got := newNotifier(t, s)

	// 82% → one warn.
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 82, FiveHourResetAt: reset})
	n.Evaluate(ctx, "gem")
	if len(*got) != 1 {
		t.Fatalf("want 1 warn notification, got %d (%v)", len(*got), *got)
	}
	// Still 82% next tick → no repeat.
	n.Evaluate(ctx, "gem")
	if len(*got) != 1 {
		t.Fatalf("warn must not repeat within a window, got %d (%v)", len(*got), *got)
	}
	// Climbs to 96% → escalates to crit (one more).
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 96, FiveHourResetAt: reset})
	n.Evaluate(ctx, "gem")
	if len(*got) != 2 {
		t.Fatalf("want crit escalation, got %d (%v)", len(*got), *got)
	}
	// 96% again → no repeat.
	n.Evaluate(ctx, "gem")
	if len(*got) != 2 {
		t.Fatalf("crit must not repeat within a window, got %d (%v)", len(*got), *got)
	}
}

func TestNotify_WindowReset_ReArms(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	disableAutoSwap(t, s)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "gem", KeyringRef: "r"})
	n, got := newNotifier(t, s)

	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 96, FiveHourResetAt: time.Now().Add(time.Hour)})
	n.Evaluate(ctx, "gem")
	if len(*got) != 1 {
		t.Fatalf("want 1 crit, got %d", len(*got))
	}
	// New reset time = window rolled over → a fresh crit is allowed.
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 96, FiveHourResetAt: time.Now().Add(6 * time.Hour)})
	n.Evaluate(ctx, "gem")
	if len(*got) != 2 {
		t.Fatalf("window reset must re-arm, got %d", len(*got))
	}
}

func TestNotify_SuppressedWhenAutoSwapOn(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// Auto-swap left at its default (enabled) → notifier stays silent.
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "gem", KeyringRef: "r"})
	n, got := newNotifier(t, s)
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 99, FiveHourResetAt: time.Now().Add(time.Hour)})
	n.Evaluate(ctx, "gem")
	if len(*got) != 0 {
		t.Fatalf("notifier must stay silent while auto-swap is on, got %v", *got)
	}
}

func TestNotify_DisabledNoNotifications(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	disableAutoSwap(t, s)
	if err := s.PutSetting(ctx, SettingsKeyNotifyEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "gem", KeyringRef: "r"})
	n, got := newNotifier(t, s)
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 99, FiveHourResetAt: time.Now().Add(time.Hour)})
	n.Evaluate(ctx, "gem")
	if len(*got) != 0 {
		t.Fatalf("notify.enabled=false must silence notifications, got %v", *got)
	}
}
