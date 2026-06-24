package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cuTask is the slimmed task shape returned by list/search tools. ClickUp's
// raw task payloads are enormous (custom fields, watchers, checklists);
// keep the fields needed to identify, triage, and link.
type cuTask struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Status    string   `json:"status,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	DueDate   string   `json:"due_date,omitempty"`
	List      string   `json:"list,omitempty"`
	URL       string   `json:"url,omitempty"`
}

type rawCUTask struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status struct {
		Status string `json:"status"`
	} `json:"status"`
	Assignees []struct {
		Username string `json:"username"`
	} `json:"assignees"`
	Priority *struct {
		Priority string `json:"priority"`
	} `json:"priority"`
	DueDate string `json:"due_date"`
	List    struct {
		Name string `json:"name"`
	} `json:"list"`
	URL string `json:"url"`
}

func slimTask(t rawCUTask) cuTask {
	out := cuTask{
		ID: t.ID, Name: t.Name, Status: t.Status.Status,
		DueDate: t.DueDate, List: t.List.Name, URL: t.URL,
	}
	for _, a := range t.Assignees {
		out.Assignees = append(out.Assignees, a.Username)
	}
	if t.Priority != nil {
		out.Priority = t.Priority.Priority
	}
	return out
}

// --- hierarchy ----------------------------------------------------------

func (c *Client) clickupListWorkspaces(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	var out struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"teams"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/team", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"workspaces": out.Teams})
}

type cuWorkspaceIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
}

func (c *Client) clickupListSpaces(ctx context.Context, _ *mcp.CallToolRequest, in cuWorkspaceIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		Spaces []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Private bool   `json:"private"`
		} `json:"spaces"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/team/"+url.PathEscape(in.WorkspaceID)+"/space", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"spaces": out.Spaces})
}

type cuSpaceIn struct {
	SpaceID string `json:"space_id" jsonschema:"space ID"`
}

func (c *Client) clickupListFolders(ctx context.Context, _ *mcp.CallToolRequest, in cuSpaceIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		Folders []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Lists []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"lists"`
		} `json:"folders"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/space/"+url.PathEscape(in.SpaceID)+"/folder", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"folders": out.Folders})
}

type cuListListsIn struct {
	FolderID string `json:"folder_id,omitempty" jsonschema:"folder ID (use this OR space_id)"`
	SpaceID  string `json:"space_id,omitempty" jsonschema:"space ID for folderless lists"`
}

func (c *Client) clickupListLists(ctx context.Context, _ *mcp.CallToolRequest, in cuListListsIn) (*mcp.CallToolResult, any, error) {
	var path string
	switch {
	case in.FolderID != "":
		path = "/folder/" + url.PathEscape(in.FolderID) + "/list"
	case in.SpaceID != "":
		path = "/space/" + url.PathEscape(in.SpaceID) + "/list"
	default:
		return nil, nil, fmt.Errorf("provide folder_id or space_id")
	}
	var out struct {
		Lists []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			TaskCount any    `json:"task_count"`
		} `json:"lists"`
	}
	if err := c.clickup(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"lists": out.Lists})
}

func (c *Client) clickupListMembers(ctx context.Context, _ *mcp.CallToolRequest, in cuWorkspaceIn) (*mcp.CallToolResult, any, error) {
	// v2 has no /team/{id}/member endpoint; members ride on GET /team.
	var out struct {
		Teams []struct {
			ID      string `json:"id"`
			Members []struct {
				User struct {
					ID       int    `json:"id"`
					Username string `json:"username"`
					Email    string `json:"email"`
				} `json:"user"`
			} `json:"members"`
		} `json:"teams"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/team", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	type member struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	var members []member
	for _, t := range out.Teams {
		if t.ID != in.WorkspaceID {
			continue
		}
		for _, m := range t.Members {
			members = append(members, member(m.User))
		}
	}
	return textResult(map[string]any{"members": members})
}

// --- tasks --------------------------------------------------------------

type cuListTasksIn struct {
	ListID        string   `json:"list_id" jsonschema:"list ID"`
	Statuses      []string `json:"statuses,omitempty" jsonschema:"only these statuses"`
	IncludeClosed bool     `json:"include_closed,omitempty" jsonschema:"include closed tasks"`
	Page          int      `json:"page,omitempty" jsonschema:"page number (0-based; 100 tasks per page)"`
}

func (c *Client) clickupListTasks(ctx context.Context, _ *mcp.CallToolRequest, in cuListTasksIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	for _, s := range in.Statuses {
		q.Add("statuses[]", s)
	}
	if in.IncludeClosed {
		q.Set("include_closed", "true")
	}
	if in.Page > 0 {
		q.Set("page", strconv.Itoa(in.Page))
	}
	var out struct {
		Tasks []rawCUTask `json:"tasks"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/list/"+url.PathEscape(in.ListID)+"/task", q, nil, &out); err != nil {
		return nil, nil, err
	}
	tasks := make([]cuTask, 0, len(out.Tasks))
	for _, t := range out.Tasks {
		tasks = append(tasks, slimTask(t))
	}
	return textResult(map[string]any{"tasks": tasks})
}

type cuSearchTasksIn struct {
	WorkspaceID   string   `json:"workspace_id" jsonschema:"workspace (team) ID"`
	AssigneeIDs   []string `json:"assignee_ids,omitempty" jsonschema:"only tasks assigned to these user IDs"`
	Statuses      []string `json:"statuses,omitempty" jsonschema:"only these statuses"`
	IncludeClosed bool     `json:"include_closed,omitempty" jsonschema:"include closed tasks"`
	UpdatedAfter  string   `json:"updated_after,omitempty" jsonschema:"unix ms timestamp; only tasks updated after this"`
	Page          int      `json:"page,omitempty" jsonschema:"page number (0-based)"`
}

func (c *Client) clickupSearchTasks(ctx context.Context, _ *mcp.CallToolRequest, in cuSearchTasksIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	for _, a := range in.AssigneeIDs {
		q.Add("assignees[]", a)
	}
	for _, s := range in.Statuses {
		q.Add("statuses[]", s)
	}
	if in.IncludeClosed {
		q.Set("include_closed", "true")
	}
	if in.UpdatedAfter != "" {
		q.Set("date_updated_gt", in.UpdatedAfter)
	}
	if in.Page > 0 {
		q.Set("page", strconv.Itoa(in.Page))
	}
	var out struct {
		Tasks []rawCUTask `json:"tasks"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/team/"+url.PathEscape(in.WorkspaceID)+"/task", q, nil, &out); err != nil {
		return nil, nil, err
	}
	tasks := make([]cuTask, 0, len(out.Tasks))
	for _, t := range out.Tasks {
		tasks = append(tasks, slimTask(t))
	}
	return textResult(map[string]any{"tasks": tasks})
}

type cuTaskIn struct {
	TaskID string `json:"task_id" jsonschema:"task ID (e.g. 86c2j3k4m or custom ID)"`
}

func (c *Client) clickupGetTask(ctx context.Context, _ *mcp.CallToolRequest, in cuTaskIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		rawCUTask
		Description    string `json:"description"`
		DateCreated    string `json:"date_created"`
		DateUpdated    string `json:"date_updated"`
		Parent         string `json:"parent"`
		TopLevelParent string `json:"top_level_parent"`
		Creator        struct {
			Username string `json:"username"`
		} `json:"creator"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/task/"+url.PathEscape(in.TaskID), nil, nil, &out); err != nil {
		return nil, nil, err
	}
	res := map[string]any{
		"task":         slimTask(out.rawCUTask),
		"description":  out.Description,
		"date_created": out.DateCreated,
		"date_updated": out.DateUpdated,
		"creator":      out.Creator.Username,
	}
	if out.Parent != "" {
		res["parent"] = out.Parent
	}
	// top_level_parent comes back in the same GET /task response (no extra
	// request); surface it so callers can jump to the top of a subtask
	// hierarchy in one hop instead of walking up via repeated get_task.
	if out.TopLevelParent != "" {
		res["top_level_parent"] = out.TopLevelParent
	}
	return textResult(res)
}

type cuCreateTaskIn struct {
	ListID      string   `json:"list_id" jsonschema:"list to create the task in"`
	Name        string   `json:"name" jsonschema:"task name"`
	Description string   `json:"description,omitempty" jsonschema:"task description (markdown)"`
	Status      string   `json:"status,omitempty" jsonschema:"initial status"`
	Priority    int      `json:"priority,omitempty" jsonschema:"priority level: 1 urgent, 2 high, 3 normal, 4 low"`
	Assignees   []int    `json:"assignees,omitempty" jsonschema:"assignee user IDs"`
	DueDate     int64    `json:"due_date,omitempty" jsonschema:"due date as unix ms timestamp"`
	Tags        []string `json:"tags,omitempty" jsonschema:"tag names"`
	Parent      string   `json:"parent,omitempty" jsonschema:"parent task ID (creates a subtask)"`
}

func (c *Client) clickupCreateTask(ctx context.Context, _ *mcp.CallToolRequest, in cuCreateTaskIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"name": in.Name}
	if in.Description != "" {
		body["markdown_description"] = in.Description
	}
	if in.Status != "" {
		body["status"] = in.Status
	}
	if in.Priority != 0 {
		body["priority"] = in.Priority
	}
	if len(in.Assignees) > 0 {
		body["assignees"] = in.Assignees
	}
	if in.DueDate != 0 {
		body["due_date"] = in.DueDate
	}
	if len(in.Tags) > 0 {
		body["tags"] = in.Tags
	}
	if in.Parent != "" {
		body["parent"] = in.Parent
	}
	var out rawCUTask
	if err := c.clickup(ctx, http.MethodPost, "/list/"+url.PathEscape(in.ListID)+"/task", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"created": slimTask(out)})
}

type cuUpdateTaskIn struct {
	TaskID      string `json:"task_id" jsonschema:"task to update"`
	Name        string `json:"name,omitempty" jsonschema:"new name"`
	Description string `json:"description,omitempty" jsonschema:"new description (markdown)"`
	Status      string `json:"status,omitempty" jsonschema:"new status"`
	Priority    int    `json:"priority,omitempty" jsonschema:"priority level: 1 urgent, 2 high, 3 normal, 4 low"`
	DueDate     int64  `json:"due_date,omitempty" jsonschema:"new due date as unix ms timestamp"`
}

func (c *Client) clickupUpdateTask(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateTaskIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.Description != "" {
		body["markdown_description"] = in.Description
	}
	if in.Status != "" {
		body["status"] = in.Status
	}
	if in.Priority != 0 {
		body["priority"] = in.Priority
	}
	if in.DueDate != 0 {
		body["due_date"] = in.DueDate
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update — provide at least one field")
	}
	var out rawCUTask
	if err := c.clickup(ctx, http.MethodPut, "/task/"+url.PathEscape(in.TaskID), nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"updated": slimTask(out)})
}

// --- comments -----------------------------------------------------------

// commentBody builds the request body for create/update comment. With no
// mentions it sends the flat comment_text (unchanged behaviour). With mentions
// it sends ClickUp's structured `comment` array — the text followed by one
// `type:tag` block per user id — so each tag becomes a live @mention that
// notifies the user instead of staying plain text.
//
// NOTE: ClickUp documents the `comment` array for CREATE (POST
// /task/{id}/comment). Its acceptance on UPDATE (PUT /comment/{id}) and the
// actual notification firing are not documented and are unverified against a
// live workspace.
func commentBody(text string, mentions []int) map[string]any {
	if len(mentions) == 0 {
		return map[string]any{"comment_text": text}
	}
	blocks := make([]map[string]any, 0, 1+2*len(mentions))
	if text != "" {
		blocks = append(blocks, map[string]any{"text": text})
	}
	for _, uid := range mentions {
		blocks = append(blocks,
			map[string]any{"text": " "},
			map[string]any{"type": "tag", "user": map[string]any{"id": uid}},
		)
	}
	return map[string]any{"comment": blocks}
}

type cuAddCommentIn struct {
	TaskID   string `json:"task_id" jsonschema:"task to comment on"`
	Comment  string `json:"comment" jsonschema:"comment text"`
	Mentions []int  `json:"mentions,omitempty" jsonschema:"ClickUp user IDs to @mention as live tags that notify them; get IDs from clickup_list_members"`
}

func (c *Client) clickupAddComment(ctx context.Context, _ *mcp.CallToolRequest, in cuAddCommentIn) (*mcp.CallToolResult, any, error) {
	body := commentBody(in.Comment, in.Mentions)
	var out struct {
		ID any `json:"id"`
	}
	if err := c.clickup(ctx, http.MethodPost, "/task/"+url.PathEscape(in.TaskID)+"/comment", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"comment_id": out.ID, "status": "posted"})
}

func (c *Client) clickupListComments(ctx context.Context, _ *mcp.CallToolRequest, in cuTaskIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		Comments []struct {
			ID          string `json:"id"`
			CommentText string `json:"comment_text"`
			User        struct {
				Username string `json:"username"`
			} `json:"user"`
			Date string `json:"date"`
		} `json:"comments"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/task/"+url.PathEscape(in.TaskID)+"/comment", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	type comment struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		By   string `json:"by"`
		Date string `json:"date"`
	}
	comments := make([]comment, 0, len(out.Comments))
	for _, cm := range out.Comments {
		comments = append(comments, comment{ID: cm.ID, Text: cm.CommentText, By: cm.User.Username, Date: cm.Date})
	}
	return textResult(map[string]any{"comments": comments})
}

type cuDeleteCommentIn struct {
	CommentID string `json:"comment_id" jsonschema:"comment ID (from clickup_list_comments or clickup_add_comment)"`
}

func (c *Client) clickupDeleteComment(ctx context.Context, _ *mcp.CallToolRequest, in cuDeleteCommentIn) (*mcp.CallToolResult, any, error) {
	if err := c.clickup(ctx, http.MethodDelete, "/comment/"+url.PathEscape(in.CommentID), nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"comment_id": in.CommentID, "status": "deleted"})
}

type cuUpdateCommentIn struct {
	CommentID string `json:"comment_id" jsonschema:"comment ID (from clickup_list_comments or clickup_add_comment)"`
	Comment   string `json:"comment" jsonschema:"new comment text"`
	Mentions  []int  `json:"mentions,omitempty" jsonschema:"ClickUp user IDs to @mention as live tags that notify them; get IDs from clickup_list_members"`
}

// clickupUpdateComment edits a comment's text in place via PUT /comment/{id},
// so the comment keeps its id and thread position (unlike delete + re-add).
func (c *Client) clickupUpdateComment(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateCommentIn) (*mcp.CallToolResult, any, error) {
	body := commentBody(in.Comment, in.Mentions)
	if err := c.clickup(ctx, http.MethodPut, "/comment/"+url.PathEscape(in.CommentID), nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"comment_id": in.CommentID, "status": "updated"})
}

// --- docs (API v3) --------------------------------------------------------
// A ClickUp Doc is a container; the CONTENT lives in its pages. URL shape:
// app.clickup.com/<workspace>/v/dc/<doc_id>/<page_id>.

type cuListDocsIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"pagination cursor from a previous call"`
}

func (c *Client) clickupListDocs(ctx context.Context, _ *mcp.CallToolRequest, in cuListDocsIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	if in.Cursor != "" {
		q.Set("next_cursor", in.Cursor)
	}
	var out struct {
		Docs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"docs"`
		NextCursor string `json:"next_cursor"`
	}
	if err := c.clickupV3(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs", q, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"docs": out.Docs, "next_cursor": out.NextCursor})
}

type cuDocIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	DocID       string `json:"doc_id" jsonschema:"doc ID (the dc/<id> part of the doc URL)"`
}

// clickupGetDoc lists the doc's pages WITH markdown content.
func (c *Client) clickupGetDoc(ctx context.Context, _ *mcp.CallToolRequest, in cuDocIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{"content_format": {"text/md"}}
	var pages []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := c.clickupV3(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs/"+url.PathEscape(in.DocID)+"/pages", q, nil, &pages); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"pages": pages})
}

type cuCreateDocIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	Name        string `json:"name" jsonschema:"doc name"`
}

func (c *Client) clickupCreateDoc(ctx context.Context, _ *mcp.CallToolRequest, in cuCreateDocIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.clickupV3(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs", nil,
		map[string]any{"name": in.Name}, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"created_doc": out})
}

type cuCreatePageIn struct {
	WorkspaceID  string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	DocID        string `json:"doc_id" jsonschema:"doc to add the page to"`
	Name         string `json:"name" jsonschema:"page title"`
	Content      string `json:"content" jsonschema:"page content (markdown)"`
	ParentPageID string `json:"parent_page_id,omitempty" jsonschema:"nest under this page"`
}

func (c *Client) clickupCreatePage(ctx context.Context, _ *mcp.CallToolRequest, in cuCreatePageIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{
		"name":           in.Name,
		"content":        in.Content,
		"content_format": "text/md",
	}
	if in.ParentPageID != "" {
		body["parent_page_id"] = in.ParentPageID
	}
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.clickupV3(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs/"+url.PathEscape(in.DocID)+"/pages", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"created_page": out})
}

type cuUpdatePageIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	DocID       string `json:"doc_id" jsonschema:"doc ID (dc/<id> in the URL)"`
	PageID      string `json:"page_id" jsonschema:"page ID (last segment of the page URL)"`
	Name        string `json:"name,omitempty" jsonschema:"new page title"`
	Content     string `json:"content,omitempty" jsonschema:"page content (markdown)"`
	EditMode    string `json:"content_edit_mode,omitempty" jsonschema:"replace (default), append, or prepend"`
}

func (c *Client) clickupUpdatePage(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdatePageIn) (*mcp.CallToolResult, any, error) {
	if in.Name == "" && in.Content == "" {
		return nil, nil, fmt.Errorf("nothing to update — provide name and/or content")
	}
	body := map[string]any{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.Content != "" {
		mode := in.EditMode
		if mode == "" {
			mode = "replace"
		}
		body["content"] = in.Content
		body["content_format"] = "text/md"
		body["content_edit_mode"] = mode
	}
	if err := c.clickupV3(ctx, http.MethodPut, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs/"+url.PathEscape(in.DocID)+"/pages/"+url.PathEscape(in.PageID), nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"page_id": in.PageID, "status": "updated"})
}

type cuGetPageIn struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace (team) ID"`
	DocID       string `json:"doc_id" jsonschema:"doc ID"`
	PageID      string `json:"page_id" jsonschema:"page ID"`
}

func (c *Client) clickupGetPage(ctx context.Context, _ *mcp.CallToolRequest, in cuGetPageIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{"content_format": {"text/md"}}
	var out struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := c.clickupV3(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(in.WorkspaceID)+"/docs/"+url.PathEscape(in.DocID)+"/pages/"+url.PathEscape(in.PageID), q, nil, &out); err != nil {
		return nil, nil, err
	}
	return textResult(out)
}
