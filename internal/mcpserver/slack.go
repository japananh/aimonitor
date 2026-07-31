package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// slackMsg is the slimmed message shape returned by read tools. Slack's
// raw payloads carry blocks/attachments/metadata that multiply token cost
// without helping the model; keep what's needed to read and to thread.
//
// The *_count fields are always present (a cheap detection signal — a caller
// can tell a message carries an unfurl/preview card, blocks, or files without
// paying for the payload). The full arrays are opt-in per read tool, since
// they multiply token cost (blocks especially).
type slackMsg struct {
	TS         string `json:"ts"`
	User       string `json:"user,omitempty"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Permalink  string `json:"permalink,omitempty"`
	// Detection signals — always set (omitempty drops the common zero case).
	// A non-zero count means the content is present even when its full array
	// below is omitted; request it via the tool's include_* flag.
	AttachmentCount int `json:"attachment_count,omitempty"`
	BlockCount      int `json:"block_count,omitempty"`
	FileCount       int `json:"file_count,omitempty"`
	// Full payloads, verbatim from Slack, included only when the caller opts
	// in. Unfurl / link-preview cards — including async app unfurls like the
	// ClickUp app's link_shared card — arrive as entries in Attachments (look
	// for is_app_unfurl / app_unfurl_url).
	Attachments []json.RawMessage `json:"attachments,omitempty"`
	Blocks      []json.RawMessage `json:"blocks,omitempty"`
	Files       []json.RawMessage `json:"files,omitempty"`
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
	// Kept as raw JSON so we can pass them through verbatim (and count them)
	// without modelling Slack's sprawling attachment/block/file schemas.
	Attachments []json.RawMessage `json:"attachments"`
	Blocks      []json.RawMessage `json:"blocks"`
	Files       []json.RawMessage `json:"files"`
}

// slimOpts gates which heavy payloads slimMsg copies through. Counts are always
// computed regardless; these only control the full-array inclusion.
type slimOpts struct {
	attachments bool
	blocks      bool
	files       bool
}

func slimMsg(m rawSlackMsg, opts slimOpts) slackMsg {
	user := m.User
	if user == "" {
		user = m.Username
	}
	ch := m.Channel.ID
	if m.Channel.Name != "" {
		ch = "#" + m.Channel.Name
	}
	out := slackMsg{
		TS: m.TS, User: user, Text: m.Text, ThreadTS: m.ThreadTS,
		ReplyCount: m.ReplyCount, Channel: ch, Permalink: m.Permalink,
		AttachmentCount: len(m.Attachments),
		BlockCount:      len(m.Blocks),
		FileCount:       len(m.Files),
	}
	if opts.attachments {
		out.Attachments = m.Attachments
	}
	if opts.blocks {
		out.Blocks = m.Blocks
	}
	if opts.files {
		out.Files = m.Files
	}
	return out
}

// --- post message -----------------------------------------------------

type slackPostIn struct {
	Channel        string `json:"channel" jsonschema:"channel ID (C…/D…) or #name"`
	Text           string `json:"text" jsonschema:"message text (Slack mrkdwn). Pass it RAW — do not HTML-escape; mentions are <@USERID>, channels <#CHANNELID>, links <url|label> (escaping these to &lt;…&gt; posts them as literal text that doesn't ping)"`
	ThreadTS       string `json:"thread_ts,omitempty" jsonschema:"reply in this message's thread (its ts)"`
	ReplyBroadcast bool   `json:"reply_broadcast,omitempty" jsonschema:"also show the thread reply in the channel"`
	UnfurlLinks    *bool  `json:"unfurl_links,omitempty" jsonschema:"set false to suppress the link preview cards Slack expands under URLs (omit to keep Slack's default)"`
	UnfurlMedia    *bool  `json:"unfurl_media,omitempty" jsonschema:"set false to suppress image/video previews under URLs (omit to keep Slack's default)"`
	PostAt         int64  `json:"post_at,omitempty" jsonschema:"schedule the message for this Unix epoch time (seconds); must be in the future and within 120 days. When set, Slack holds and sends it at that time and the result carries scheduled_message_id instead of ts — cancel it before then with slack_cancel_scheduled_message"`
}

func (c *Client) slackPostMessage(ctx context.Context, _ *mcp.CallToolRequest, in slackPostIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"channel": in.Channel, "text": in.Text}
	if in.ThreadTS != "" {
		body["thread_ts"] = in.ThreadTS
		if in.ReplyBroadcast {
			body["reply_broadcast"] = true
		}
	}
	if in.UnfurlLinks != nil {
		body["unfurl_links"] = *in.UnfurlLinks
	}
	if in.UnfurlMedia != nil {
		body["unfurl_media"] = *in.UnfurlMedia
	}

	// post_at > 0 → hand it to Slack's native scheduler (same chat:write scope,
	// same body) so the message is held and sent at that time. The response
	// swaps ts for scheduled_message_id (cancel via slack_cancel_scheduled_message).
	if in.PostAt > 0 {
		body["post_at"] = in.PostAt
		var out struct {
			slackEnvelope
			Channel            string `json:"channel"`
			ScheduledMessageID string `json:"scheduled_message_id"`
			PostAt             int64  `json:"post_at"`
		}
		if err := c.slackPOST(ctx, "chat.scheduleMessage", body, &out); err != nil {
			return nil, nil, err
		}
		return textResult(map[string]any{
			"channel":              out.Channel,
			"scheduled_message_id": out.ScheduledMessageID,
			"post_at":              out.PostAt,
			"status":               "scheduled",
		})
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

// --- cancel scheduled message -----------------------------------------

type slackCancelScheduledIn struct {
	Channel            string `json:"channel" jsonschema:"channel ID the message was scheduled into (C…/D…/G…)"`
	ScheduledMessageID string `json:"scheduled_message_id" jsonschema:"the scheduled_message_id returned by slack_post_message when it was scheduled with post_at"`
}

func (c *Client) slackCancelScheduledMessage(ctx context.Context, _ *mcp.CallToolRequest, in slackCancelScheduledIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"channel": in.Channel, "scheduled_message_id": in.ScheduledMessageID}
	var out struct {
		slackEnvelope
	}
	if err := c.slackPOST(ctx, "chat.deleteScheduledMessage", body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{
		"channel":              in.Channel,
		"scheduled_message_id": in.ScheduledMessageID,
		"status":               "canceled",
	})
}

// --- update (edit) message --------------------------------------------

type slackUpdateIn struct {
	Channel     string `json:"channel" jsonschema:"channel ID the message is in (C…/D…/G…)"`
	TS          string `json:"ts" jsonschema:"the target message's ts (its timestamp ID, e.g. from slack_post_message)"`
	Text        string `json:"text" jsonschema:"new message text (Slack mrkdwn). Pass it RAW — do not HTML-escape; mentions are <@USERID>, channels <#CHANNELID>, links <url|label>"`
	UnfurlLinks *bool  `json:"unfurl_links,omitempty" jsonschema:"set false to suppress the link preview cards Slack expands under URLs (omit to keep Slack's default)"`
	UnfurlMedia *bool  `json:"unfurl_media,omitempty" jsonschema:"set false to suppress image/video previews under URLs (omit to keep Slack's default)"`
}

func (c *Client) slackUpdateMessage(ctx context.Context, _ *mcp.CallToolRequest, in slackUpdateIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"channel": in.Channel, "ts": in.TS, "text": in.Text}
	if in.UnfurlLinks != nil {
		body["unfurl_links"] = *in.UnfurlLinks
	}
	if in.UnfurlMedia != nil {
		body["unfurl_media"] = *in.UnfurlMedia
	}
	var out struct {
		slackEnvelope
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := c.slackPOST(ctx, "chat.update", body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"channel": out.Channel, "ts": out.TS, "status": "updated"})
}

// --- delete message ---------------------------------------------------

type slackDeleteIn struct {
	Channel string `json:"channel" jsonschema:"channel ID the message is in (C…/D…/G…)"`
	TS      string `json:"ts" jsonschema:"the target message's ts (its timestamp ID, e.g. from slack_post_message)"`
}

func (c *Client) slackDeleteMessage(ctx context.Context, _ *mcp.CallToolRequest, in slackDeleteIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"channel": in.Channel, "ts": in.TS}
	var out struct {
		slackEnvelope
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := c.slackPOST(ctx, "chat.delete", body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"channel": out.Channel, "ts": out.TS, "status": "deleted"})
}

// --- search -----------------------------------------------------------

type slackSearchIn struct {
	Query              string `json:"query" jsonschema:"search query; supports Slack modifiers like in:#channel from:@user before:YYYY-MM-DD"`
	Count              int    `json:"count,omitempty" jsonschema:"max results (default 20, max 100)"`
	IncludeAttachments bool   `json:"include_attachments,omitempty" jsonschema:"include each message's full attachments array (unfurl/link-preview cards, incl. async app unfurls like ClickUp's — look for is_app_unfurl/app_unfurl_url). Off by default to save tokens; attachment_count is always returned so you can detect cards cheaply"`
	IncludeBlocks      bool   `json:"include_blocks,omitempty" jsonschema:"include each message's full Block Kit blocks array. Off by default — blocks are the heaviest payload; block_count is always returned"`
	IncludeFiles       bool   `json:"include_files,omitempty" jsonschema:"include each message's files array (file-share metadata). Off by default; file_count is always returned"`
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
	opts := slimOpts{attachments: in.IncludeAttachments, blocks: in.IncludeBlocks, files: in.IncludeFiles}
	msgs := make([]slackMsg, 0, len(out.Messages.Matches))
	for _, m := range out.Messages.Matches {
		msgs = append(msgs, slimMsg(m, opts))
	}
	return textResult(map[string]any{"total": out.Messages.Total, "matches": msgs})
}

// --- history / replies ------------------------------------------------

type slackHistoryIn struct {
	Channel            string `json:"channel" jsonschema:"channel ID"`
	Limit              int    `json:"limit,omitempty" jsonschema:"max messages (default 30, max 200)"`
	Oldest             string `json:"oldest,omitempty" jsonschema:"only messages after this ts"`
	Latest             string `json:"latest,omitempty" jsonschema:"only messages before this ts"`
	IncludeAttachments bool   `json:"include_attachments,omitempty" jsonschema:"include each message's full attachments array (unfurl/link-preview cards, incl. async app unfurls like ClickUp's — look for is_app_unfurl/app_unfurl_url). Off by default to save tokens; attachment_count is always returned so you can detect cards cheaply"`
	IncludeBlocks      bool   `json:"include_blocks,omitempty" jsonschema:"include each message's full Block Kit blocks array. Off by default — blocks are the heaviest payload; block_count is always returned"`
	IncludeFiles       bool   `json:"include_files,omitempty" jsonschema:"include each message's files array (file-share metadata). Off by default; file_count is always returned"`
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
	opts := slimOpts{attachments: in.IncludeAttachments, blocks: in.IncludeBlocks, files: in.IncludeFiles}
	msgs := make([]slackMsg, 0, len(out.Messages))
	for _, m := range out.Messages {
		msgs = append(msgs, slimMsg(m, opts))
	}
	return textResult(map[string]any{"messages": msgs, "has_more": out.HasMore})
}

type slackRepliesIn struct {
	Channel            string `json:"channel" jsonschema:"channel ID"`
	TS                 string `json:"ts" jsonschema:"the thread parent message's ts"`
	Limit              int    `json:"limit,omitempty" jsonschema:"max replies (default 50, max 200)"`
	IncludeAttachments bool   `json:"include_attachments,omitempty" jsonschema:"include each message's full attachments array (unfurl/link-preview cards, incl. async app unfurls like ClickUp's — look for is_app_unfurl/app_unfurl_url). Off by default to save tokens; attachment_count is always returned so you can detect cards cheaply"`
	IncludeBlocks      bool   `json:"include_blocks,omitempty" jsonschema:"include each message's full Block Kit blocks array. Off by default — blocks are the heaviest payload; block_count is always returned"`
	IncludeFiles       bool   `json:"include_files,omitempty" jsonschema:"include each message's files array (file-share metadata). Off by default; file_count is always returned"`
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
	opts := slimOpts{attachments: in.IncludeAttachments, blocks: in.IncludeBlocks, files: in.IncludeFiles}
	msgs := make([]slackMsg, 0, len(out.Messages))
	for _, m := range out.Messages {
		msgs = append(msgs, slimMsg(m, opts))
	}
	return textResult(map[string]any{"messages": msgs})
}

// --- get file content ---------------------------------------------------

// slackFileMaxBytes caps how many bytes slack_get_file will pull down before
// flagging truncation. 1 MiB is generous for the shared logs/snippets/configs
// this tool targets while keeping a single response from blowing up.
const slackFileMaxBytes = 1 << 20

// slackFileInfo is the slice of Slack's files.info payload we surface.
type slackFileInfo struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	Mimetype           string `json:"mimetype"`
	Filetype           string `json:"filetype"`
	Size               int    `json:"size"`
	Lines              int    `json:"lines"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
}

// isTextMimetype reports whether a Slack file's mimetype is text-like enough to
// return inline. Slack snippets and shared .txt/.log/.json/.yaml files arrive as
// text/* or a handful of text-bearing application/* types; anything else is
// treated as binary and gets metadata + a note instead of raw bytes.
func isTextMimetype(m string) bool {
	if strings.HasPrefix(m, "text/") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript",
		"application/x-sh", "application/x-yaml", "application/yaml",
		"application/toml", "application/x-ndjson", "application/x-httpd-php":
		return true
	}
	return false
}

// sliceLines returns the 1-based [offset, offset+limit) line window of s. A
// non-positive offset means "from the start"; a non-positive limit means "to
// the end". ranged reports whether a window was actually applied. total is the
// line count of s (a trailing newline does not count as an extra empty line).
func sliceLines(s string, offset, limit int) (out string, total int, ranged bool) {
	lines := strings.Split(s, "\n")
	// A file ending in "\n" splits to a trailing "" — don't count it as a line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total = len(lines)
	if offset <= 0 && limit <= 0 {
		return s, total, false
	}
	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n"), total, true
}

type slackGetFileIn struct {
	File   string `json:"file" jsonschema:"file ID (F…), taken from a message's files[].id (returned when a read tool is called with include_files)"`
	Offset int    `json:"offset,omitempty" jsonschema:"1-based line to start returning from (default 1); pair with limit to page through a large file"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max lines to return starting at offset (default: all, up to the ~1 MiB byte cap)"`
}

// slackGetFile resolves a file id server-side: files.info for metadata, then —
// for text-like mimetypes — downloads url_private with the held token and
// returns the full text (optionally a line window). Binary/non-text files get
// metadata + a note, never raw bytes.
func (c *Client) slackGetFile(ctx context.Context, _ *mcp.CallToolRequest, in slackGetFileIn) (*mcp.CallToolResult, any, error) {
	if in.File == "" {
		return nil, nil, fmt.Errorf("file is required")
	}
	var info struct {
		slackEnvelope
		File slackFileInfo `json:"file"`
	}
	if err := c.slackGET(ctx, "files.info", url.Values{"file": {in.File}}, &info); err != nil {
		return nil, nil, err
	}
	f := info.File
	out := map[string]any{
		"id": f.ID, "name": f.Name, "title": f.Title,
		"mimetype": f.Mimetype, "filetype": f.Filetype,
		"size": f.Size, "lines": f.Lines,
	}

	// Non-text mimetype → metadata + a note, no bytes.
	if !isTextMimetype(f.Mimetype) {
		out["note"] = fmt.Sprintf("non-text file (mimetype %q); content not returned", f.Mimetype)
		return textResult(out)
	}

	dl := f.URLPrivateDownload
	if dl == "" {
		dl = f.URLPrivate
	}
	if dl == "" {
		out["note"] = "no download URL available for this file"
		return textResult(out)
	}

	data, err := c.slackDownload(ctx, dl, slackFileMaxBytes+1)
	if err != nil {
		return nil, nil, err
	}
	if len(data) > slackFileMaxBytes {
		data = data[:slackFileMaxBytes]
		out["truncated"] = true // byte cap hit; content is cut short
	}

	content, total, ranged := sliceLines(string(data), in.Offset, in.Limit)
	out["content"] = content
	if ranged {
		start := in.Offset
		if start < 1 {
			start = 1
		}
		out["offset"] = start
		out["content_lines"] = total
	}
	return textResult(out)
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

// --- file upload --------------------------------------------------------

type slackUploadIn struct {
	Filename string `json:"filename" jsonschema:"file name including extension (e.g. report.txt, snippet.go)"`
	Content  string `json:"content" jsonschema:"the file's content as text"`
	Channel  string `json:"channel,omitempty" jsonschema:"share into this channel ID"`
	ThreadTS string `json:"thread_ts,omitempty" jsonschema:"share as a reply in this thread (requires channel)"`
	Title    string `json:"title,omitempty" jsonschema:"display title (defaults to filename)"`
	Comment  string `json:"initial_comment,omitempty" jsonschema:"message text shown with the file"`
}

// slackUploadFile drives Slack's three-step external upload:
// getUploadURLExternal → raw POST of the bytes → completeUploadExternal
// (which also shares it into a channel/thread when given).
func (c *Client) slackUploadFile(ctx context.Context, _ *mcp.CallToolRequest, in slackUploadIn) (*mcp.CallToolResult, any, error) {
	if in.Filename == "" || in.Content == "" {
		return nil, nil, fmt.Errorf("filename and content are required")
	}
	data := []byte(in.Content)

	// Step 1: reserve an upload URL.
	var urlOut struct {
		slackEnvelope
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	params := url.Values{
		"filename": {in.Filename},
		"length":   {strconv.Itoa(len(data))},
	}
	if err := c.slackGET(ctx, "files.getUploadURLExternal", params, &urlOut); err != nil {
		return nil, nil, fmt.Errorf("reserve upload: %w", err)
	}

	// Step 2: POST the raw bytes to the reserved URL (no auth header).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlOut.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("upload bytes: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("upload bytes: HTTP %d", resp.StatusCode)
	}

	// Step 3: finalize + share.
	title := in.Title
	if title == "" {
		title = in.Filename
	}
	body := map[string]any{
		"files": []map[string]string{{"id": urlOut.FileID, "title": title}},
	}
	if in.Channel != "" {
		body["channel_id"] = in.Channel
	}
	if in.ThreadTS != "" {
		body["thread_ts"] = in.ThreadTS
	}
	if in.Comment != "" {
		body["initial_comment"] = in.Comment
	}
	var doneOut struct {
		slackEnvelope
		Files []struct {
			ID        string `json:"id"`
			Permalink string `json:"permalink"`
		} `json:"files"`
	}
	if err := c.slackPOST(ctx, "files.completeUploadExternal", body, &doneOut); err != nil {
		return nil, nil, fmt.Errorf("complete upload: %w", err)
	}
	res := map[string]string{"file_id": urlOut.FileID, "status": "uploaded"}
	if len(doneOut.Files) > 0 {
		res["permalink"] = doneOut.Files[0].Permalink
	}
	return textResult(res)
}
