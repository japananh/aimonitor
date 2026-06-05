package mcpserver

import (
	"context"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// slackMsg is the slimmed message shape returned by read tools. Slack's
// raw payloads carry blocks/attachments/metadata that multiply token cost
// without helping the model; keep what's needed to read and to thread.
type slackMsg struct {
	TS         string `json:"ts"`
	User       string `json:"user,omitempty"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Permalink  string `json:"permalink,omitempty"`
}

type rawSlackMsg struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	Username   string `json:"username"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
	Permalink  string `json:"permalink"`
	Channel    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
}

func slimMsg(m rawSlackMsg) slackMsg {
	user := m.User
	if user == "" {
		user = m.Username
	}
	ch := m.Channel.ID
	if m.Channel.Name != "" {
		ch = "#" + m.Channel.Name
	}
	return slackMsg{
		TS: m.TS, User: user, Text: m.Text, ThreadTS: m.ThreadTS,
		ReplyCount: m.ReplyCount, Channel: ch, Permalink: m.Permalink,
	}
}

// --- post message -----------------------------------------------------

type slackPostIn struct {
	Channel        string `json:"channel" jsonschema:"channel ID (C…/D…) or #name"`
	Text           string `json:"text" jsonschema:"message text (Slack mrkdwn)"`
	ThreadTS       string `json:"thread_ts,omitempty" jsonschema:"reply in this message's thread (its ts)"`
	ReplyBroadcast bool   `json:"reply_broadcast,omitempty" jsonschema:"also show the thread reply in the channel"`
}

func (c *Client) slackPostMessage(ctx context.Context, _ *mcp.CallToolRequest, in slackPostIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"channel": in.Channel, "text": in.Text}
	if in.ThreadTS != "" {
		body["thread_ts"] = in.ThreadTS
		if in.ReplyBroadcast {
			body["reply_broadcast"] = true
		}
	}
	var out struct {
		slackEnvelope
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := c.slackPOST(ctx, "chat.postMessage", body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"channel": out.Channel, "ts": out.TS, "status": "posted"})
}

// --- search -----------------------------------------------------------

type slackSearchIn struct {
	Query string `json:"query" jsonschema:"search query; supports Slack modifiers like in:#channel from:@user before:YYYY-MM-DD"`
	Count int    `json:"count,omitempty" jsonschema:"max results (default 20, max 100)"`
}

func (c *Client) slackSearchMessages(ctx context.Context, _ *mcp.CallToolRequest, in slackSearchIn) (*mcp.CallToolResult, any, error) {
	count := in.Count
	if count <= 0 || count > 100 {
		count = 20
	}
	params := url.Values{"query": {in.Query}, "count": {strconv.Itoa(count)}}
	var out struct {
		slackEnvelope
		Messages struct {
			Total   int           `json:"total"`
			Matches []rawSlackMsg `json:"matches"`
		} `json:"messages"`
	}
	if err := c.slackGET(ctx, "search.messages", params, &out); err != nil {
		return nil, nil, err
	}
	msgs := make([]slackMsg, 0, len(out.Messages.Matches))
	for _, m := range out.Messages.Matches {
		msgs = append(msgs, slimMsg(m))
	}
	return textResult(map[string]any{"total": out.Messages.Total, "matches": msgs})
}

// --- history / replies ------------------------------------------------

type slackHistoryIn struct {
	Channel string `json:"channel" jsonschema:"channel ID"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max messages (default 30, max 200)"`
	Oldest  string `json:"oldest,omitempty" jsonschema:"only messages after this ts"`
	Latest  string `json:"latest,omitempty" jsonschema:"only messages before this ts"`
}

func (c *Client) slackChannelHistory(ctx context.Context, _ *mcp.CallToolRequest, in slackHistoryIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	params := url.Values{"channel": {in.Channel}, "limit": {strconv.Itoa(limit)}}
	if in.Oldest != "" {
		params.Set("oldest", in.Oldest)
	}
	if in.Latest != "" {
		params.Set("latest", in.Latest)
	}
	var out struct {
		slackEnvelope
		Messages []rawSlackMsg `json:"messages"`
		HasMore  bool          `json:"has_more"`
	}
	if err := c.slackGET(ctx, "conversations.history", params, &out); err != nil {
		return nil, nil, err
	}
	msgs := make([]slackMsg, 0, len(out.Messages))
	for _, m := range out.Messages {
		msgs = append(msgs, slimMsg(m))
	}
	return textResult(map[string]any{"messages": msgs, "has_more": out.HasMore})
}

type slackRepliesIn struct {
	Channel string `json:"channel" jsonschema:"channel ID"`
	TS      string `json:"ts" jsonschema:"the thread parent message's ts"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max replies (default 50, max 200)"`
}

func (c *Client) slackThreadReplies(ctx context.Context, _ *mcp.CallToolRequest, in slackRepliesIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	params := url.Values{"channel": {in.Channel}, "ts": {in.TS}, "limit": {strconv.Itoa(limit)}}
	var out struct {
		slackEnvelope
		Messages []rawSlackMsg `json:"messages"`
	}
	if err := c.slackGET(ctx, "conversations.replies", params, &out); err != nil {
		return nil, nil, err
	}
	msgs := make([]slackMsg, 0, len(out.Messages))
	for _, m := range out.Messages {
		msgs = append(msgs, slimMsg(m))
	}
	return textResult(map[string]any{"messages": msgs})
}

// --- channels / users -------------------------------------------------

type slackListChannelsIn struct {
	Types  string `json:"types,omitempty" jsonschema:"comma-separated: public_channel,private_channel,im,mpim (default public+private channels)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max channels per page (default 100, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"pagination cursor from a previous call"`
}

func (c *Client) slackListChannels(ctx context.Context, _ *mcp.CallToolRequest, in slackListChannelsIn) (*mcp.CallToolResult, any, error) {
	types := in.Types
	if types == "" {
		types = "public_channel,private_channel"
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	params := url.Values{
		"types": {types}, "limit": {strconv.Itoa(limit)},
		"exclude_archived": {"true"},
	}
	if in.Cursor != "" {
		params.Set("cursor", in.Cursor)
	}
	var out struct {
		slackEnvelope
		Channels []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsPrivate bool   `json:"is_private"`
			IsIM      bool   `json:"is_im"`
		} `json:"channels"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.slackGET(ctx, "conversations.list", params, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{
		"channels":    out.Channels,
		"next_cursor": out.ResponseMetadata.NextCursor,
	})
}

type slackListUsersIn struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"max users per page (default 100, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"pagination cursor from a previous call"`
}

type slackUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	IsBot    bool   `json:"is_bot,omitempty"`
}

func (c *Client) slackListUsers(ctx context.Context, _ *mcp.CallToolRequest, in slackListUsersIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	params := url.Values{"limit": {strconv.Itoa(limit)}}
	if in.Cursor != "" {
		params.Set("cursor", in.Cursor)
	}
	var out struct {
		slackEnvelope
		Members          []slackUser `json:"members"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.slackGET(ctx, "users.list", params, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{
		"users":       out.Members,
		"next_cursor": out.ResponseMetadata.NextCursor,
	})
}

type slackGetUserIn struct {
	User string `json:"user" jsonschema:"user ID (U…)"`
}

func (c *Client) slackGetUser(ctx context.Context, _ *mcp.CallToolRequest, in slackGetUserIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		slackEnvelope
		User slackUser `json:"user"`
	}
	if err := c.slackGET(ctx, "users.info", url.Values{"user": {in.User}}, &out); err != nil {
		return nil, nil, err
	}
	return textResult(out.User)
}

type slackPermalinkIn struct {
	Channel   string `json:"channel" jsonschema:"channel ID"`
	MessageTS string `json:"message_ts" jsonschema:"the message's ts"`
}

func (c *Client) slackGetPermalink(ctx context.Context, _ *mcp.CallToolRequest, in slackPermalinkIn) (*mcp.CallToolResult, any, error) {
	params := url.Values{"channel": {in.Channel}, "message_ts": {in.MessageTS}}
	var out struct {
		slackEnvelope
		Permalink string `json:"permalink"`
	}
	if err := c.slackGET(ctx, "chat.getPermalink", params, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"permalink": out.Permalink})
}
