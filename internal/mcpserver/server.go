package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/japananh/aimonitor/internal/version"
)

// textResult wraps a value as a single JSON text content block — the
// standard return shape for every tool here. Tools return raw data;
// Claude does the prose.
func textResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

// toolDef pairs a tool's metadata with its registration thunk so the
// catalog below stays declarative. write=true tools are skipped in
// read-only mode.
type toolDef struct {
	name  string
	svc   Service
	write bool
	add   func(s *mcp.Server, c *Client, name, desc string)
	desc  string
}

// addTyped builds a registration thunk for a handler with input type In.
func addTyped[In any](h func(*Client) mcp.ToolHandlerFor[In, any]) func(*mcp.Server, *Client, string, string) {
	return func(s *mcp.Server, c *Client, name, desc string) {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, h(c))
	}
}

// catalog is every tool this server can expose, in stable order.
func catalog() []toolDef {
	return []toolDef{
		// Slack
		{name: "slack_post_message", svc: ServiceSlack, write: true,
			desc: "Post a message to a Slack channel, or reply in a thread via thread_ts",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackPostIn, any] {
				return c.slackPostMessage
			})},
		{name: "slack_update_message", svc: ServiceSlack, write: true,
			desc: "Edit a Slack message you posted (by channel + ts) — e.g. to fix a broken mention",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackUpdateIn, any] {
				return c.slackUpdateMessage
			})},
		{name: "slack_delete_message", svc: ServiceSlack, write: true,
			desc: "Delete a Slack message you posted (by channel + ts)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackDeleteIn, any] {
				return c.slackDeleteMessage
			})},
		{name: "slack_upload_file", svc: ServiceSlack, write: true,
			desc: "Upload a text file to Slack, optionally sharing it into a channel or thread",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackUploadIn, any] {
				return c.slackUploadFile
			})},
		{name: "slack_search_messages", svc: ServiceSlack,
			desc: "Search Slack messages across the workspace (supports in:#channel, from:@user, before:/after: modifiers). Each result carries attachment_count/block_count/file_count; set include_attachments to see unfurl/preview cards",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackSearchIn, any] {
				return c.slackSearchMessages
			})},
		{name: "slack_channel_history", svc: ServiceSlack,
			desc: "Fetch recent messages from a Slack channel. Each message carries attachment_count/block_count/file_count; set include_attachments to see unfurl/preview cards (incl. app unfurls like ClickUp's)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackHistoryIn, any] {
				return c.slackChannelHistory
			})},
		{name: "slack_thread_replies", svc: ServiceSlack,
			desc: "Fetch the replies in a Slack thread. Each message carries attachment_count/block_count/file_count; set include_attachments to see unfurl/preview cards",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackRepliesIn, any] {
				return c.slackThreadReplies
			})},
		{name: "slack_get_file", svc: ServiceSlack,
			desc: "Read the full content of a Slack file attachment by its files[].id (the read tools only expose Slack's short, truncated preview). Fetches server-side with the workspace token; returns full text + metadata (name, mimetype, size, lines) for text-like files, with optional offset/limit line paging and a ~1 MiB cap. Binary/non-text files return metadata + a note",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackGetFileIn, any] {
				return c.slackGetFile
			})},
		{name: "slack_list_channels", svc: ServiceSlack,
			desc: "List Slack channels (public + private by default)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackListChannelsIn, any] {
				return c.slackListChannels
			})},
		{name: "slack_list_users", svc: ServiceSlack,
			desc: "List Slack workspace users",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackListUsersIn, any] {
				return c.slackListUsers
			})},
		{name: "slack_get_user", svc: ServiceSlack,
			desc: "Get one Slack user by ID",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackGetUserIn, any] {
				return c.slackGetUser
			})},
		{name: "slack_get_permalink", svc: ServiceSlack,
			desc: "Get the permalink URL for a Slack message",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackPermalinkIn, any] {
				return c.slackGetPermalink
			})},

		// ClickUp
		{name: "clickup_list_workspaces", svc: ServiceClickUp,
			desc: "List ClickUp workspaces (teams) the token can access",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[struct{}, any] {
				return c.clickupListWorkspaces
			})},
		{name: "clickup_list_spaces", svc: ServiceClickUp,
			desc: "List spaces in a ClickUp workspace",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuWorkspaceIn, any] {
				return c.clickupListSpaces
			})},
		{name: "clickup_list_folders", svc: ServiceClickUp,
			desc: "List folders (and their lists) in a ClickUp space",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuSpaceIn, any] {
				return c.clickupListFolders
			})},
		{name: "clickup_list_lists", svc: ServiceClickUp,
			desc: "List ClickUp lists in a folder, or folderless lists in a space",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuListListsIn, any] {
				return c.clickupListLists
			})},
		{name: "clickup_list_members", svc: ServiceClickUp,
			desc: "List members (id/username/email) of a ClickUp workspace, or — via task_id — of a task. Use task_id to resolve assignee/@mention user IDs when the workspace member list is empty (ClickUp omits members from /team for large workspaces)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuListMembersIn, any] {
				return c.clickupListMembers
			})},
		{name: "clickup_list_tasks", svc: ServiceClickUp,
			desc: "List tasks in a ClickUp list (filter by status, include closed, paginate)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuListTasksIn, any] {
				return c.clickupListTasks
			})},
		{name: "clickup_search_tasks", svc: ServiceClickUp,
			desc: "Search tasks across a ClickUp workspace by assignee, status, or last-updated time",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuSearchTasksIn, any] {
				return c.clickupSearchTasks
			})},
		{name: "clickup_get_task", svc: ServiceClickUp,
			desc: "Get one ClickUp task with its description, attachments (downloadable URLs), and, by default, its flattened subtask/sub-subtask tree",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTaskIn, any] {
				return c.clickupGetTask
			})},
		{name: "clickup_create_task", svc: ServiceClickUp, write: true,
			desc: "Create a ClickUp task (or subtask via parent); set custom_item_id for a work-item type like Bug",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreateTaskIn, any] {
				return c.clickupCreateTask
			})},
		{name: "clickup_update_task", svc: ServiceClickUp, write: true,
			desc: "Update a ClickUp task's name, description, status, priority, due date, assignees (add/remove), or work-item type (custom_item_id)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdateTaskIn, any] {
				return c.clickupUpdateTask
			})},
		{name: "clickup_delete_task", svc: ServiceClickUp, write: true,
			desc: "Delete one or more ClickUp tasks by ID (task_ids); each is moved to the Trash (recoverable). Subtasks are tasks — delete them the same way. Returns which ids were deleted and which failed (with the error), so failures can be retried",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDeleteTaskIn, any] {
				return c.clickupDeleteTask
			})},
		{name: "clickup_add_tag", svc: ServiceClickUp, write: true,
			desc: "Add an existing Space tag to a ClickUp task (the tag must already exist). Use this to (re)tag a task after creation, since clickup_update_task can't change tags",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTagIn, any] {
				return c.clickupAddTag
			})},
		{name: "clickup_remove_tag", svc: ServiceClickUp, write: true,
			desc: "Remove a tag from a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTagIn, any] {
				return c.clickupRemoveTag
			})},
		{name: "clickup_list_custom_item_types", svc: ServiceClickUp,
			desc: "List a ClickUp workspace's custom work-item types (id + name, e.g. Bug) to resolve a name to the custom_item_id used by clickup_create_task / clickup_update_task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuWorkspaceIn, any] {
				return c.clickupListCustomItemTypes
			})},
		{name: "clickup_add_comment", svc: ServiceClickUp, write: true,
			desc: "Add a comment to a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuAddCommentIn, any] {
				return c.clickupAddComment
			})},
		{name: "clickup_list_docs", svc: ServiceClickUp,
			desc: "List ClickUp Docs in a workspace",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuListDocsIn, any] {
				return c.clickupListDocs
			})},
		{name: "clickup_get_doc", svc: ServiceClickUp,
			desc: "Get a ClickUp Doc's pages with their markdown content (doc URL: …/v/dc/<doc_id>/<page_id>)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDocIn, any] {
				return c.clickupGetDoc
			})},
		{name: "clickup_get_page", svc: ServiceClickUp,
			desc: "Get one ClickUp Doc page's markdown content",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuGetPageIn, any] {
				return c.clickupGetPage
			})},
		{name: "clickup_create_doc", svc: ServiceClickUp, write: true,
			desc: "Create a new ClickUp Doc in a workspace",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreateDocIn, any] {
				return c.clickupCreateDoc
			})},
		{name: "clickup_create_page", svc: ServiceClickUp, write: true,
			desc: "Add a page (markdown) to a ClickUp Doc",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreatePageIn, any] {
				return c.clickupCreatePage
			})},
		{name: "clickup_update_page", svc: ServiceClickUp, write: true,
			desc: "Edit a ClickUp Doc page's markdown content (replace, append, or prepend)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdatePageIn, any] {
				return c.clickupUpdatePage
			})},
		{name: "clickup_delete_comment", svc: ServiceClickUp, write: true,
			desc: "Delete a ClickUp comment by its ID",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDeleteCommentIn, any] {
				return c.clickupDeleteComment
			})},
		{name: "clickup_update_comment", svc: ServiceClickUp, write: true,
			desc: "Edit a ClickUp comment's text by its ID",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdateCommentIn, any] {
				return c.clickupUpdateComment
			})},
		{name: "clickup_list_comments", svc: ServiceClickUp,
			desc: "List the top-level comments on a ClickUp task. Each carries reply_count; when >0, fetch the thread with clickup_list_comment_replies",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTaskIn, any] {
				return c.clickupListComments
			})},
		{name: "clickup_list_comment_replies", svc: ServiceClickUp,
			desc: "List the threaded replies to a ClickUp comment (comment_id from clickup_list_comments; use when a comment's reply_count > 0)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCommentRepliesIn, any] {
				return c.clickupListCommentReplies
			})},
		{name: "clickup_upload_attachment", svc: ServiceClickUp, write: true,
			desc: "Attach a file (given as text content) to a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUploadAttachmentIn, any] {
				return c.clickupUploadAttachment
			})},
		{name: "clickup_list_custom_fields", svc: ServiceClickUp,
			desc: "List the custom fields accessible on a ClickUp list (id + name + type, with option UUIDs in type_config for drop_down/labels) to resolve a field name to the field_id used by clickup_set_custom_field / clickup_remove_custom_field",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuListFieldsIn, any] {
				return c.clickupListCustomFields
			})},
		{name: "clickup_set_custom_field", svc: ServiceClickUp, write: true,
			desc: "Set a custom field value on a ClickUp task (value shaped for the field type: string/number, an option UUID for drop_down, an array for labels, unix ms for date)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuSetCustomFieldIn, any] {
				return c.clickupSetCustomField
			})},
		{name: "clickup_remove_custom_field", svc: ServiceClickUp, write: true,
			desc: "Clear a task's value for one ClickUp custom field",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuRemoveCustomFieldIn, any] {
				return c.clickupRemoveCustomField
			})},
		{name: "clickup_add_dependency", svc: ServiceClickUp, write: true,
			desc: "Link a ClickUp task dependency: set depends_on (task_id waits on it) OR dependency_of (task_id blocks it). ClickUp has no update endpoint — re-point a dependency by deleting and re-adding",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDependencyIn, any] {
				return c.clickupAddDependency
			})},
		{name: "clickup_delete_dependency", svc: ServiceClickUp, write: true,
			desc: "Remove a ClickUp task dependency (pass the same depends_on or dependency_of used to add it)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDependencyIn, any] {
				return c.clickupDeleteDependency
			})},
		{name: "clickup_create_checklist", svc: ServiceClickUp, write: true,
			desc: "Add a checklist to a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreateChecklistIn, any] {
				return c.clickupCreateChecklist
			})},
		{name: "clickup_update_checklist", svc: ServiceClickUp, write: true,
			desc: "Rename or reposition a ClickUp checklist",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdateChecklistIn, any] {
				return c.clickupUpdateChecklist
			})},
		{name: "clickup_delete_checklist", svc: ServiceClickUp, write: true,
			desc: "Delete a ClickUp checklist and all its items",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDeleteChecklistIn, any] {
				return c.clickupDeleteChecklist
			})},
		{name: "clickup_create_checklist_item", svc: ServiceClickUp, write: true,
			desc: "Add an item to a ClickUp checklist (optionally assigned); returns the checklist with each item's id for follow-up edits",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreateChecklistItemIn, any] {
				return c.clickupCreateChecklistItem
			})},
		{name: "clickup_update_checklist_item", svc: ServiceClickUp, write: true,
			desc: "Edit a ClickUp checklist item: rename, mark resolved/unresolved, (re)assign, or nest it under another item (parent)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdateChecklistItemIn, any] {
				return c.clickupUpdateChecklistItem
			})},
		{name: "clickup_delete_checklist_item", svc: ServiceClickUp, write: true,
			desc: "Delete one item from a ClickUp checklist",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuDeleteChecklistItemIn, any] {
				return c.clickupDeleteChecklistItem
			})},

		// Sentry
		{name: "sentry_list_projects", svc: ServiceSentry,
			desc: "List Sentry projects in the organization (use it to resolve a project slug to the numeric id the issues API wants)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryListProjectsIn, any] {
				return c.sentryListProjects
			})},
		{name: "sentry_search_issues", svc: ServiceSentry,
			desc: "Search Sentry issues for a triage digest — each row has shortId, title, culprit, event count, users affected, first/last seen, level, status, permalink. Supports queries like 'is:unresolved firstSeen:-24h', a project filter (slug or id), statsPeriod, and sort by freq/date/new/user",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentrySearchIssuesIn, any] {
				return c.sentrySearchIssues
			})},
		{name: "sentry_get_issue", svc: ServiceSentry,
			desc: "Get one Sentry issue's detail by numeric id or shortId (e.g. PRICING-SERVICE-9V)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryGetIssueIn, any] {
				return c.sentryGetIssue
			})},
		{name: "sentry_get_latest_event", svc: ServiceSentry,
			desc: "Get a Sentry issue's latest event for root-causing — exception type/value + stacktrace frames (filename/function/line) and event tags. By numeric id or shortId",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryGetLatestEventIn, any] {
				return c.sentryGetLatestEvent
			})},
		{name: "sentry_issue_tags", svc: ServiceSentry,
			desc: "Tag value distribution for a Sentry issue — pass key (e.g. shop.id, environment, user) for per-value counts ('how many X affected / confirm scope'), or omit to list all tag keys",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryIssueTagsIn, any] {
				return c.sentryIssueTags
			})},
		{name: "sentry_update_issue", svc: ServiceSentry, write: true,
			desc: "Update a Sentry issue: set status (resolved/unresolved/ignored) and/or assign it (assigned_to = email, user:<id>, or team:<id>)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryUpdateIssueIn, any] {
				return c.sentryUpdateIssue
			})},
		{name: "sentry_add_comment", svc: ServiceSentry, write: true,
			desc: "Add a comment to a Sentry issue",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryAddCommentIn, any] {
				return c.sentryAddComment
			})},
		{name: "sentry_delete_comment", svc: ServiceSentry, write: true,
			desc: "Delete a comment from a Sentry issue by its comment_id (from sentry_add_comment). Needs an event:admin-scoped token (Issue & Event = Admin); add/update/resolve only need Write",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[sentryDeleteCommentIn, any] {
				return c.sentryDeleteComment
			})},
	}
}

// connected reports which services have a stored token. A service without
// a token registers no tools at all (instead of N tools that each fail).
func connected(creds *CredStore) map[Service]bool {
	out := map[Service]bool{}
	for _, svc := range Services {
		tok, err := creds.Token(svc)
		out[svc] = err == nil && tok != ""
	}
	return out
}

// BuildServer assembles the MCP server, registering only the tools that
// the config and connection state allow:
//   - service not connected or mcp.<svc>.enabled=false → no tools
//   - mcp.<svc>.read_only=true → write tools skipped
//   - mcp.disabled_tools → individually hidden
//
// Hidden tools don't exist for Claude at all (not in tools/list), which
// both enforces read-only regardless of past "always allow" choices and
// saves context tokens.
func BuildServer(cfg Config, creds *CredStore) (*mcp.Server, []string) {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:       "aimonitor",
		Title:      "AIMonitor — Slack, ClickUp & Sentry tools",
		Version:    version.Version,
		WebsiteURL: "https://github.com/japananh/aimonitor",
	}, nil)

	client := NewClient(creds)
	client.SentryOrg = cfg.SentryOrg
	client.SentryAPIBase = cfg.SentryBaseURL
	conn := connected(creds)
	var registered []string
	for _, t := range catalog() {
		switch {
		case !conn[t.svc], !cfg.Enabled[t.svc]:
			continue
		case t.write && cfg.ReadOnly[t.svc]:
			continue
		case cfg.Disabled[t.name]:
			continue
		}
		t.add(srv, client, t.name, t.desc)
		registered = append(registered, t.name)
	}
	return srv, registered
}

// Serve runs the stdio MCP server until the client disconnects or ctx is
// cancelled. This is the entrypoint for `aimonitor mcp serve`.
//
// A client disconnect (Claude Code ending the session) surfaces from the
// SDK as an EOF-flavoured error — that is the NORMAL end of an MCP
// process's life, not a failure, so it maps to nil. Anything written to
// stdout besides JSON-RPC frames would corrupt the protocol, which is
// also why the caller must keep cobra's usage/error printing off.
func Serve(ctx context.Context, cfg Config, creds *CredStore) error {
	srv, _ := BuildServer(cfg, creds)
	err := srv.Run(ctx, &mcp.StdioTransport{})
	switch {
	case err == nil,
		errors.Is(err, io.EOF),
		errors.Is(err, context.Canceled),
		// The SDK wraps the disconnect as "server is closing: EOF" without
		// a matchable sentinel; the suffix check catches it.
		strings.HasSuffix(err.Error(), "EOF"):
		return nil
	}
	return err
}
