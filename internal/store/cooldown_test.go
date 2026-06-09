package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCooldown_SetClearRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	acct, err := s.CreateAccount(ctx, Account{Label: "a", KeyringRef: "r"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Fresh account: no cooldown.
	got, _ := s.GetAccountByID(ctx, acct.ID)
	if !got.CooldownUntil.IsZero() || got.CooldownReason != "" {
		t.Fatalf("new account should have no cooldown, got until=%v reason=%q", got.CooldownUntil, got.CooldownReason)
	}

	// Set, then read back through ListAccounts (the path the daemon/UI use).
	until := time.Now().Add(30 * time.Minute).Truncate(time.Millisecond)
	if err := s.SetCooldown(ctx, acct.ID, until, "rate-limited (429)"); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}
	accts, _ := s.ListAccounts(ctx)
	if len(accts) != 1 || !accts[0].CooldownUntil.Equal(until) || accts[0].CooldownReason != "rate-limited (429)" {
		t.Fatalf("cooldown not persisted: %+v", accts[0])
	}

	// Clear restores the zero state.
	if err := s.ClearCooldown(ctx, acct.ID); err != nil {
		t.Fatalf("ClearCooldown: %v", err)
	}
	got, _ = s.GetAccountByID(ctx, acct.ID)
	if !got.CooldownUntil.IsZero() || got.CooldownReason != "" {
		t.Fatalf("cooldown not cleared: until=%v reason=%q", got.CooldownUntil, got.CooldownReason)
	}
}

func TestCooldown_SetMissingAccount(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	if err := s.SetCooldown(context.Background(), 999, time.Now(), "x"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("SetCooldown on missing account = %v, want ErrAccountNotFound", err)
	}
}
