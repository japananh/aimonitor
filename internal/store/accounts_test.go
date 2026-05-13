package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAccounts_CRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create.
	created, err := s.CreateAccount(ctx, Account{
		Label:      "personal",
		Email:      "alice@example.com",
		KeyringRef: "aimonitor-aaaa",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 || created.Provider != "claude" || created.CreatedAt.IsZero() {
		t.Fatalf("created defaults: %+v", created)
	}

	// Get by label.
	got, err := s.GetAccountByLabel(ctx, "personal")
	if err != nil {
		t.Fatalf("GetByLabel: %v", err)
	}
	if got.ID != created.ID || got.Email != "alice@example.com" {
		t.Errorf("got %+v, want id=%d email=%q", got, created.ID, "alice@example.com")
	}

	// Get by ID.
	got2, err := s.GetAccountByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got2.Label != "personal" {
		t.Errorf("GetByID label: %q", got2.Label)
	}

	// Duplicate label rejected.
	_, err = s.CreateAccount(ctx, Account{Label: "personal", KeyringRef: "aimonitor-bbbb"})
	if err == nil {
		t.Fatal("expected UNIQUE violation; got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}

	// Create a second account.
	_, err = s.CreateAccount(ctx, Account{Label: "work", KeyringRef: "aimonitor-bbbb"})
	if err != nil {
		t.Fatalf("Create #2: %v", err)
	}

	// List both, ordered by created_at.
	list, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2", len(list))
	}
	if list[0].Label != "personal" || list[1].Label != "work" {
		t.Errorf("List order wrong: %s, %s", list[0].Label, list[1].Label)
	}

	// UpdateAccountLastUsed.
	now := time.Now().Truncate(time.Millisecond)
	if err := s.UpdateAccountLastUsed(ctx, created.ID, now); err != nil {
		t.Fatalf("UpdateLastUsed: %v", err)
	}
	got3, _ := s.GetAccountByID(ctx, created.ID)
	if !got3.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v, want %v", got3.LastUsedAt, now)
	}

	// Rename.
	if err := s.RenameAccount(ctx, "personal", "home"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := s.GetAccountByLabel(ctx, "personal"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("after rename, old label should be 404; got %v", err)
	}
	got4, err := s.GetAccountByLabel(ctx, "home")
	if err != nil {
		t.Fatalf("Get after rename: %v", err)
	}
	if got4.ID != created.ID {
		t.Errorf("renamed account changed ID? %d -> %d", created.ID, got4.ID)
	}

	// Rename to taken label fails.
	if err := s.RenameAccount(ctx, "home", "work"); err == nil {
		t.Error("Rename to existing label: want error, got nil")
	}

	// Delete.
	if err := s.DeleteAccount(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetAccountByID(ctx, created.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("after delete: want ErrAccountNotFound, got %v", err)
	}

	// Re-delete is a not-found error.
	if err := s.DeleteAccount(ctx, created.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("re-delete: want ErrAccountNotFound, got %v", err)
	}
}

func TestCreateAccount_Validation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateAccount(ctx, Account{Label: "", KeyringRef: "x"}); err == nil {
		t.Error("empty label: want error")
	}
	if _, err := s.CreateAccount(ctx, Account{Label: "x", KeyringRef: ""}); err == nil {
		t.Error("empty keyring ref: want error")
	}
}

func TestRenameAccount_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.RenameAccount(ctx, "nope", "new"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("want ErrAccountNotFound, got %v", err)
	}
	if err := s.RenameAccount(ctx, "x", ""); err == nil {
		t.Error("empty newLabel: want error")
	}
}
