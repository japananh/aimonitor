package store

import (
	"context"
	"testing"
)

func TestSetNeedsRelogin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, err := s.CreateAccount(ctx, Account{Label: "x", KeyringRef: "r"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Default is false on a fresh row.
	if got, _ := s.GetAccountByID(ctx, a.ID); got.NeedsRelogin {
		t.Fatal("NeedsRelogin should default to false")
	}
	// Set, then read back true.
	if err := s.SetNeedsRelogin(ctx, a.ID, true); err != nil {
		t.Fatalf("SetNeedsRelogin(true): %v", err)
	}
	if got, _ := s.GetAccountByID(ctx, a.ID); !got.NeedsRelogin {
		t.Error("NeedsRelogin should be true after set")
	}
	// Clear, then read back false.
	if err := s.SetNeedsRelogin(ctx, a.ID, false); err != nil {
		t.Fatalf("SetNeedsRelogin(false): %v", err)
	}
	if got, _ := s.GetAccountByID(ctx, a.ID); got.NeedsRelogin {
		t.Error("NeedsRelogin should be false after clear")
	}
}
