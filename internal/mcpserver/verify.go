package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/japananh/aimonitor/internal/version"
)

// Default API roots. Vars (not consts) so tests point them at httptest
// servers; the production values never change at runtime.
var (
	slackAPIBase     = "https://slack.com/api"
	clickupAPIBase   = "https://api.clickup.com/api/v2"
	clickupV3APIBase = "https://api.clickup.com/api/v3"
)

// verifyHTTP is the client for verification calls. Short timeout: these
// are interactive (`aimonitor mcp connect`) and a hung connect is worse
// than a retry.
var verifyHTTP = &http.Client{Timeout: 15 * time.Second}

func userAgent() string {
	return "aimonitor/" + version.Version + " (+https://github.com/japananh/aimonitor)"
}

// VerifySlack checks a Slack USER token (xoxp-/xoxe-) via auth.test and
// confirms it can use search.messages (the reason a user token is required
// — bot tokens can't search). Returns a human identity ("user @ team").
func VerifySlack(ctx context.Context, token string) (string, error) {
	if strings.HasPrefix(token, "xoxb-") {
		return "", fmt.Errorf("this is a bot token (xoxb-); a user token (xoxp-) is required — bot tokens cannot search messages")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, slackAPIBase+"/auth.test", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent())
	resp, err := verifyHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("slack auth.test: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		User  string `json:"user"`
		Team  string `json:"team"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("slack auth.test: decode: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("slack auth.test: %s", out.Error)
	}
	return fmt.Sprintf("%s @ %s", out.User, out.Team), nil
}

// VerifyClickUp checks a ClickUp personal token (pk_…) via GET /user.
// ClickUp wants the raw token in Authorization (no Bearer). Returns the
// account's username/email as the identity.
func VerifyClickUp(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clickupAPIBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := verifyHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("clickup /user: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("clickup rejected the token (401) — check it starts with pk_ and is current")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("clickup /user: HTTP %d", resp.StatusCode)
	}
	var out struct {
		User struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("clickup /user: decode: %w", err)
	}
	ident := out.User.Username
	if out.User.Email != "" {
		ident = fmt.Sprintf("%s (%s)", out.User.Username, out.User.Email)
	}
	return ident, nil
}

// Verifier returns the verification function for svc.
func Verifier(svc Service) func(ctx context.Context, token string) (string, error) {
	if svc == ServiceClickUp {
		return VerifyClickUp
	}
	return VerifySlack
}
