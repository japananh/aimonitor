package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

func TestProbeCache_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Need an account row (probe_results has FK to accounts).
	acct, err := s.CreateAccount(ctx, Account{Label: "p", KeyringRef: "ref"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Miss before any write.
	if _, err := s.GetProbeResult(ctx, acct.ID); !errors.Is(err, ErrProbeNotFound) {
		t.Fatalf("want ErrProbeNotFound, got %v", err)
	}

	// Fresh write.
	rl := provider.RateLimit{
		ProbedAt:        time.Now(),
		TokensRemaining: 12345,
		ResetAt:         time.Now().Add(1 * time.Hour).Truncate(time.Millisecond),
		HTTPStatus:      200,
	}
	if err := s.PutProbeResult(ctx, acct.ID, rl); err != nil {
		t.Fatalf("PutProbeResult: %v", err)
	}

	// Fresh read.
	got, err := s.GetProbeResult(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetProbeResult: %v", err)
	}
	if got.TokensRemaining != 12345 || got.HTTPStatus != 200 {
		t.Errorf("got %+v, want TokensRemaining=12345 HTTPStatus=200", got)
	}
	if !got.ResetAt.Equal(rl.ResetAt) {
		t.Errorf("ResetAt mismatch: %v vs %v", got.ResetAt, rl.ResetAt)
	}
}

func TestProbeCache_Stale(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, _ := s.CreateAccount(ctx, Account{Label: "p", KeyringRef: "ref"})

	// Backdated row (older than TTL).
	old := provider.RateLimit{
		ProbedAt:        time.Now().Add(-2 * ProbeCacheTTL),
		TokensRemaining: 999,
		ResetAt:         time.Now(),
		HTTPStatus:      200,
	}
	if err := s.PutProbeResult(ctx, acct.ID, old); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.GetProbeResult(ctx, acct.ID)
	if !errors.Is(err, ErrProbeStale) {
		t.Errorf("want ErrProbeStale, got %v", err)
	}
	if got.TokensRemaining != 999 {
		t.Errorf("stale rl should still carry data; got TokensRemaining=%d", got.TokensRemaining)
	}
}

func TestProbeCache_Upsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, _ := s.CreateAccount(ctx, Account{Label: "p", KeyringRef: "ref"})

	if err := s.PutProbeResult(ctx, acct.ID, provider.RateLimit{TokensRemaining: 1, ProbedAt: time.Now(), HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProbeResult(ctx, acct.ID, provider.RateLimit{TokensRemaining: 2, ProbedAt: time.Now(), HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProbeResult(ctx, acct.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TokensRemaining != 2 {
		t.Errorf("upsert should overwrite; got %d, want 2", got.TokensRemaining)
	}
}
