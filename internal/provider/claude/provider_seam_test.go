package claude

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
)

// These tests drive the Provider methods and package-level helpers through
// the SetKeyringForTest seam, which routes every keychain operation at an
// in-memory keyring. Hermetic: no real OS keychain, no network, no exec.
//
// NOTE: must NOT use t.Parallel — SetKeyringForTest mutates the package
// global opsOverride. Each test defers the returned restore function so the
// global is reset (and so sharedOpsOnce never fires newKeychainOps, which
// would build the real keyring).

func TestProvider_ActiveCredentialRoundTrip(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	p := New()

	// Empty slot → empty credential, no error.
	got, err := p.ActiveCredential(ctx)
	if err != nil {
		t.Fatalf("ActiveCredential (empty): %v", err)
	}
	if len(got.Bytes) != 0 {
		t.Fatalf("ActiveCredential (empty): got %d bytes, want 0", len(got.Bytes))
	}

	// SetActiveCredential then read back.
	blob := []byte(`{"claudeAiOauth":{"accessToken":"sk-active"}}`)
	if err := p.SetActiveCredential(ctx, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("SetActiveCredential: %v", err)
	}
	got, err = p.ActiveCredential(ctx)
	if err != nil {
		t.Fatalf("ActiveCredential: %v", err)
	}
	if string(got.Bytes) != string(blob) {
		t.Errorf("ActiveCredential = %q, want %q", got.Bytes, blob)
	}

	// Second read exercises the cache-hit branch in readActive.
	got2, err := p.ActiveCredential(ctx)
	if err != nil {
		t.Fatalf("ActiveCredential (cached): %v", err)
	}
	if string(got2.Bytes) != string(blob) {
		t.Errorf("cached ActiveCredential = %q, want %q", got2.Bytes, blob)
	}
}

func TestProvider_SeedLiveSlotViaConstant(t *testing.T) {
	// A test in any package can seed the live slot directly via the exported
	// KeychainUserForTest constant, then read it back through the Provider.
	ring := secret.NewMemoryKeyring()
	restore := SetKeyringForTest(ring)
	defer restore()

	seed := []byte(`{"claudeAiOauth":{"accessToken":"sk-seeded"}}`)
	if err := ring.Set(ClaudeCodeService, KeychainUserForTest, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := New().ActiveCredential(context.Background())
	if err != nil {
		t.Fatalf("ActiveCredential: %v", err)
	}
	if string(got.Bytes) != string(seed) {
		t.Errorf("ActiveCredential = %q, want seeded %q", got.Bytes, seed)
	}
}

func TestProvider_Stubs(t *testing.T) {
	p := New()
	ctx := context.Background()

	if _, err := p.LoadAccounts(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("LoadAccounts: want ErrNotImplemented, got %v", err)
	}
	if _, err := p.EstimateSessionUsage(ctx, provider.Account{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("EstimateSessionUsage: want ErrNotImplemented, got %v", err)
	}
}

func TestProvider_ProbeServerSide_StashNotFound(t *testing.T) {
	// Error path only: with an empty ring, RetrieveStash returns before any
	// network call. The Probe success line needs network and is left
	// uncovered (un-hermetic ceiling).
	ring := secret.NewMemoryKeyring()
	restore := SetKeyringForTest(ring)
	defer restore()

	_, err := New().ProbeServerSide(context.Background(), provider.Account{
		ID:         7,
		Label:      "missing",
		KeyringRef: "no-such-ref",
	})
	if err == nil {
		t.Fatal("ProbeServerSide with no stashed credential: want error")
	}
	if !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("error should wrap secret.ErrNotFound (read stash failed), got %v", err)
	}
}

func TestPackageHelpers_StashRoundTrip(t *testing.T) {
	// Exercises StashCredential / RetrieveStash / DeleteStash through the
	// shared (overridden) ops singleton.
	ring := secret.NewMemoryKeyring()
	restore := SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()
	ref := "acct-uuid-1"
	blob := []byte(`{"claudeAiOauth":{"accessToken":"sk-stash"}}`)

	// RetrieveStash on a missing ref → ErrNotFound.
	if _, err := RetrieveStash(ctx, ref); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("RetrieveStash missing: want ErrNotFound, got %v", err)
	}

	if err := StashCredential(ctx, ref, provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("StashCredential: %v", err)
	}
	got, err := RetrieveStash(ctx, ref)
	if err != nil {
		t.Fatalf("RetrieveStash: %v", err)
	}
	if string(got.Bytes) != string(blob) {
		t.Errorf("RetrieveStash = %q, want %q", got.Bytes, blob)
	}

	if err := DeleteStash(ctx, ref); err != nil {
		t.Fatalf("DeleteStash: %v", err)
	}
	// DeleteStash is idempotent on already-missing.
	if err := DeleteStash(ctx, ref); err != nil {
		t.Fatalf("DeleteStash idempotent: %v", err)
	}
	if _, err := RetrieveStash(ctx, ref); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("RetrieveStash after delete: want ErrNotFound, got %v", err)
	}
}

func TestReadActiveFresh_BypassesCache(t *testing.T) {
	ring := secret.NewMemoryKeyring()
	restore := SetKeyringForTest(ring)
	defer restore()

	ctx := context.Background()

	// Prime the cache via the Provider.
	first := []byte(`{"claudeAiOauth":{"accessToken":"sk-first"}}`)
	if err := New().SetActiveCredential(ctx, provider.Credential{Bytes: first}); err != nil {
		t.Fatalf("SetActiveCredential: %v", err)
	}

	// Write a new value directly into the ring, bypassing aimonitor's cache
	// (simulating Claude Code itself rotating the token).
	second := []byte(`{"claudeAiOauth":{"accessToken":"sk-second"}}`)
	if err := ring.Set(ClaudeCodeService, KeychainUserForTest, second); err != nil {
		t.Fatalf("ring.Set: %v", err)
	}

	// ReadActiveFresh must invalidate the cache and return the new value.
	got, err := ReadActiveFresh(ctx)
	if err != nil {
		t.Fatalf("ReadActiveFresh: %v", err)
	}
	if string(got.Bytes) != string(second) {
		t.Errorf("ReadActiveFresh = %q, want fresh %q", got.Bytes, second)
	}
}

func TestSetKeyringForTest_RestoresPrevious(t *testing.T) {
	// Nested SetKeyringForTest calls must restore the prior override, not
	// leave a leaked global that pollutes later tests.
	if opsOverride != nil {
		t.Fatalf("precondition: opsOverride should be nil between tests, got %v", opsOverride)
	}
	restore := SetKeyringForTest(secret.NewMemoryKeyring())
	if opsOverride == nil {
		t.Fatal("SetKeyringForTest should install an override")
	}
	restore()
	if opsOverride != nil {
		t.Error("restore should reset opsOverride to nil")
	}
}

// --- credcache white-box: expiry + cloneBytes(nil) ---

func TestCredCache_ExpiryPrune(t *testing.T) {
	// Construct credCache directly and override the now field (white-box, no
	// production edit) to advance past the TTL and assert the entry is
	// pruned on access.
	cur := time.Unix(0, 0)
	c := &credCache{
		items: map[string]credCacheEntry{},
		ttl:   5 * time.Second,
		now:   func() time.Time { return cur },
	}
	key := cacheKey("svc", "acct")
	c.put(key, []byte("data"))

	// Still within TTL: present.
	if _, ok := c.get(key); !ok {
		t.Fatal("entry should be present within TTL")
	}

	// Advance past TTL: get must prune and miss.
	cur = cur.Add(6 * time.Second)
	if _, ok := c.get(key); ok {
		t.Fatal("entry should be expired and pruned after TTL")
	}
	// Confirm it was actually removed from the map.
	if _, present := c.items[key]; present {
		t.Error("expired entry should be deleted from the cache map")
	}
}

func TestCloneBytes_Nil(t *testing.T) {
	if got := cloneBytes(nil); got != nil {
		t.Errorf("cloneBytes(nil) = %v, want nil", got)
	}
	in := []byte("abc")
	out := cloneBytes(in)
	if string(out) != "abc" {
		t.Errorf("cloneBytes mismatch: %q", out)
	}
	out[0] = 'X'
	if string(in) != "abc" {
		t.Error("cloneBytes should return an independent copy")
	}
}

// --- keychain.go ring-error branches (hermetic via an error-injecting fake) ---

var errRing = errors.New("simulated keyring failure")

// realErrKeyring implements secret.Keyring and returns errRing from every
// operation, driving the ring-error wrap branches in keychainOps without any
// real keychain.
type realErrKeyring struct{}

func (realErrKeyring) Get(string, string) ([]byte, error) { return nil, errRing }
func (realErrKeyring) Set(string, string, []byte) error   { return errRing }
func (realErrKeyring) Delete(string, string) error        { return errRing }

func newErrOps() *keychainOps {
	return &keychainOps{ring: realErrKeyring{}, user: "tester", cache: newCredCache(credCacheTTL)}
}

func TestKeychainOps_RingErrors(t *testing.T) {
	ops := newErrOps()
	ctx := context.Background()

	if _, err := ops.readActive(ctx); err == nil {
		t.Error("readActive ring error: want error")
	}
	if err := ops.writeActive(ctx, provider.Credential{Bytes: []byte("x")}); err == nil {
		t.Error("writeActive ring error: want error")
	}
	if _, err := ops.readStash(ctx, "ref"); err == nil {
		t.Error("readStash ring error: want error")
	}
	if err := ops.writeStash(ctx, "ref", provider.Credential{Bytes: []byte("x")}); err == nil {
		t.Error("writeStash ring error: want error")
	}
	// deleteStash returns the ring error when it isn't ErrNotFound.
	if err := ops.deleteStash(ctx, "ref"); err == nil {
		t.Error("deleteStash ring error: want error")
	}
}

func TestKeychainOps_ReadStashFromRing(t *testing.T) {
	// Seed the ring directly (bypassing writeStash, so the cache is empty),
	// then readStash: this exercises the cache-miss → ring.Get → cache.put
	// path that the round-trip tests skip (writeStash pre-populates cache).
	ops, fr := newTestKeychainOps(t)
	ctx := context.Background()
	blob := []byte(`{"claudeAiOauth":{"accessToken":"sk-ring"}}`)
	service := AimonitorServicePrefix + "ref-ring"
	if err := fr.Set(service, ops.user, blob); err != nil {
		t.Fatalf("seed ring: %v", err)
	}
	got, err := ops.readStash(ctx, "ref-ring")
	if err != nil {
		t.Fatalf("readStash from ring: %v", err)
	}
	if string(got.Bytes) != string(blob) {
		t.Errorf("readStash = %q, want %q", got.Bytes, blob)
	}
}

func TestKeychainOps_WriteStashEmptyBytes(t *testing.T) {
	ops, _ := newTestKeychainOps(t)
	if err := ops.writeStash(context.Background(), "ref", provider.Credential{}); err == nil {
		t.Error("writeStash empty bytes: want error")
	}
}

func TestKeychainOps_ReadStashCacheHit(t *testing.T) {
	// Second readStash for the same ref must be served from the cache,
	// covering the cache-hit branch in readStash.
	ops, _ := newTestKeychainOps(t)
	ctx := context.Background()
	blob := []byte(`{"claudeAiOauth":{"accessToken":"sk-c"}}`)
	if err := ops.writeStash(ctx, "ref-cache", provider.Credential{Bytes: blob}); err != nil {
		t.Fatalf("writeStash: %v", err)
	}
	// First read populates (writeStash already cached it); read twice.
	if _, err := ops.readStash(ctx, "ref-cache"); err != nil {
		t.Fatalf("readStash 1: %v", err)
	}
	got, err := ops.readStash(ctx, "ref-cache")
	if err != nil {
		t.Fatalf("readStash 2 (cache hit): %v", err)
	}
	if string(got.Bytes) != string(blob) {
		t.Errorf("cached readStash = %q, want %q", got.Bytes, blob)
	}
}
