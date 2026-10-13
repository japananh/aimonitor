package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

type resolveStep struct {
	acct      store.Account
	found     bool
	liveEmpty bool
	err       error
}

// scriptedResolver feeds resolveWith one step per call.
type scriptedResolver struct{ steps []resolveStep }

func (s *scriptedResolver) fn(context.Context, *store.Store, provider.Provider, *claudeconfig.Store) (store.Account, bool, bool, error) {
	st := s.steps[0]
	if len(s.steps) > 1 {
		s.steps = s.steps[1:]
	}
	return st.acct, st.found, st.liveEmpty, st.err
}

func keychainDown() error {
	return fmt.Errorf("active credential: %w", &secret.ReadError{Service: "svc", Account: "acct", Reason: "timed out after 5s"})
}

func newResolver(now *time.Time) *ActiveResolver {
	return &ActiveResolver{now: func() time.Time { return *now }}
}

var (
	be2 = store.Account{ID: 1, Label: "BE 2"}
	be1 = store.Account{ID: 2, Label: "BE 1"}
)

// The incident: reads fail around 14:29 while the daemon is sampling. The
// account that was active must keep being returned, with no error.
func TestActiveResolver_TransientFailureKeepsLastKnownAccount(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 29, 0, 0, time.UTC)
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{{acct: be2, found: true}, {err: keychainDown()}}}

	if got, ok, err := r.resolveWith(context.Background(), s.fn); err != nil || !ok || got.ID != be2.ID {
		t.Fatalf("healthy resolve = %+v, %v, %v", got, ok, err)
	}
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		got, ok, err := r.resolveWith(context.Background(), s.fn)
		if err != nil || !ok || got.ID != be2.ID {
			t.Fatalf("resolve during outage (%d) = %+v, %v, %v; want last known BE 2", i, got, ok, err)
		}
	}
}

func TestActiveResolver_TransientFailureWithoutHistoryReturnsError(t *testing.T) {
	now := time.Now()
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{{err: keychainDown()}}}

	_, ok, err := r.resolveWith(context.Background(), s.fn)
	if ok || !errors.Is(err, secret.ErrUnavailable) {
		t.Fatalf("ok=%v err=%v, want not-found with the transient error", ok, err)
	}
}

func TestActiveResolver_OtherErrorsAreNotMasked(t *testing.T) {
	now := time.Now()
	r := newResolver(&now)
	boom := errors.New("list accounts: db closed")
	s := &scriptedResolver{steps: []resolveStep{{acct: be2, found: true}, {err: boom}}}

	_, _, _ = r.resolveWith(context.Background(), s.fn)
	if _, ok, err := r.resolveWith(context.Background(), s.fn); ok || !errors.Is(err, boom) {
		t.Fatalf("ok=%v err=%v, want the non-keychain error surfaced", ok, err)
	}
}

func TestActiveResolver_RecoveryReplacesLastKnown(t *testing.T) {
	now := time.Now()
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{
		{acct: be2, found: true},
		{err: keychainDown()},
		{acct: be1, found: true}, // Claude Code rewrote the item as BE 1
	}}

	_, _, _ = r.resolveWith(context.Background(), s.fn)
	_, _, _ = r.resolveWith(context.Background(), s.fn)
	if got, ok, err := r.resolveWith(context.Background(), s.fn); err != nil || !ok || got.ID != be1.ID {
		t.Fatalf("after recovery = %+v, %v, %v; want BE 1", got, ok, err)
	}
}

// An absent item may just be Claude Code mid-rewrite: hold the last account for
// the grace period, then let go.
func TestActiveResolver_MissingItemChangesStateOnlyAfterGrace(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{{acct: be2, found: true}, {liveEmpty: true}}}

	_, _, _ = r.resolveWith(context.Background(), s.fn)
	for _, elapsed := range []time.Duration{0, 5 * time.Minute, liveMissingGrace - time.Second} {
		now = time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC).Add(elapsed)
		if got, ok, _ := r.resolveWith(context.Background(), s.fn); !ok || got.ID != be2.ID {
			t.Fatalf("at +%s: = %+v, %v; want BE 2 held during grace", elapsed, got, ok)
		}
	}
	now = now.Add(2 * time.Second) // past 10 minutes of consistent absence
	if _, ok, err := r.resolveWith(context.Background(), s.fn); ok || err != nil {
		t.Fatalf("after grace: ok=%v err=%v, want no active account and no error", ok, err)
	}
	// And it stays released: the last account is no longer resurrected.
	if _, ok, _ := r.resolveWith(context.Background(), s.fn); ok {
		t.Fatal("released state must stay released")
	}
}

func TestActiveResolver_ReappearingItemResetsMissingClock(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{
		{acct: be2, found: true},
		{liveEmpty: true},
		{acct: be2, found: true},
		{liveEmpty: true},
	}}
	ctx := context.Background()

	_, _, _ = r.resolveWith(ctx, s.fn)
	_, _, _ = r.resolveWith(ctx, s.fn) // absent, clock starts
	now = now.Add(9 * time.Minute)
	_, _, _ = r.resolveWith(ctx, s.fn) // back: clock resets
	now = now.Add(9 * time.Minute)
	if got, ok, _ := r.resolveWith(ctx, s.fn); !ok || got.ID != be2.ID {
		t.Fatalf("= %+v, %v; two short absences must not add up to a confirmed one", got, ok)
	}
}

// A credential that is present but matches no managed account is a real change
// (someone logged into an unmanaged account), so it must show immediately.
func TestActiveResolver_UnmatchedCredentialClearsImmediately(t *testing.T) {
	now := time.Now()
	r := newResolver(&now)
	s := &scriptedResolver{steps: []resolveStep{{acct: be2, found: true}, {liveEmpty: false}}}

	_, _, _ = r.resolveWith(context.Background(), s.fn)
	if _, ok, err := r.resolveWith(context.Background(), s.fn); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want immediate no-active for an unmanaged credential", ok, err)
	}
}
