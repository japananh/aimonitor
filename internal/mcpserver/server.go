package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

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
		{name: "slack_search_messages", svc: ServiceSlack,
			desc: "Search Slack messages across the workspace (supports in:#channel, from:@user, before:/after: modifiers)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackSearchIn, any] {
				return c.slackSearchMessages
			})},
		{name: "slack_channel_history", svc: ServiceSlack,
			desc: "Fetch recent messages from a Slack channel",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackHistoryIn, any] {
				return c.slackChannelHistory
			})},
		{name: "slack_thread_replies", svc: ServiceSlack,
			desc: "Fetch the replies in a Slack thread",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[slackRepliesIn, any] {
				return c.slackThreadReplies
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
			desc: "List members of a ClickUp workspace (for assignee user IDs)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuWorkspaceIn, any] {
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
			desc: "Get one ClickUp task with its description",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTaskIn, any] {
				return c.clickupGetTask
			})},
		{name: "clickup_create_task", svc: ServiceClickUp, write: true,
			desc: "Create a ClickUp task (or subtask via parent)",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuCreateTaskIn, any] {
				return c.clickupCreateTask
			})},
		{name: "clickup_update_task", svc: ServiceClickUp, write: true,
			desc: "Update a ClickUp task's name, description, status, priority, or due date",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuUpdateTaskIn, any] {
				return c.clickupUpdateTask
			})},
		{name: "clickup_add_comment", svc: ServiceClickUp, write: true,
			desc: "Add a comment to a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuAddCommentIn, any] {
				return c.clickupAddComment
			})},
		{name: "clickup_list_comments", svc: ServiceClickUp,
			desc: "List the comments on a ClickUp task",
			add: addTyped(func(c *Client) mcp.ToolHandlerFor[cuTaskIn, any] {
				return c.clickupListComments
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
		Title:      "AIMonitor — Slack & ClickUp tools",
		Version:    version.Version,
		WebsiteURL: "https://github.com/japananh/aimonitor",
	}, nil)

	client := NewClient(creds)
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
func Serve(ctx context.Context, cfg Config, creds *CredStore) error {
	srv, _ := BuildServer(cfg, creds)
	return srv.Run(ctx, &mcp.StdioTransport{})
}
