package claude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// Covers the ReplaceTokens / constructBlob branches that token_refresh_test.go
// and probe_test.go leave uncovered: empty/malformed inputs, the nil
// claudeAiOauth sub-object case, and constructBlob with expiry + scopes set.
// Pure functions — fully hermetic. (Names are suffixed _Branches to avoid
// colliding with the existing credential tests.)

func TestParseCredential_EmptyBranches(t *testing.T) {
	if _, err := ParseCredential(provider.Credential{}); err == nil {
		t.Error("ParseCredential(empty): want error")
	}
	if _, err := ParseCredential(provider.Credential{Bytes: []byte("{not json")}); err == nil {
		t.Error("ParseCredential(malformed): want error")
	}
}

func TestExtractAccessToken_MissingBranches(t *testing.T) {
	// Empty credential → ParseCredential error path.
	if _, err := extractAccessToken(provider.Credential{}); err == nil {
		t.Error("extractAccessToken(empty): want error")
	}
	// Valid blob but no accessToken → the dedicated empty-token error.
	if _, err := extractAccessToken(provider.Credential{
		Bytes: []byte(`{"claudeAiOauth":{"refreshToken":"rt-only"}}`),
	}); err == nil {
		t.Error("extractAccessToken(no accessToken): want error")
	}
}

func TestReplaceTokens_MalformedOrig(t *testing.T) {
	_, err := ReplaceTokens(provider.Credential{Bytes: []byte("{not json")}, CredentialTokens{})
	if err == nil {
		t.Error("ReplaceTokens(malformed orig): want parse error")
	}
}

func TestReplaceTokens_NilOauthSubObject(t *testing.T) {
	// Valid JSON with no claudeAiOauth object: ReplaceTokens must create one
	// and still preserve the unrelated sibling field. This is the
	// `oauth == nil` branch.
	orig := provider.Credential{Bytes: []byte(`{"otherStuff":1}`)}
	out, err := ReplaceTokens(orig, CredentialTokens{AccessToken: "a", RefreshToken: "r"})
	if err != nil {
		t.Fatalf("ReplaceTokens: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["otherStuff"] == nil {
		t.Errorf("sibling field dropped: %v", doc)
	}
	oauth := doc["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "a" {
		t.Errorf("created oauth object missing accessToken: %v", oauth)
	}
}

func TestReplaceTokens_WithScopes(t *testing.T) {
	// Scopes present → the `len(fresh.Scopes) > 0` branch in ReplaceTokens.
	orig := provider.Credential{Bytes: []byte(`{"claudeAiOauth":{"accessToken":"old"}}`)}
	out, err := ReplaceTokens(orig, CredentialTokens{
		AccessToken: "new",
		Scopes:      []string{"user:inference", "user:profile"},
	})
	if err != nil {
		t.Fatalf("ReplaceTokens: %v", err)
	}
	oauth := decodeOauthBranches(t, out.Bytes)
	scopes, ok := oauth["scopes"].([]any)
	if !ok || len(scopes) != 2 {
		t.Errorf("scopes not written: %v", oauth["scopes"])
	}
}

func TestConstructBlob_WithExpiryAndScopes(t *testing.T) {
	// Drives constructBlob's expiry + scopes branches (the minimal path is
	// already covered via ReplaceTokens_EmptyOriginal in token_refresh_test).
	exp := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	out, err := constructBlob(CredentialTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		ExpiresAt:    exp,
		Scopes:       []string{"user:inference"},
	})
	if err != nil {
		t.Fatalf("constructBlob: %v", err)
	}
	oauth := decodeOauthBranches(t, out.Bytes)
	if int64(oauth["expiresAt"].(float64)) != exp.UnixMilli() {
		t.Errorf("expiresAt = %v, want %d", oauth["expiresAt"], exp.UnixMilli())
	}
	if scopes, _ := oauth["scopes"].([]any); len(scopes) != 1 {
		t.Errorf("scopes = %v", oauth["scopes"])
	}
}

func decodeOauthBranches(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("decode blob: %v", err)
	}
	oauth, ok := doc["claudeAiOauth"].(map[string]any)
	if !ok {
		t.Fatalf("blob has no claudeAiOauth object: %s", b)
	}
	return oauth
}
