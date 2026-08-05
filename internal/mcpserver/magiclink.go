package mcpserver

import (
	"context"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

// magicLinkRe matches a claude.ai magic-link and captures the base64 tail
// that self-identifies the target email. Shape observed from the n8n
// workflow's Slack posts: `https://claude.ai/magic-link#<hex>:<base64-email>`.
// The character classes stop at Slack's `<…>`/`|display` URL wrappers.
var magicLinkRe = regexp.MustCompile(`https://claude\.ai/magic-link#[0-9a-fA-F]+:([A-Za-z0-9+/_=-]+)`)

// parseMagicLink returns the first magic-link in text whose base64 tail
// decodes to email. Matching on the link's own encoded email (not the
// surrounding "To:" line) makes it robust when messages for several
// accounts interleave in the channel.
func parseMagicLink(text, email string) (string, bool) {
	for _, m := range magicLinkRe.FindAllStringSubmatch(text, -1) {
		if decodeB64Email(m[1]) == strings.ToLower(strings.TrimSpace(email)) {
			return m[0], true
		}
	}
	return "", false
}

// decodeB64Email tries the base64 variants a magic-link tail might use and
// returns the lowercased decoded string, or "" if none decode.
func decodeB64Email(s string) string {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return strings.ToLower(strings.TrimSpace(string(b)))
		}
	}
	return ""
}

// LatestMagicLink returns the newest claude.ai magic-link addressed to
// email in the given Slack channel. oldest (a Slack ts, "" = no bound)
// restricts the search so callers can require a link generated after they
// prompted the user — never a stale, already-consumed one. Returns
// ("", nil) when no matching link is present.
func (c *Client) LatestMagicLink(ctx context.Context, channel, email, oldest string) (string, error) {
	params := url.Values{"channel": {channel}, "limit": {"50"}}
	if oldest != "" {
		params.Set("oldest", oldest)
	}
	var out struct {
		slackEnvelope
		Messages []rawSlackMsg `json:"messages"`
	}
	if err := c.slackGET(ctx, "conversations.history", params, &out); err != nil {
		return "", err
	}
	// conversations.history is newest-first, so the first match is freshest.
	for _, m := range out.Messages {
		if link, ok := parseMagicLink(m.Text, email); ok {
			return link, nil
		}
	}
	return "", nil
}
