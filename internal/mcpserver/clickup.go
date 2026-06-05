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
		Description string `json:"description"`
		DateCreated string `json:"date_created"`
		DateUpdated string `json:"date_updated"`
		Parent      string `json:"parent"`
		Creator     struct {
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

type cuAddCommentIn struct {
	TaskID  string `json:"task_id" jsonschema:"task to comment on"`
	Comment string `json:"comment" jsonschema:"comment text"`
}

func (c *Client) clickupAddComment(ctx context.Context, _ *mcp.CallToolRequest, in cuAddCommentIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"comment_text": in.Comment}
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
