package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

func TestMarkRelogin(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a, err := s.CreateAccount(ctx, store.Account{Label: "x", KeyringRef: "r"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// A wrapped expired-token error sets the flag (errors.As must unwrap it).
	expired := fmt.Errorf("refresh %q token: %w", "x", &claude.TokenRefreshExpiredError{Status: 400})
	markRelogin(ctx, s, a, expired)
	if got, _ := s.GetAccountByID(ctx, a.ID); !got.NeedsRelogin {
		t.Fatal("expired-token error should set needs_relogin")
	}

	// A non-token error (network, 429) must NOT clear an existing flag.
	markRelogin(ctx, s, a, errors.New("network boom"))
	if got, _ := s.GetAccountByID(ctx, a.ID); !got.NeedsRelogin {
		t.Error("unrelated error must leave needs_relogin set")
	}

	// A successful refresh (nil err) clears it.
	markRelogin(ctx, s, a, nil)
	if got, _ := s.GetAccountByID(ctx, a.ID); got.NeedsRelogin {
		t.Error("nil err (success) should clear needs_relogin")
	}
}
