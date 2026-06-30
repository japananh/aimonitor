package provider

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// stubProvider is a minimal Provider used only to exercise the registry.
// Every method beyond Name() is a no-op; the registry never calls them.
type stubProvider struct{ name string }

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) LoadAccounts(context.Context) ([]Account, error) {
	return nil, nil
}
func (s stubProvider) EstimateSessionUsage(context.Context, Account) (Usage, error) {
	return Usage{}, nil
}
func (s stubProvider) ProbeServerSide(context.Context, Account) (RateLimit, error) {
	return RateLimit{}, nil
}
func (s stubProvider) ActiveCredential(context.Context) (Credential, error) {
	return Credential{}, nil
}
func (s stubProvider) SetActiveCredential(context.Context, Credential) error { return nil }
func (s stubProvider) OnboardingFlow(context.Context) (Credential, error) {
	return Credential{}, nil
}

func TestRegisterAndLookup(t *testing.T) {
	// Unique name so the process-global registry (no deregister exists)
	// doesn't collide with other tests' registrations.
	name := "stub-registerlookup"
	Register(stubProvider{name: name})

	got, err := Lookup(name)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", name, err)
	}
	if got.Name() != name {
		t.Errorf("Lookup returned provider %q, want %q", got.Name(), name)
	}
}

func TestLookup_Unknown(t *testing.T) {
	_, err := Lookup("definitely-not-registered-xyz")
	if err == nil {
		t.Fatal("Lookup of unregistered name: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error %q should mention 'not registered'", err.Error())
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	name := "stub-duplicate"
	Register(stubProvider{name: name})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("duplicate Register: want panic, got none")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "registered twice") {
			t.Errorf("panic message %q should mention 'registered twice'", msg)
		}
	}()
	Register(stubProvider{name: name}) // second registration must panic
}

func TestNames_SortedSubsequence(t *testing.T) {
	// Register two known names; Names() returns the whole global registry
	// sorted, so we only assert our two appear in the correct relative
	// (sorted) order — other tests' registrations pollute the slice.
	Register(stubProvider{name: "zz-names-b"})
	Register(stubProvider{name: "zz-names-a"})

	names := Names()

	var ia, ib = -1, -1
	for i, n := range names {
		switch n {
		case "zz-names-a":
			ia = i
		case "zz-names-b":
			ib = i
		}
	}
	if ia == -1 || ib == -1 {
		t.Fatalf("both registered names should appear in %v", names)
	}
	if ia > ib {
		t.Errorf("Names() not sorted: zz-names-a at %d after zz-names-b at %d", ia, ib)
	}
}

func TestCredential_Zero(t *testing.T) {
	c := Credential{Bytes: []byte("secret-token")}
	c.Zero()
	if c.Bytes != nil {
		t.Errorf("Zero should nil out Bytes, got %v", c.Bytes)
	}

	// Zeroing then nilling on an already-empty credential is safe.
	empty := Credential{}
	empty.Zero()
	if empty.Bytes != nil {
		t.Errorf("Zero on empty credential should leave nil Bytes")
	}

	// Confirm the underlying array is actually overwritten with zeros, not
	// just detached: capture the backing slice before Zero nils the field.
	backing := []byte("sensitive")
	c2 := Credential{Bytes: backing}
	c2.Zero()
	if !bytes.Equal(backing, make([]byte, len("sensitive"))) {
		t.Errorf("backing bytes not zeroed: %v", backing)
	}
}
