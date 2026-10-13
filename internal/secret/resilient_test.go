package secret

import (
	"errors"
	"testing"
	"time"
)

// scriptedKeyring returns queued results for Get and counts the calls.
type scriptedKeyring struct {
	MemoryKeyring
	results []error // consumed one per Get; nil = succeed with "v"
	calls   int
}

func (s *scriptedKeyring) Get(service, account string) ([]byte, error) {
	s.calls++
	if len(s.results) == 0 {
		return []byte("v"), nil
	}
	err := s.results[0]
	s.results = s.results[1:]
	if err != nil {
		return nil, err
	}
	return []byte("v"), nil
}

func transient() error {
	return &ReadError{Service: "svc", Account: "acct", Reason: "timed out after 5s"}
}

func newTestResilient(inner Keyring) (*Resilient, *time.Time, *[]Event) {
	now := time.Date(2026, 10, 13, 14, 29, 43, 0, time.UTC)
	var events []Event
	r := NewResilient(inner, func(e Event) { events = append(events, e) })
	r.now = func() time.Time { return now }
	return r, &now, &events
}

func TestReadError_IsUnavailableNotNotFound(t *testing.T) {
	err := error(&ReadError{Service: "s", Account: "a", Reason: "exit status 36"})
	if !errors.Is(err, ErrUnavailable) {
		t.Error("ReadError must satisfy ErrUnavailable")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("ReadError must not look like not-found")
	}
}

func TestResilient_FailsFastBetweenRetries(t *testing.T) {
	inner := &scriptedKeyring{results: []error{transient()}}
	r, now, _ := newTestResilient(inner)

	if _, err := r.Get("svc", "acct"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first Get err = %v, want ErrUnavailable", err)
	}
	// Inside the 5s window the keychain must not be asked again.
	*now = now.Add(4 * time.Second)
	if _, err := r.Get("svc", "acct"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get inside window err = %v, want ErrUnavailable", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (fail fast inside the window)", inner.calls)
	}
	// Past the window it retries, and a success clears the state.
	*now = now.Add(2 * time.Second)
	if data, err := r.Get("svc", "acct"); err != nil || string(data) != "v" {
		t.Fatalf("Get after window = %q, %v", data, err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner calls = %d, want 2", inner.calls)
	}
}

func TestResilient_RetryScheduleIs5_10_15ThenEvery30(t *testing.T) {
	fails := make([]error, 10)
	for i := range fails {
		fails[i] = transient()
	}
	inner := &scriptedKeyring{results: fails}
	r, now, _ := newTestResilient(inner)

	var attempts []time.Duration
	start := *now
	for elapsed := time.Duration(0); elapsed <= 100*time.Second; elapsed += time.Second {
		*now = start.Add(elapsed)
		before := inner.calls
		_, _ = r.Get("svc", "acct")
		if inner.calls > before {
			attempts = append(attempts, elapsed)
		}
	}
	want := []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second, 90 * time.Second}
	if len(attempts) != len(want) {
		t.Fatalf("attempt times = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt times = %v, want %v", attempts, want)
		}
	}
}

func TestResilient_ReportsOnceThenRecovery(t *testing.T) {
	inner := &scriptedKeyring{results: []error{transient(), transient(), transient()}}
	r, now, events := newTestResilient(inner)

	for i := 0; i < 3; i++ {
		_, _ = r.Get("svc", "acct")
		*now = now.Add(20 * time.Second)
	}
	if _, err := r.Get("svc", "acct"); err != nil {
		t.Fatalf("recovery Get: %v", err)
	}
	if len(*events) != 2 {
		t.Fatalf("events = %+v, want one degraded + one recovered", *events)
	}
	if (*events)[0].Kind != EventDegraded || !errors.Is((*events)[0].Err, ErrUnavailable) {
		t.Errorf("first event = %+v, want degraded with the read error", (*events)[0])
	}
	rec := (*events)[1]
	if rec.Kind != EventRecovered || rec.Failures != 3 || rec.Down != 60*time.Second {
		t.Errorf("recovery event = %+v, want 3 failures over 60s", rec)
	}
}

// Not-found is an answer, not an outage: it passes through untouched and
// clears any failure state instead of being retried or reported.
func TestResilient_NotFoundPassesThroughAndClears(t *testing.T) {
	inner := &scriptedKeyring{results: []error{transient(), ErrNotFound, ErrNotFound}}
	r, now, events := newTestResilient(inner)

	_, _ = r.Get("svc", "acct")
	*now = now.Add(6 * time.Second)
	if _, err := r.Get("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// State cleared: the next call reaches the keychain immediately.
	if _, err := r.Get("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if inner.calls != 3 {
		t.Errorf("inner calls = %d, want 3", inner.calls)
	}
	if len(*events) != 2 || (*events)[1].Kind != EventRecovered {
		t.Errorf("events = %+v, want degraded then recovered", *events)
	}
}

func TestResilient_OtherErrorsAreNotThrottled(t *testing.T) {
	boom := errors.New("boom")
	inner := &scriptedKeyring{results: []error{boom, boom}}
	r, _, events := newTestResilient(inner)

	_, _ = r.Get("svc", "acct")
	if _, err := r.Get("svc", "acct"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the original error", err)
	}
	if inner.calls != 2 || len(*events) != 0 {
		t.Errorf("calls=%d events=%v, want 2 calls and no events", inner.calls, *events)
	}
}

func TestResilient_ItemsFailIndependently(t *testing.T) {
	inner := &scriptedKeyring{results: []error{transient()}}
	r, _, _ := newTestResilient(inner)

	_, _ = r.Get("svc", "a")
	if _, err := r.Get("svc", "b"); err != nil {
		t.Fatalf("a different item must not inherit a's backoff: %v", err)
	}
}

func TestFaultKeyring_FailsThenDelegates(t *testing.T) {
	inner := NewMemoryKeyring()
	_ = inner.Set("s", "a", []byte("x"))
	k := NewFaultKeyring(inner, time.Hour)
	if _, err := k.Get("s", "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable while the fault window is open", err)
	}
	k = NewFaultKeyring(inner, -time.Second)
	if data, err := k.Get("s", "a"); err != nil || string(data) != "x" {
		t.Fatalf("Get after window = %q, %v", data, err)
	}
}
