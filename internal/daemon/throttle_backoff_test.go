package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
)

// Regression tests for the 2026-09 throttle loop: with every account over
// threshold and no swap target, the scheduler polled the exhausted active
// account every ~60 s, got a 429, waited a flat 10 min, succeeded once (which
// reset the backoff) and went straight back to 60 s — for hours. Manual/UI
// refreshes ignored the cooldown entirely.

func TestThrottleBackoff_DoublesToCap(t *testing.T) {
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, w := range want {
		if got := throttleBackoff(i + 1); got != w {
			t.Errorf("throttleBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := throttleBackoff(1000); got != cooldownMax {
		t.Errorf("throttleBackoff(1000) = %v, want cap %v", got, cooldownMax)
	}
}

// A success between two 429s lifts the cooldown but must not reset the
// streak: each 429 without Retry-After parks the account longer, up to 1 h.
func TestRecordThrottle_GrowsAcrossSuccess(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	throttled := &claude.UsageThrottledError{Status: 429}

	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour} {
		until, ok := recordThrottleAt(ctx, s, acct, throttled, now)
		if !ok {
			t.Fatalf("429 #%d: no cooldown set", i+1)
		}
		if got := until.Sub(now); got != want {
			t.Errorf("429 #%d: cooldown %v, want %v", i+1, got, want)
		}
		clearThrottle(ctx, s, acct) // the success in between
		now = until.Add(time.Minute)
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	if got.ThrottleStrikes != 4 {
		t.Errorf("strikes = %d, want 4", got.ThrottleStrikes)
	}
}

// A 429 long after the previous one starts a new streak at the floor.
func TestRecordThrottle_StreakResetsAfterQuietPeriod(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := s.SetThrottle(ctx, acct.ID, now.Add(-3*time.Hour), throttleReason, 4, now.Add(-4*time.Hour)); err != nil {
		t.Fatal(err)
	}

	until, ok := recordThrottleAt(ctx, s, acct, &claude.UsageThrottledError{Status: 429}, now)
	if !ok {
		t.Fatal("no cooldown set")
	}
	if got := until.Sub(now); got != cooldownDefault {
		t.Errorf("cooldown %v, want a fresh-streak %v", got, cooldownDefault)
	}
}

// Retry-After still wins over the streak backoff, but the strike is counted.
func TestRecordThrottle_RetryAfterWinsButCountsStrike(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	acct, _ := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	_ = s.SetThrottle(ctx, acct.ID, now.Add(-time.Minute), throttleReason, 2, now.Add(-20*time.Minute))

	until, _ := recordThrottleAt(ctx, s, acct, &claude.UsageThrottledError{Status: 429, RetryAfter: 2 * time.Minute}, now)
	if got := until.Sub(now); got != 2*time.Minute {
		t.Errorf("cooldown %v, want Retry-After 2m", got)
	}
	got, _ := s.GetAccountByID(ctx, acct.ID)
	if got.ThrottleStrikes != 3 {
		t.Errorf("strikes = %d, want 3", got.ThrottleStrikes)
	}
}

func TestExhaustedUntil(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	in8m, in4d := now.Add(8*time.Minute), now.Add(94*time.Hour)
	cases := []struct {
		name string
		lim  provider.Limits
		want time.Time
	}{
		{"none exhausted", provider.Limits{FiveHourPct: 99, SevenDayPct: 98, FiveHourResetAt: in8m, SevenDayResetAt: in4d}, time.Time{}},
		{"5h exhausted", provider.Limits{FiveHourPct: 100, SevenDayPct: 98, FiveHourResetAt: in8m, SevenDayResetAt: in4d}, in8m},
		{"7d exhausted", provider.Limits{FiveHourPct: 10, SevenDayPct: 100, FiveHourResetAt: in8m, SevenDayResetAt: in4d}, in4d},
		{"both, later wins", provider.Limits{FiveHourPct: 100, SevenDayPct: 100, FiveHourResetAt: in8m, SevenDayResetAt: in4d}, in4d},
		{"reset passed", provider.Limits{FiveHourPct: 100, FiveHourResetAt: now.Add(-time.Minute)}, time.Time{}},
		{"reset unknown", provider.Limits{FiveHourPct: 100}, time.Time{}},
	}
	for _, c := range cases {
		if got := exhaustedUntil(c.lim, now); !got.Equal(c.want) {
			t.Errorf("%s: exhaustedUntil = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFetchGate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	newAcct := func(t *testing.T, s *store.Store, lim *provider.Limits) store.Account {
		t.Helper()
		a, err := s.CreateAccount(ctx, store.Account{Label: "a", KeyringRef: "ref-a"})
		if err != nil {
			t.Fatal(err)
		}
		if lim != nil {
			if err := s.PutLimits(ctx, a.ID, *lim); err != nil {
				t.Fatal(err)
			}
		}
		return a
	}

	t.Run("no data passes", func(t *testing.T) {
		s := openStore(t)
		a := newAcct(t, s, nil)
		if skip := fetchGate(ctx, s, a, now, minRefreshAge); skip != nil {
			t.Errorf("gate = %v, want nil", skip)
		}
	})
	t.Run("cooldown blocks, even with a stale acct row", func(t *testing.T) {
		s := openStore(t)
		a := newAcct(t, s, nil)
		// Written by another process after `a` was loaded.
		_ = s.SetCooldown(ctx, a.ID, now.Add(20*time.Minute), throttleReason)
		skip := fetchGate(ctx, s, a, now, 0)
		if skip == nil || skip.Reason != SkipRateLimited || !skip.Until.Equal(now.Add(20*time.Minute)) {
			t.Errorf("gate = %+v, want rate-limited until +20m", skip)
		}
	})
	t.Run("exhausted blocks until reset", func(t *testing.T) {
		s := openStore(t)
		reset := now.Add(8 * time.Minute)
		a := newAcct(t, s, &provider.Limits{FiveHourPct: 100, SevenDayPct: 98, FiveHourResetAt: reset, FetchedAt: now.Add(-time.Hour)})
		skip := fetchGate(ctx, s, a, now, 0)
		if skip == nil || skip.Reason != SkipAtLimit || !skip.Until.Equal(reset) {
			t.Fatalf("gate = %+v, want at-limit until reset", skip)
		}
		if !skip.HasCached || skip.Cached.FiveHourPct != 100 {
			t.Errorf("cached snapshot missing: %+v", skip)
		}
		if skip := fetchGate(ctx, s, a, reset.Add(time.Second), 0); skip != nil {
			t.Errorf("after reset gate = %v, want nil", skip)
		}
	})
	t.Run("fresh blocks only with minAge", func(t *testing.T) {
		s := openStore(t)
		a := newAcct(t, s, &provider.Limits{FiveHourPct: 50, FiveHourResetAt: now.Add(time.Hour), FetchedAt: now.Add(-time.Minute)})
		if skip := fetchGate(ctx, s, a, now, minRefreshAge); skip == nil || skip.Reason != SkipFresh {
			t.Errorf("gate = %+v, want fresh", skip)
		}
		if skip := fetchGate(ctx, s, a, now, 0); skip != nil {
			t.Errorf("minAge=0 gate = %v, want nil", skip)
		}
	})
	t.Run("reset-crossed snapshot is not fresh", func(t *testing.T) {
		s := openStore(t)
		a := newAcct(t, s, &provider.Limits{FiveHourPct: 40, FiveHourResetAt: now.Add(-10 * time.Second), FetchedAt: now.Add(-time.Minute)})
		if skip := fetchGate(ctx, s, a, now, minRefreshAge); skip != nil {
			t.Errorf("gate = %v, want nil", skip)
		}
	})
}

// failServer fails the test if anything reaches it.
func failServer(t *testing.T, what string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("%s must not be called", what)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The manual/UI path (`aimonitor usage refresh`) must respect the cooldown and
// a known-exhausted window: no token refresh, no usage call.
func TestRefreshAccountUsage_GatedMakesNoRequest(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	usage := failServer(t, "usage endpoint")
	refresh := failServer(t, "token endpoint")
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	st := openStore(t)
	cooling, _ := st.CreateAccount(ctx, store.Account{Label: "cooling", KeyringRef: "ref-c"})
	exhausted, _ := st.CreateAccount(ctx, store.Account{Label: "exhausted", KeyringRef: "ref-e"})
	// Expired tokens: an ungated call would hit the token endpoint first.
	for _, a := range []store.Account{cooling, exhausted} {
		blob := stashBlob("sk-old", "rtok", time.Now().Add(-time.Hour))
		if err := claude.StashCredential(ctx, a.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetCooldown(ctx, cooling.ID, time.Now().Add(10*time.Minute), throttleReason)
	_ = st.PutLimits(ctx, exhausted.ID, provider.Limits{
		FiveHourPct: 100, SevenDayPct: 98,
		FiveHourResetAt: time.Now().Add(8 * time.Minute), FetchedAt: time.Now().Add(-time.Hour),
	})

	for _, c := range []struct {
		acct store.Account
		want SkipReason
	}{{cooling, SkipRateLimited}, {exhausted, SkipAtLimit}} {
		_, err := RefreshAccountUsage(ctx, st, fetcher, refresher, c.acct)
		var skip *RefreshSkippedError
		if !errors.As(err, &skip) || skip.Reason != c.want {
			t.Errorf("%s: err = %v, want skipped (%s)", c.acct.Label, err, c.want)
		}
	}
}

// A 429 on the manual path parks the account, so the next click is refused
// locally instead of hitting Anthropic again (the screenshot's error loop).
func TestRefreshAccountUsage_429ParksAccount(t *testing.T) {
	restore := claude.SetKeyringForTest(secret.NewMemoryKeyring())
	defer restore()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	var hits atomic.Int32
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests) // no Retry-After
	}))
	defer usage.Close()
	refresh := failServer(t, "token endpoint")
	fetcher := &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()}
	refresher := &claude.TokenRefresher{HTTP: refresh.Client(), TokenURL: refresh.URL}

	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "BE 2", KeyringRef: "ref-b"})
	blob := stashBlob("sk-ok", "rtok", time.Now().Add(time.Hour))
	if err := claude.StashCredential(ctx, acct.KeyringRef, provider.Credential{Bytes: blob}); err != nil {
		t.Fatal(err)
	}

	if _, err := RefreshAccountUsage(ctx, st, fetcher, refresher, acct); !claude.IsThrottledError(err) {
		t.Fatalf("first refresh err = %v, want 429", err)
	}
	got, _ := st.GetAccountByID(ctx, acct.ID)
	if d := time.Until(got.CooldownUntil); d < cooldownDefault-time.Minute || d > cooldownDefault+time.Minute {
		t.Errorf("cooldown %v, want ~%v", d, cooldownDefault)
	}
	var skip *RefreshSkippedError
	if _, err := RefreshAccountUsage(ctx, st, fetcher, refresher, acct); !errors.As(err, &skip) {
		t.Errorf("second refresh err = %v, want skipped", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("usage endpoint hit %d times, want 1", n)
	}
}

// Scheduler, active account at a limit that hasn't reset: no token refresh, no
// usage call, but auto-swap still gets to look for a recovered target.
func TestUsageScheduler_Tick_AtLimitSkipsFetchButEvaluatesSwap(t *testing.T) {
	ctx := context.Background()
	usage := failServer(t, "usage endpoint")
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "BE 2", KeyringRef: "ref"})
	_ = st.PutLimits(ctx, acct.ID, provider.Limits{
		FiveHourPct: 10, SevenDayPct: 100,
		SevenDayResetAt: time.Now().Add(94 * time.Hour), FetchedAt: time.Now().Add(-time.Hour),
	})

	afterFetch := 0
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{active: provider.Credential{Bytes: append([]byte(nil), goodCred...)}},
		Fetcher:  &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
		RefreshActive: func(context.Context, store.Account, bool) (provider.Credential, error) {
			t.Error("token refresh must not run for an at-limit account")
			return provider.Credential{}, nil
		},
		AfterFetch: func(context.Context, string) { afterFetch++ },
	}
	err := u.tickOnce(ctx)
	var skip *RefreshSkippedError
	if !errors.As(err, &skip) || skip.Reason != SkipAtLimit {
		t.Fatalf("tickOnce err = %v, want at-limit skip", err)
	}
	if afterFetch != 1 {
		t.Errorf("AfterFetch called %d times, want 1", afterFetch)
	}
}

// Scheduler, active account parked by another path (e.g. a CLI refresh 429):
// no request until the cooldown ends.
func TestUsageScheduler_Tick_RespectsCooldown(t *testing.T) {
	ctx := context.Background()
	usage := failServer(t, "usage endpoint")
	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "p", KeyringRef: "ref"})
	_ = st.SetCooldown(ctx, acct.ID, time.Now().Add(10*time.Minute), throttleReason)

	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{active: provider.Credential{Bytes: append([]byte(nil), goodCred...)}},
		Fetcher:  &claude.UsageFetcher{BaseURL: usage.URL, HTTP: usage.Client()},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
		AfterFetch: func(context.Context, string) { t.Error("AfterFetch must not run on a cooldown skip") },
	}
	var skip *RefreshSkippedError
	if err := u.tickOnce(ctx); !errors.As(err, &skip) || skip.Reason != SkipRateLimited {
		t.Fatalf("tickOnce err = %v, want rate-limited skip", err)
	}
}

// Scheduler 429 without Retry-After parks the active account and escalates
// across an intervening success (429 → ok → 429 must wait longer, not reset).
func TestUsageScheduler_Tick_429EscalatesAcrossSuccess(t *testing.T) {
	ctx := context.Background()
	var status atomic.Int32
	status.Store(http.StatusTooManyRequests)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s := int(status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			return
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":95.0},"seven_day":{"utilization":50.0}}`))
	}))
	defer srv.Close()

	st := openStore(t)
	acct, _ := st.CreateAccount(ctx, store.Account{Label: "p", KeyringRef: "ref"})
	u := &UsageScheduler{
		Store:    st,
		Provider: &fakeProvider{active: provider.Credential{Bytes: append([]byte(nil), goodCred...)}},
		Fetcher:  &claude.UsageFetcher{BaseURL: srv.URL, HTTP: srv.Client()},
		ResolveActive: func(context.Context) (store.Account, bool, error) {
			return acct, true, nil
		},
	}
	u.defaults()

	parkedFor := func() time.Duration {
		t.Helper()
		err := u.tickOnce(ctx)
		var parked *parkedError
		if !errors.As(err, &parked) || !claude.IsThrottledError(err) {
			t.Fatalf("tickOnce err = %v, want parked 429", err)
		}
		return time.Until(parked.until)
	}

	first := parkedFor()
	if first < cooldownDefault-time.Minute || first > cooldownDefault {
		t.Errorf("first 429 parked for %v, want ~%v", first, cooldownDefault)
	}

	// Cooldown over, one success, then another 429.
	_ = st.ClearCooldown(ctx, acct.ID)
	status.Store(http.StatusOK)
	if err := u.tickOnce(ctx); err != nil {
		t.Fatalf("success tick: %v", err)
	}
	status.Store(http.StatusTooManyRequests)
	if second := parkedFor(); second < 2*cooldownDefault-time.Minute {
		t.Errorf("second 429 parked for %v, want ~%v (escalated)", second, 2*cooldownDefault)
	}
}

func TestUsageScheduler_WaitIntervals(t *testing.T) {
	u := &UsageScheduler{}
	u.defaults() // Baseline 5m, Jitter 30s
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// Cooldown: never before it ends (jitter can only push later), never
	// faster than Baseline.
	if got := u.untilInterval(now.Add(40*time.Minute), now); got != 40*time.Minute+u.Jitter {
		t.Errorf("untilInterval(40m) = %v", got)
	}
	if got := u.untilInterval(now.Add(time.Minute), now); got != u.Baseline {
		t.Errorf("untilInterval(1m) = %v, want Baseline", got)
	}
	// At limit: re-evaluate at Baseline, or right after a sooner reset.
	if got := u.atLimitInterval(now.Add(94*time.Hour), now); got != u.Baseline {
		t.Errorf("atLimitInterval(94h) = %v, want Baseline", got)
	}
	if got := u.atLimitInterval(now.Add(2*time.Minute), now); got != 2*time.Minute+u.Jitter {
		t.Errorf("atLimitInterval(2m) = %v", got)
	}
}

// No target → Stuck; a target reappearing clears it.
func TestAutoSwap_StuckTracksNoCandidate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "r0"})
	other, _ := s.CreateAccount(ctx, store.Account{Label: "other", KeyringRef: "r1"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 100, FiveHourResetAt: time.Now().Add(time.Hour)})
	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 100, FiveHourResetAt: time.Now().Add(time.Hour)})
	immediateSwap(t, s)
	a, fsw, _ := withAutoSwapStubs(t, s)

	if _, err := a.MaybeSwap(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	if !a.Stuck() {
		t.Fatal("Stuck() = false with no candidate")
	}

	_ = s.PutLimits(ctx, other.ID, provider.Limits{FiveHourPct: 5, FiveHourResetAt: time.Now().Add(time.Hour)})
	if _, err := a.MaybeSwap(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	if a.Stuck() {
		t.Error("Stuck() = true after a candidate appeared")
	}
	if len(fsw.switched) != 1 || fsw.switched[0] != "other" {
		t.Errorf("switched = %v, want [other]", fsw.switched)
	}
}

// A stale candidate known to be at its limit until a future reset is neither
// re-fetched by the JIT refresh nor promoted to the uncertain tier.
func TestAutoSwap_StaleExhaustedCandidateNotRefreshedOrPicked(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	active, _ := s.CreateAccount(ctx, store.Account{Label: "active", KeyringRef: "r0"})
	capped, _ := s.CreateAccount(ctx, store.Account{Label: "capped", KeyringRef: "r1"})
	_ = s.PutLimits(ctx, active.ID, provider.Limits{FiveHourPct: 100, FiveHourResetAt: now.Add(time.Hour), FetchedAt: now})
	_ = s.PutLimits(ctx, capped.ID, provider.Limits{
		SevenDayPct: 100, SevenDayResetAt: now.Add(72 * time.Hour), FetchedAt: now.Add(-2 * time.Hour),
	})
	immediateSwap(t, s)
	a, fsw, _ := withAutoSwapStubs(t, s)
	a.Now = func() time.Time { return now }
	a.RefreshUsage = func(_ context.Context, acct store.Account) (provider.Limits, error) {
		t.Errorf("RefreshUsage called for %q", acct.Label)
		return provider.Limits{}, nil
	}

	if _, err := a.MaybeSwap(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	if len(fsw.switched) != 0 {
		t.Errorf("switched to %v, want no swap", fsw.switched)
	}
	if !a.Stuck() {
		t.Error("Stuck() = false, want true")
	}
}
