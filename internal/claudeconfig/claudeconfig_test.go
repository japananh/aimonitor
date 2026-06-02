package claudeconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	return NewAt(path), path
}

func TestWriteOAuthAccount_PreservesSiblingFields(t *testing.T) {
	s, path := tempStore(t)
	ctx := context.Background()

	// Seed a realistic claude.json with unrelated top-level fields plus an
	// old oauthAccount identity.
	seed := map[string]any{
		"numStartups":  42,
		"userID":       "user-abc",
		"projects":     map[string]any{"/Users/x/proj": map[string]any{"allowedTools": []any{}}},
		"oauthAccount": map[string]any{"emailAddress": "old@example.com", "accountUuid": "OLD-UUID"},
	}
	writeJSON(t, path, seed)

	// Patch to a new identity.
	if err := s.WriteOAuthAccount(ctx, OAuthAccount{
		EmailAddress:     "new@example.com",
		OrganizationName: "New Org",
		OrganizationUUID: "ORG-NEW",
	}); err != nil {
		t.Fatalf("WriteOAuthAccount: %v", err)
	}

	got := readJSON(t, path)

	// Sibling fields survive.
	if got["numStartups"].(float64) != 42 {
		t.Errorf("numStartups not preserved: %v", got["numStartups"])
	}
	if got["userID"] != "user-abc" {
		t.Errorf("userID not preserved: %v", got["userID"])
	}
	if _, ok := got["projects"]; !ok {
		t.Errorf("projects key dropped")
	}

	// oauthAccount fully replaced — new identity, and stale accountUuid gone.
	oa := got["oauthAccount"].(map[string]any)
	if oa["emailAddress"] != "new@example.com" {
		t.Errorf("emailAddress = %v, want new@example.com", oa["emailAddress"])
	}
	if oa["organizationUuid"] != "ORG-NEW" {
		t.Errorf("organizationUuid = %v, want ORG-NEW", oa["organizationUuid"])
	}
	if _, present := oa["accountUuid"]; present {
		t.Errorf("stale accountUuid should be dropped on patch, got %v", oa["accountUuid"])
	}
}

func TestReadOAuthAccount_RoundTrip(t *testing.T) {
	s, _ := tempStore(t)
	ctx := context.Background()

	want := OAuthAccount{EmailAddress: "rt@example.com", OrganizationName: "RT", OrganizationUUID: "ORG-RT"}
	if err := s.WriteOAuthAccount(ctx, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := s.ReadOAuthAccount(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || *got != want {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestReadOAuthAccount_MissingFile(t *testing.T) {
	s, _ := tempStore(t)
	got, err := s.ReadOAuthAccount(context.Background())
	if err != nil {
		t.Fatalf("read missing: %v", err)
	}
	if got != nil {
		t.Errorf("missing file should yield nil oauthAccount, got %+v", got)
	}
}

func TestReadOAuthAccount_NoOAuthKey(t *testing.T) {
	s, path := tempStore(t)
	writeJSON(t, path, map[string]any{"numStartups": 1})
	got, err := s.ReadOAuthAccount(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != nil {
		t.Errorf("file without oauthAccount should yield nil, got %+v", got)
	}
}

func TestWriteOAuthAccount_CreatesMissingFile(t *testing.T) {
	s, path := tempStore(t)
	ctx := context.Background()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: file should not exist")
	}
	if err := s.WriteOAuthAccount(ctx, OAuthAccount{EmailAddress: "fresh@example.com"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readJSON(t, path)
	oa := got["oauthAccount"].(map[string]any)
	if oa["emailAddress"] != "fresh@example.com" {
		t.Errorf("created file has wrong email: %v", oa["emailAddress"])
	}
}

func TestWriteOAuthAccount_FilePerms(t *testing.T) {
	s, path := tempStore(t)
	if err := s.WriteOAuthAccount(context.Background(), OAuthAccount{EmailAddress: "p@example.com"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 600", perm)
	}
}

func TestReadRaw_MalformedErrorsAfterRetry(t *testing.T) {
	s, path := tempStore(t)
	// Write genuinely invalid JSON that will never parse. The bounded
	// retry should exhaust and surface an error rather than hang.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReadOAuthAccount(context.Background())
	if err == nil {
		t.Errorf("expected parse error on malformed file")
	}
}

func TestReadRaw_ContextCancel(t *testing.T) {
	s, path := tempStore(t)
	if err := os.WriteFile(path, []byte("{still bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call
	_, err := s.ReadOAuthAccount(ctx)
	if err == nil {
		t.Errorf("expected error from cancelled context during retry")
	}
}

// --- helpers ---

func writeJSON(t *testing.T, path string, v map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	return m
}
