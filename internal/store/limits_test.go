package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

func TestLimits_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, err := s.CreateAccount(ctx, Account{Label: "l", KeyringRef: "ref"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := s.GetLimits(ctx, acct.ID); !errors.Is(err, ErrLimitsNotFound) {
		t.Fatalf("want ErrLimitsNotFound, got %v", err)
	}

	want := provider.Limits{
		FiveHourPct:     73.5,
		FiveHourResetAt: time.Now().Add(2 * time.Hour).Truncate(time.Millisecond),
		SevenDayPct:     41.0,
		SevenDayResetAt: time.Now().Add(5 * 24 * time.Hour).Truncate(time.Millisecond),
		Source:          "oauth",
		FetchedAt:       time.Now().Truncate(time.Millisecond),
	}
	if err := s.PutLimits(ctx, acct.ID, want); err != nil {
		t.Fatalf("PutLimits: %v", err)
	}

	got, err := s.GetLimits(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetLimits: %v", err)
	}
	if got.FiveHourPct != want.FiveHourPct || got.SevenDayPct != want.SevenDayPct {
		t.Errorf("percentages: got %+v want %+v", got, want)
	}
	if !got.FiveHourResetAt.Equal(want.FiveHourResetAt) {
		t.Errorf("FiveHourResetAt: got %v want %v", got.FiveHourResetAt, want.FiveHourResetAt)
	}
	if got.Source != "oauth" {
		t.Errorf("Source = %q want oauth", got.Source)
	}
}

func TestLimits_NullableResets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, _ := s.CreateAccount(ctx, Account{Label: "l", KeyringRef: "ref"})

	// Anthropic may omit reset times for windows the account hasn't used.
	// Storing/reading zero times should round-trip cleanly via the
	// nullable INTEGER columns.
	if err := s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 5.0}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.GetLimits(ctx, acct.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.FiveHourResetAt.IsZero() {
		t.Errorf("FiveHourResetAt should be zero when omitted, got %v", got.FiveHourResetAt)
	}
	if !got.SevenDayResetAt.IsZero() {
		t.Errorf("SevenDayResetAt should be zero when omitted, got %v", got.SevenDayResetAt)
	}
}

func TestLimits_Upsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, _ := s.CreateAccount(ctx, Account{Label: "l", KeyringRef: "ref"})

	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 10})
	_ = s.PutLimits(ctx, acct.ID, provider.Limits{FiveHourPct: 90})

	got, _ := s.GetLimits(ctx, acct.ID)
	if got.FiveHourPct != 90 {
		t.Errorf("upsert should overwrite; got %v want 90", got.FiveHourPct)
	}
}
