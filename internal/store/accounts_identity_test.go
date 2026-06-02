package store

import (
	"context"
	"errors"
	"testing"
)

func TestAccountIdentity_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	created, err := s.CreateAccount(ctx, Account{
		Label:            "work",
		Email:            "me@example.com",
		OrganizationUUID: "ORG-1",
		OrganizationName: "Acme",
		KeyringRef:       "ref-1",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if created.OrganizationUUID != "ORG-1" || created.OrganizationName != "Acme" {
		t.Errorf("create returned wrong identity: %+v", created)
	}

	got, err := s.GetAccountByLabel(ctx, "work")
	if err != nil {
		t.Fatalf("GetAccountByLabel: %v", err)
	}
	if got.Email != "me@example.com" || got.OrganizationUUID != "ORG-1" || got.OrganizationName != "Acme" {
		t.Errorf("read identity mismatch: %+v", got)
	}
}

func TestGetAccountByIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, _ := s.CreateAccount(ctx, Account{Label: "personal", Email: "x@example.com", OrganizationUUID: "ORG-P", KeyringRef: "r1"})
	// Same email, different org → distinct identity, must NOT match.
	_, _ = s.CreateAccount(ctx, Account{Label: "x-at-work", Email: "x@example.com", OrganizationUUID: "ORG-W", KeyringRef: "r2"})

	got, err := s.GetAccountByIdentity(ctx, "x@example.com", "ORG-P")
	if err != nil {
		t.Fatalf("GetAccountByIdentity: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("matched wrong account: got id=%d want %d", got.ID, a.ID)
	}

	// Unknown identity.
	if _, err := s.GetAccountByIdentity(ctx, "nobody@example.com", "ORG-P"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("want ErrAccountNotFound for unknown identity, got %v", err)
	}

	// Empty email never matches (legacy rows have no identity yet).
	if _, err := s.GetAccountByIdentity(ctx, "", "ORG-P"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("empty email should never match, got %v", err)
	}
}

func TestUpdateAccountIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Simulate a legacy row added before 0003 (no identity captured).
	a, _ := s.CreateAccount(ctx, Account{Label: "legacy", KeyringRef: "r1"})
	if a.Email != "" || a.OrganizationUUID != "" {
		t.Fatalf("precondition: legacy row should have empty identity, got %+v", a)
	}

	if err := s.UpdateAccountIdentity(ctx, a.ID, "back@example.com", "ORG-B", "Backfill Inc"); err != nil {
		t.Fatalf("UpdateAccountIdentity: %v", err)
	}
	got, _ := s.GetAccountByID(ctx, a.ID)
	if got.Email != "back@example.com" || got.OrganizationUUID != "ORG-B" || got.OrganizationName != "Backfill Inc" {
		t.Errorf("identity not backfilled: %+v", got)
	}

	// And it's now findable by identity.
	if _, err := s.GetAccountByIdentity(ctx, "back@example.com", "ORG-B"); err != nil {
		t.Errorf("backfilled identity not findable: %v", err)
	}

	// Unknown id.
	if err := s.UpdateAccountIdentity(ctx, 99999, "a", "b", "c"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("want ErrAccountNotFound for unknown id, got %v", err)
	}
}
