package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// CredentialTokens is the parsed view of a Claude Code credential blob.
// All fields are best-effort: Anthropic's blob shape has evolved across
// CLI versions, so callers must tolerate empty values. Zero-value
// ExpiresAt means the blob did not record an explicit expiry.
type CredentialTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

// claudeCodeCredential mirrors the structural part of the JSON shape
// Claude Code writes to its keychain slot. Used only for the typed
// extraction; round-trip rewrites use a generic map so unknown future
// fields are preserved.
type claudeCodeCredential struct {
	ClaudeAiOauth struct {
		AccessToken  string   `json:"accessToken"`
		RefreshToken string   `json:"refreshToken"`
		ExpiresAt    int64    `json:"expiresAt"` // unix millis
		Scopes       []string `json:"scopes,omitempty"`
	} `json:"claudeAiOauth"`
}

// ParseCredential extracts the access + refresh tokens (and expiry, if
// recorded) from a Claude Code credential blob. Returns an error only on
// genuinely unparseable input — missing fields are not errors, just
// zero values in the result.
func ParseCredential(cred provider.Credential) (CredentialTokens, error) {
	if len(cred.Bytes) == 0 {
		return CredentialTokens{}, errors.New("ParseCredential: empty credential")
	}
	var c claudeCodeCredential
	if err := json.Unmarshal(cred.Bytes, &c); err != nil {
		return CredentialTokens{}, fmt.Errorf("ParseCredential: %w", err)
	}
	t := CredentialTokens{
		AccessToken:  c.ClaudeAiOauth.AccessToken,
		RefreshToken: c.ClaudeAiOauth.RefreshToken,
		Scopes:       c.ClaudeAiOauth.Scopes,
	}
	if c.ClaudeAiOauth.ExpiresAt > 0 {
		t.ExpiresAt = time.UnixMilli(c.ClaudeAiOauth.ExpiresAt)
	}
	return t, nil
}

// extractAccessToken is a narrow helper for the legacy probe path. New
// code should use ParseCredential. Returns an error when the access
// token is empty — probes have no fallback if the token is missing.
func extractAccessToken(cred provider.Credential) (string, error) {
	t, err := ParseCredential(cred)
	if err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", errors.New("extractAccessToken: credential has no claudeAiOauth.accessToken field")
	}
	return t.AccessToken, nil
}

// ReplaceTokens returns a new credential blob with the access/refresh
// tokens and expiry replaced by the values in fresh. All other fields
// in the original blob (subscription type, future fields we don't know
// about yet) are preserved by round-tripping through a generic map.
//
// Used after a successful TokenRefresher.Refresh to write the rotated
// tokens back to keychain without losing any per-account metadata.
func ReplaceTokens(orig provider.Credential, fresh CredentialTokens) (provider.Credential, error) {
	if len(orig.Bytes) == 0 {
		// Fresh-install case: construct a minimal blob.
		return constructBlob(fresh)
	}
	var doc map[string]any
	if err := json.Unmarshal(orig.Bytes, &doc); err != nil {
		return provider.Credential{}, fmt.Errorf("ReplaceTokens: parse: %w", err)
	}
	oauth, _ := doc["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		oauth = map[string]any{}
		doc["claudeAiOauth"] = oauth
	}
	oauth["accessToken"] = fresh.AccessToken
	oauth["refreshToken"] = fresh.RefreshToken
	if !fresh.ExpiresAt.IsZero() {
		oauth["expiresAt"] = fresh.ExpiresAt.UnixMilli()
	}
	if len(fresh.Scopes) > 0 {
		oauth["scopes"] = fresh.Scopes
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("ReplaceTokens: marshal: %w", err)
	}
	return provider.Credential{Bytes: out}, nil
}

func constructBlob(t CredentialTokens) (provider.Credential, error) {
	doc := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  t.AccessToken,
			"refreshToken": t.RefreshToken,
		},
	}
	if !t.ExpiresAt.IsZero() {
		doc["claudeAiOauth"].(map[string]any)["expiresAt"] = t.ExpiresAt.UnixMilli()
	}
	if len(t.Scopes) > 0 {
		doc["claudeAiOauth"].(map[string]any)["scopes"] = t.Scopes
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return provider.Credential{}, fmt.Errorf("constructBlob: %w", err)
	}
	return provider.Credential{Bytes: out}, nil
}
