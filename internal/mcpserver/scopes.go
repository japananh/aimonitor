package mcpserver

import "strings"

// SlackUserTokenScopes are the Slack user-token OAuth scopes the Slack tools
// need. It's the source of truth for the setup docs/help: aimonitor stores a
// user-provided xoxp- token and cannot request scopes itself, so the user
// grants these on their Slack app (api.slack.com → OAuth & Permissions →
// User Token Scopes), reinstalls, then re-runs `aimonitor mcp connect slack`.
//
// Tool → Slack method → scope:
//
//	search_messages                       search.messages        search:read
//	get_user / list_users                 users.info/users.list  users:read
//	channel_history / thread_replies      conversations.history  channels:history groups:history im:history mpim:history
//	list_channels                         conversations.list     channels:read groups:read im:read mpim:read
//	post_message / update / delete        chat.*                 chat:write
//	upload_file                           files.*                files:write
var SlackUserTokenScopes = []string{
	"search:read",
	"users:read",
	"channels:history", "groups:history", "im:history", "mpim:history",
	"channels:read", "groups:read", "im:read", "mpim:read",
	"chat:write",
	"files:write",
}

// SlackScopesCSV is the scope list as a comma-separated string for display.
func SlackScopesCSV() string {
	return strings.Join(SlackUserTokenScopes, ", ")
}
