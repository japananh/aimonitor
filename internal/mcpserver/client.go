package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client carries shared state for tool handlers: the HTTP client and the
// credential store. Tokens are resolved from the keyring PER CALL (not
// captured at serve start) so a reconnect takes effect without restarting
// the Claude session; the OS keyring read is a few ms.
type Client struct {
	HTTP  *http.Client
	Creds *CredStore
}

// NewClient builds the production client. 30s timeout: large ClickUp
// responses (docs, big task lists) need more than claude-bar's 20s.
func NewClient(creds *CredStore) *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 30 * time.Second},
		Creds: creds,
	}
}

func (c *Client) token(svc Service) (string, error) {
	tok, err := c.Creds.Token(svc)
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", fmt.Errorf("%s is not connected — run `aimonitor mcp connect %s`", svc, svc)
	}
	return tok, nil
}

// do runs one HTTP request and decodes the JSON response into out.
// 429s surface the Retry-After header so Claude can tell the user when
// to retry instead of hammering.
func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		retry := resp.Header.Get("Retry-After")
		if retry == "" {
			retry = "a moment"
		}
		return fmt.Errorf("rate limited (429) — retry after %s", retry)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 400))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// slackEnvelope is the {ok, error} wrapper every Slack Web API response
// carries. Embedded by per-call response structs.
type slackEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (e slackEnvelope) check() error {
	if !e.OK {
		return fmt.Errorf("slack: %s", e.Error)
	}
	return nil
}

// slackGET calls a Slack Web API method with query params.
func (c *Client) slackGET(ctx context.Context, method string, params url.Values, out interface{ check() error }) error {
	tok, err := c.token(ServiceSlack)
	if err != nil {
		return err
	}
	u := slackAPIBase + "/" + method
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if err := c.do(req, out); err != nil {
		return err
	}
	return out.check()
}

// slackPOST calls a Slack Web API method with a JSON body.
func (c *Client) slackPOST(ctx context.Context, method string, body any, out interface{ check() error }) error {
	tok, err := c.token(ServiceSlack)
	if err != nil {
		return err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackAPIBase+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if err := c.do(req, out); err != nil {
		return err
	}
	return out.check()
}

// clickup runs one ClickUp v2 API call. ClickUp wants the raw personal
// token in Authorization (no Bearer prefix).
func (c *Client) clickup(ctx context.Context, method, path string, query url.Values, body, out any) error {
	tok, err := c.token(ServiceClickUp)
	if err != nil {
		return err
	}
	u := clickupAPIBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
