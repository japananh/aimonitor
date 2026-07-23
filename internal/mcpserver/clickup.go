package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cuTask is the slimmed task shape returned by list/search tools. ClickUp's
// raw task payloads are enormous (custom fields, watchers, checklists);
// keep the fields needed to identify, triage, and link.
type cuTask struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Status         string   `json:"status,omitempty"`
	Assignees      []string `json:"assignees,omitempty"`
	AssigneeIDs    []int    `json:"assignee_ids,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	DueDate        string   `json:"due_date,omitempty"`
	List           string   `json:"list,omitempty"`
	ListID         string   `json:"list_id,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Parent         string   `json:"parent,omitempty"`
	TopLevelParent string   `json:"top_level_parent,omitempty"`
	URL            string   `json:"url,omitempty"`
}

// cuAttachment is the slimmed shape for a task attachment. url is the variant
// that downloads the bytes without a separate auth dance (see slimAttachment).
type cuAttachment struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	Mimetype  string `json:"mimetype,omitempty"`
	Extension string `json:"extension,omitempty"`
	Date      string `json:"date,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

type rawCUTask struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status struct {
		Status string `json:"status"`
	} `json:"status"`
	Assignees []struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	} `json:"assignees"`
	Priority *struct {
		Priority string `json:"priority"`
	} `json:"priority"`
	DueDate string `json:"due_date"`
	List    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"list"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Parent         string `json:"parent"`
	TopLevelParent string `json:"top_level_parent"`
	URL            string `json:"url"`
}

func slimTask(t rawCUTask) cuTask {
	out := cuTask{
		ID: t.ID, Name: t.Name, Status: t.Status.Status,
		DueDate: t.DueDate, List: t.List.Name, ListID: t.List.ID,
		Parent: t.Parent, TopLevelParent: t.TopLevelParent, URL: t.URL,
	}
	for _, a := range t.Assignees {
		out.Assignees = append(out.Assignees, a.Username)
		out.AssigneeIDs = append(out.AssigneeIDs, a.ID)
	}
	for _, tag := range t.Tags {
		out.Tags = append(out.Tags, tag.Name)
	}
	if t.Priority != nil {
		out.Priority = t.Priority.Priority
	}
	return out
}

// rawCUAttachment is ClickUp's attachment object as embedded in a GET
// /task/{id} response. It carries three URL variants; mimetype is the v2 field
// (mime_type appears on the newer v3 attachments endpoint) — we read both and
// coalesce so the shape is robust across ClickUp API surfaces.
type rawCUAttachment struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Extension    string `json:"extension"`
	Mimetype     string `json:"mimetype"`
	MimeType     string `json:"mime_type"`
	Date         string `json:"date"`
	Size         int64  `json:"size"`
	URL          string `json:"url"`
	URLWithQuery string `json:"url_w_query"`
	URLWithHost  string `json:"url_w_host"`
}

// slimAttachment picks the directly-downloadable URL and coalesces the mimetype
// field. url_w_query carries a signed query string that fetches the bytes
// without auth; plain url can 401 on a private workspace, so prefer the signed
// variant and fall back only if it's absent.
func slimAttachment(a rawCUAttachment) cuAttachment {
	dl := a.URLWithQuery
	if dl == "" {
		dl = a.URL
	}
	if dl == "" {
		dl = a.URLWithHost
	}
	mime := a.Mimetype
	if mime == "" {
		mime = a.MimeType
	}
	return cuAttachment{
		ID:        a.ID,
		Title:     a.Title,
		URL:       dl,
		Mimetype:  mime,
		Extension: a.Extension,
		Date:      a.Date,
		Size:      a.Size,
	}
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

// cuListMembersIn selects the member source for clickup_list_members. Prefer
// task_id: ClickUp's GET /team omits the members array for large workspaces (it
// comes back empty), so the workspace path can't resolve user IDs there —
// GET /task/{id}/member does, and it's the task you'd be @mentioning on anyway.
type cuListMembersIn struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"workspace (team) ID — lists workspace members via GET /team. ClickUp omits members from /team for large workspaces (returns an empty list); pass task_id then."`
	TaskID      string `json:"task_id,omitempty" jsonschema:"a task ID — lists the members with access to this task (GET /task/{id}/member), each as {id, username, email}. Use this to resolve user IDs for @mentions when the workspace member list is empty; pass the task you're commenting on."`
}

func (c *Client) clickupListMembers(ctx context.Context, _ *mcp.CallToolRequest, in cuListMembersIn) (*mcp.CallToolResult, any, error) {
	type member struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	// Stable empty slice, never nil — a nil slice marshals to JSON null, which
	// is what made an empty result read as {"members": null} (issue #60).
	members := []member{}

	// Task scope: /task/{id}/member returns members flat ({id,username,email})
	// and works even when GET /team omits them for a large workspace.
	if in.TaskID != "" {
		var out struct {
			Members []member `json:"members"`
		}
		if err := c.clickup(ctx, http.MethodGet, "/task/"+url.PathEscape(in.TaskID)+"/member", nil, nil, &out); err != nil {
			return nil, nil, err
		}
		members = append(members, out.Members...)
		return textResult(map[string]any{"members": members})
	}

	if in.WorkspaceID == "" {
		return nil, nil, fmt.Errorf("provide workspace_id or task_id")
	}

	// Workspace scope: members ride on GET /team under teams[].members[].user.
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
	for _, t := range out.Teams {
		if t.ID != in.WorkspaceID {
			continue
		}
		for _, m := range t.Members {
			members = append(members, member(m.User))
		}
	}
	res := map[string]any{"members": members}
	if len(members) == 0 {
		// Empty can mean a wrong workspace_id (no team matched) OR ClickUp
		// omitting members from /team for a large workspace — don't assert which.
		// Point the caller at the task path either way.
		res["note"] = "No members came back for this workspace_id. If the id is correct, ClickUp likely omitted them (its GET /team drops members for large workspaces, and the endpoint can be flaky) — call clickup_list_members again with task_id set to the task you're working with to resolve a user ID (e.g. for a clickup_add_comment mention)."
	}
	return textResult(res)
}

// --- tasks --------------------------------------------------------------

type cuListTasksIn struct {
	ListID          string   `json:"list_id" jsonschema:"list ID"`
	Statuses        []string `json:"statuses,omitempty" jsonschema:"only these statuses"`
	IncludeClosed   bool     `json:"include_closed,omitempty" jsonschema:"include closed tasks"`
	IncludeSubtasks bool     `json:"include_subtasks,omitempty" jsonschema:"also return subtasks (ClickUp omits them by default)"`
	Page            int      `json:"page,omitempty" jsonschema:"page number (0-based; 100 tasks per page)"`
}

func (c *Client) clickupListTasks(ctx context.Context, _ *mcp.CallToolRequest, in cuListTasksIn) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	for _, s := range in.Statuses {
		q.Add("statuses[]", s)
	}
	if in.IncludeClosed {
		q.Set("include_closed", "true")
	}
	if in.IncludeSubtasks {
		q.Set("subtasks", "true")
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
	WorkspaceID     string   `json:"workspace_id" jsonschema:"workspace (team) ID"`
	AssigneeIDs     []string `json:"assignee_ids,omitempty" jsonschema:"only tasks assigned to these user IDs"`
	Statuses        []string `json:"statuses,omitempty" jsonschema:"only these statuses"`
	IncludeClosed   bool     `json:"include_closed,omitempty" jsonschema:"include closed tasks"`
	IncludeSubtasks bool     `json:"include_subtasks,omitempty" jsonschema:"also return subtasks (ClickUp omits them by default)"`
	UpdatedAfter    string   `json:"updated_after,omitempty" jsonschema:"unix ms timestamp; only tasks updated after this"`
	Page            int      `json:"page,omitempty" jsonschema:"page number (0-based)"`
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
	if in.IncludeSubtasks {
		q.Set("subtasks", "true")
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

// cuTaskIn carries the task id and an opt-out for the subtask tree. The flag is
// a *bool so the default is "include" (nil) while an explicit false still keeps
// the payload small — a plain bool couldn't tell "omitted" from "set false".
type cuTaskIn struct {
	TaskID          string `json:"task_id" jsonschema:"task ID (e.g. 86c2j3k4m or custom ID)"`
	IncludeSubtasks *bool  `json:"include_subtasks,omitempty" jsonschema:"include the task's subtasks and sub-subtasks (flattened descendant tree, each carrying parent / top_level_parent); default true"`
}

func (c *Client) clickupGetTask(ctx context.Context, _ *mcp.CallToolRequest, in cuTaskIn) (*mcp.CallToolResult, any, error) {
	// Default to including the tree; only an explicit false opts out.
	includeSubtasks := in.IncludeSubtasks == nil || *in.IncludeSubtasks
	q := url.Values{}
	if includeSubtasks {
		// ClickUp only returns the `subtasks` array on GET /task/{id} when this
		// is set; it returns every descendant flattened, not just direct kids.
		q.Set("include_subtasks", "true")
	}
	var out struct {
		rawCUTask
		Description string            `json:"description"`
		DateCreated string            `json:"date_created"`
		DateUpdated string            `json:"date_updated"`
		Subtasks    []rawCUTask       `json:"subtasks"`
		Attachments []rawCUAttachment `json:"attachments"`
		Creator     struct {
			Username string `json:"username"`
		} `json:"creator"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/task/"+url.PathEscape(in.TaskID), q, nil, &out); err != nil {
		return nil, nil, err
	}
	// parent / top_level_parent ride on the slim task now (so list_tasks and
	// search_tasks expose them too), all from this one GET — no extra request.
	res := map[string]any{
		"task":         slimTask(out.rawCUTask),
		"description":  out.Description,
		"date_created": out.DateCreated,
		"date_updated": out.DateUpdated,
		"creator":      out.Creator.Username,
	}
	// ClickUp returns `attachments` by default (no include flag needed). Always
	// set the key — an empty slice marshals as [] so callers can tell "none"
	// from a shape that never carried attachments.
	attachments := make([]cuAttachment, 0, len(out.Attachments))
	for _, a := range out.Attachments {
		attachments = append(attachments, slimAttachment(a))
	}
	res["attachments"] = attachments
	// Slim the descendants with the same shape as list/search; each carries
	// parent / top_level_parent so the caller can rebuild the nesting. Only set
	// the key when asked, so callers can tell "none" from "didn't request".
	if includeSubtasks {
		subtasks := make([]cuTask, 0, len(out.Subtasks))
		for _, t := range out.Subtasks {
			subtasks = append(subtasks, slimTask(t))
		}
		res["subtasks"] = subtasks
	}
	return textResult(res)
}

type cuCreateTaskIn struct {
	ListID       string   `json:"list_id" jsonschema:"list to create the task in"`
	Name         string   `json:"name" jsonschema:"task name"`
	Description  string   `json:"description,omitempty" jsonschema:"task description (markdown)"`
	Status       string   `json:"status,omitempty" jsonschema:"initial status"`
	Priority     int      `json:"priority,omitempty" jsonschema:"priority level: 1 urgent, 2 high, 3 normal, 4 low"`
	Assignees    []int    `json:"assignees,omitempty" jsonschema:"assignee user IDs"`
	DueDate      int64    `json:"due_date,omitempty" jsonschema:"due date as unix ms timestamp"`
	Tags         []string `json:"tags,omitempty" jsonschema:"tag names"`
	Parent       string   `json:"parent,omitempty" jsonschema:"parent task ID (creates a subtask)"`
	CustomItemID *int     `json:"custom_item_id,omitempty" jsonschema:"work-item type ID (e.g. ClickUp's Bug type); resolve names to IDs with clickup_list_custom_item_types. Omit for the default Task type"`
}

// setMarkdownDescription writes a markdown description onto a create/update body
// under both markdown fields. ClickUp's older markdown_description does not emit
// link marks — it drops [label](url) (keeping only the label) and leaves bare
// URLs as plain text (#102). Its newer markdown_content parses markdown into real
// rich-text link marks (and auto-linkifies bare URLs). We send both: where
// ClickUp honours markdown_content it renders clickable links, and
// markdown_description stays as a no-regression fallback if it doesn't. Verified
// only at the request-body level here; the rendering itself is ClickUp's
// documented markdown_content behaviour, not checked against a live workspace.
func setMarkdownDescription(body map[string]any, desc string) {
	body["markdown_content"] = desc
	body["markdown_description"] = desc
}

// createTaskBody maps the create-task input onto ClickUp's POST /list/{id}/task
// body. custom_item_id is sent whenever provided (including 0, a valid type ID),
// which is why it's a pointer rather than guarded on != 0.
func createTaskBody(in cuCreateTaskIn) map[string]any {
	body := map[string]any{"name": in.Name}
	if in.Description != "" {
		setMarkdownDescription(body, in.Description)
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
	if in.CustomItemID != nil {
		body["custom_item_id"] = *in.CustomItemID
	}
	return body
}

func (c *Client) clickupCreateTask(ctx context.Context, _ *mcp.CallToolRequest, in cuCreateTaskIn) (*mcp.CallToolResult, any, error) {
	body := createTaskBody(in)
	var out rawCUTask
	if err := c.clickup(ctx, http.MethodPost, "/list/"+url.PathEscape(in.ListID)+"/task", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"created": slimTask(out)})
}

type cuUpdateTaskIn struct {
	TaskID          string `json:"task_id" jsonschema:"task to update"`
	Name            string `json:"name,omitempty" jsonschema:"new name"`
	Description     string `json:"description,omitempty" jsonschema:"new description (markdown)"`
	Status          string `json:"status,omitempty" jsonschema:"new status"`
	Priority        int    `json:"priority,omitempty" jsonschema:"priority level: 1 urgent, 2 high, 3 normal, 4 low"`
	DueDate         int64  `json:"due_date,omitempty" jsonschema:"new due date as unix ms timestamp"`
	AddAssignees    []int  `json:"add_assignees,omitempty" jsonschema:"user IDs to assign to the task (resolve with clickup_list_members)"`
	RemoveAssignees []int  `json:"remove_assignees,omitempty" jsonschema:"user IDs to unassign from the task"`
	CustomItemID    *int   `json:"custom_item_id,omitempty" jsonschema:"work-item type ID (e.g. ClickUp's Bug type); resolve names to IDs with clickup_list_custom_item_types"`
}

// updateTaskBody maps the update-task input onto ClickUp's PUT /task/{id} body.
// Assignees ride on the ClickUp add/remove shape ({"assignees":{"add":[…],
// "rem":[…]}}), unlike create-task's plain array. custom_item_id is a pointer so
// 0 (a valid type ID) can be sent explicitly and an absent field stays omitted.
func updateTaskBody(in cuUpdateTaskIn) (map[string]any, error) {
	body := map[string]any{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.Description != "" {
		setMarkdownDescription(body, in.Description)
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
	if in.CustomItemID != nil {
		body["custom_item_id"] = *in.CustomItemID
	}
	if len(in.AddAssignees) > 0 || len(in.RemoveAssignees) > 0 {
		a := map[string]any{}
		if len(in.AddAssignees) > 0 {
			a["add"] = in.AddAssignees
		}
		if len(in.RemoveAssignees) > 0 {
			a["rem"] = in.RemoveAssignees
		}
		body["assignees"] = a
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("nothing to update — provide at least one field")
	}
	return body, nil
}

func (c *Client) clickupUpdateTask(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateTaskIn) (*mcp.CallToolResult, any, error) {
	body, err := updateTaskBody(in)
	if err != nil {
		return nil, nil, err
	}
	var out rawCUTask
	if err := c.clickup(ctx, http.MethodPut, "/task/"+url.PathEscape(in.TaskID), nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"updated": slimTask(out)})
}

type cuDeleteTaskIn struct {
	TaskIDs []string `json:"task_ids" jsonschema:"task IDs to delete (each moved to the Trash, recoverable); pass a single-element list to delete one task"`
}

// cuDeleteFailure records one task that couldn't be deleted, so the caller can
// retry just the failures rather than resend the whole batch.
type cuDeleteFailure struct {
	TaskID string `json:"task_id"`
	Error  string `json:"error"`
}

// clickupDeleteTask deletes each task via DELETE /task/{id}. ClickUp has no bulk
// endpoint, so it loops; one failed delete is recorded and the rest continue,
// and the result reports deleted vs failed ids. Deleted tasks go to the Trash
// (recoverable), not a hard delete.
func (c *Client) clickupDeleteTask(ctx context.Context, _ *mcp.CallToolRequest, in cuDeleteTaskIn) (*mcp.CallToolResult, any, error) {
	if len(in.TaskIDs) == 0 {
		return nil, nil, fmt.Errorf("task_ids is required (at least one task ID)")
	}
	deleted := make([]string, 0, len(in.TaskIDs))
	failed := make([]cuDeleteFailure, 0)
	for _, id := range in.TaskIDs {
		if id == "" {
			failed = append(failed, cuDeleteFailure{TaskID: id, Error: "empty task_id"})
			continue
		}
		if err := c.clickup(ctx, http.MethodDelete, "/task/"+url.PathEscape(id), nil, nil, nil); err != nil {
			failed = append(failed, cuDeleteFailure{TaskID: id, Error: err.Error()})
			continue
		}
		deleted = append(deleted, id)
	}
	return textResult(map[string]any{"deleted": deleted, "failed": failed})
}

type cuTagIn struct {
	TaskID string `json:"task_id" jsonschema:"task to tag"`
	Tag    string `json:"tag" jsonschema:"tag name (must already exist in the Space; create it in the ClickUp UI first)"`
}

// clickupAddTag adds an existing Space tag to a task via
// POST /task/{id}/tag/{tag_name}. ClickUp's PUT /task can't mutate tags, so this
// (and clickupRemoveTag) is the only way to (re)tag an existing task.
func (c *Client) clickupAddTag(ctx context.Context, _ *mcp.CallToolRequest, in cuTagIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.Tag == "" {
		return nil, nil, fmt.Errorf("task_id and tag are required")
	}
	path := "/task/" + url.PathEscape(in.TaskID) + "/tag/" + url.PathEscape(in.Tag)
	if err := c.clickup(ctx, http.MethodPost, path, nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, "tag": in.Tag, "status": "added"})
}

// clickupRemoveTag removes a tag from a task via DELETE /task/{id}/tag/{tag_name}.
func (c *Client) clickupRemoveTag(ctx context.Context, _ *mcp.CallToolRequest, in cuTagIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.Tag == "" {
		return nil, nil, fmt.Errorf("task_id and tag are required")
	}
	path := "/task/" + url.PathEscape(in.TaskID) + "/tag/" + url.PathEscape(in.Tag)
	if err := c.clickup(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, "tag": in.Tag, "status": "removed"})
}

// cuCustomItemType is the slimmed work-item type shape. id is what
// clickup_create_task / clickup_update_task want for custom_item_id; the default
// Task type (custom_item_id null/omitted) is not returned by this endpoint.
type cuCustomItemType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) clickupListCustomItemTypes(ctx context.Context, _ *mcp.CallToolRequest, in cuWorkspaceIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		CustomItems []cuCustomItemType `json:"custom_items"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/team/"+url.PathEscape(in.WorkspaceID)+"/custom_item", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	// Stable empty slice, never nil — a nil slice marshals to JSON null.
	types := make([]cuCustomItemType, 0, len(out.CustomItems))
	types = append(types, out.CustomItems...)
	return textResult(map[string]any{"custom_item_types": types})
}

// --- custom fields ------------------------------------------------------

// cuCustomField is the slimmed accessible-custom-field shape. id is the UUID
// clickup_set_custom_field / clickup_remove_custom_field want; type names the
// field kind (text, number, drop_down, labels, date, …) so the caller can shape
// the value it sends, and type_config carries the option UUIDs for
// drop_down/labels fields.
type cuCustomField struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	TypeConfig any    `json:"type_config,omitempty"`
}

type cuListFieldsIn struct {
	ListID string `json:"list_id" jsonschema:"list ID whose accessible custom fields to list"`
}

// clickupListCustomFields lists the custom fields available on a list via
// GET /list/{id}/field, so the caller can resolve a field name to the UUID that
// clickup_set_custom_field / clickup_remove_custom_field want (and read the
// option UUIDs for drop_down/labels fields from type_config).
func (c *Client) clickupListCustomFields(ctx context.Context, _ *mcp.CallToolRequest, in cuListFieldsIn) (*mcp.CallToolResult, any, error) {
	if in.ListID == "" {
		return nil, nil, fmt.Errorf("list_id is required")
	}
	var out struct {
		Fields []cuCustomField `json:"fields"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/list/"+url.PathEscape(in.ListID)+"/field", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	// Stable empty slice, never nil — a nil slice marshals to JSON null.
	fields := make([]cuCustomField, 0, len(out.Fields))
	fields = append(fields, out.Fields...)
	return textResult(map[string]any{"custom_fields": fields})
}

type cuSetCustomFieldIn struct {
	TaskID       string         `json:"task_id" jsonschema:"task to set the custom field value on"`
	FieldID      string         `json:"field_id" jsonschema:"custom field UUID (from clickup_list_custom_fields)"`
	Value        any            `json:"value" jsonschema:"the value, shaped for the field type: a string for text, a number for number, an option UUID for drop_down, an array of option UUIDs for labels, a unix ms timestamp for date, etc."`
	ValueOptions map[string]any `json:"value_options,omitempty" jsonschema:"optional ClickUp value_options (e.g. {\"time\": true} to include a time component on a date field)"`
}

// setCustomFieldBody maps the input onto ClickUp's POST /task/{id}/field/{id}
// body ({value, value_options?}). value is polymorphic by field type, so it's
// forwarded verbatim.
func setCustomFieldBody(in cuSetCustomFieldIn) map[string]any {
	body := map[string]any{"value": in.Value}
	if len(in.ValueOptions) > 0 {
		body["value_options"] = in.ValueOptions
	}
	return body
}

func (c *Client) clickupSetCustomField(ctx context.Context, _ *mcp.CallToolRequest, in cuSetCustomFieldIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.FieldID == "" {
		return nil, nil, fmt.Errorf("task_id and field_id are required")
	}
	if in.Value == nil {
		return nil, nil, fmt.Errorf("value is required (to clear a field, use clickup_remove_custom_field)")
	}
	body := setCustomFieldBody(in)
	if err := c.clickup(ctx, http.MethodPost, "/task/"+url.PathEscape(in.TaskID)+"/field/"+url.PathEscape(in.FieldID), nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, "field_id": in.FieldID, "status": "set"})
}

type cuRemoveCustomFieldIn struct {
	TaskID  string `json:"task_id" jsonschema:"task to clear the custom field value on"`
	FieldID string `json:"field_id" jsonschema:"custom field UUID (from clickup_list_custom_fields)"`
}

// clickupRemoveCustomField clears a task's value for one custom field via
// DELETE /task/{id}/field/{field_id}.
func (c *Client) clickupRemoveCustomField(ctx context.Context, _ *mcp.CallToolRequest, in cuRemoveCustomFieldIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.FieldID == "" {
		return nil, nil, fmt.Errorf("task_id and field_id are required")
	}
	if err := c.clickup(ctx, http.MethodDelete, "/task/"+url.PathEscape(in.TaskID)+"/field/"+url.PathEscape(in.FieldID), nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, "field_id": in.FieldID, "status": "removed"})
}

// --- dependencies -------------------------------------------------------

// cuDependencyIn identifies a directed dependency link on task_id. Exactly one
// of depends_on / dependency_of must be set — they encode opposite directions
// (task_id waits on depends_on; task_id blocks dependency_of), and ClickUp
// rejects a request carrying both or neither. ClickUp has no update endpoint for
// dependencies; re-point a link by deleting and re-adding.
type cuDependencyIn struct {
	TaskID       string `json:"task_id" jsonschema:"the task the dependency is on"`
	DependsOn    string `json:"depends_on,omitempty" jsonschema:"task ID that task_id waits on (is blocked by); provide this OR dependency_of, not both"`
	DependencyOf string `json:"dependency_of,omitempty" jsonschema:"task ID that task_id blocks (is a dependency of); provide this OR depends_on, not both"`
}

// dependencySide validates that exactly one direction is set and returns the
// ClickUp field name + value, shared by the add (body) and delete (query) paths.
func dependencySide(in cuDependencyIn) (key, val string, err error) {
	switch {
	case in.DependsOn != "" && in.DependencyOf != "":
		return "", "", fmt.Errorf("provide only one of depends_on or dependency_of, not both")
	case in.DependsOn != "":
		return "depends_on", in.DependsOn, nil
	case in.DependencyOf != "":
		return "dependency_of", in.DependencyOf, nil
	default:
		return "", "", fmt.Errorf("provide depends_on or dependency_of")
	}
}

// clickupAddDependency links a dependency via POST /task/{id}/dependency with a
// {depends_on|dependency_of} body.
func (c *Client) clickupAddDependency(ctx context.Context, _ *mcp.CallToolRequest, in cuDependencyIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" {
		return nil, nil, fmt.Errorf("task_id is required")
	}
	key, val, err := dependencySide(in)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{key: val}
	if err := c.clickup(ctx, http.MethodPost, "/task/"+url.PathEscape(in.TaskID)+"/dependency", nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, key: val, "status": "linked"})
}

// clickupDeleteDependency removes a dependency via DELETE /task/{id}/dependency,
// which takes the linked task on the QUERY string (depends_on / dependency_of),
// not in a body.
func (c *Client) clickupDeleteDependency(ctx context.Context, _ *mcp.CallToolRequest, in cuDependencyIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" {
		return nil, nil, fmt.Errorf("task_id is required")
	}
	key, val, err := dependencySide(in)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{key: {val}}
	if err := c.clickup(ctx, http.MethodDelete, "/task/"+url.PathEscape(in.TaskID)+"/dependency", q, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"task_id": in.TaskID, key: val, "status": "unlinked"})
}

// --- checklists ---------------------------------------------------------

// cuChecklistItem is the slimmed checklist-item shape. id is what
// clickup_update_checklist_item / clickup_delete_checklist_item want.
type cuChecklistItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Resolved bool   `json:"resolved"`
}

// cuChecklist is the slimmed checklist shape returned after a create — id is
// what clickup_update_checklist / clickup_delete_checklist and the item tools
// want; items carry their own ids for follow-up edits.
type cuChecklist struct {
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	Items []cuChecklistItem `json:"items"`
}

type rawCUChecklist struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Items []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Resolved bool   `json:"resolved"`
	} `json:"items"`
}

func slimChecklist(raw rawCUChecklist) cuChecklist {
	items := make([]cuChecklistItem, 0, len(raw.Items))
	for _, it := range raw.Items {
		items = append(items, cuChecklistItem{ID: it.ID, Name: it.Name, Resolved: it.Resolved})
	}
	return cuChecklist{ID: raw.ID, Name: raw.Name, Items: items}
}

type cuCreateChecklistIn struct {
	TaskID string `json:"task_id" jsonschema:"task to add the checklist to"`
	Name   string `json:"name" jsonschema:"checklist name"`
}

// clickupCreateChecklist adds a checklist to a task via POST /task/{id}/checklist.
func (c *Client) clickupCreateChecklist(ctx context.Context, _ *mcp.CallToolRequest, in cuCreateChecklistIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.Name == "" {
		return nil, nil, fmt.Errorf("task_id and name are required")
	}
	var out struct {
		Checklist rawCUChecklist `json:"checklist"`
	}
	if err := c.clickup(ctx, http.MethodPost, "/task/"+url.PathEscape(in.TaskID)+"/checklist", nil, map[string]any{"name": in.Name}, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"created_checklist": slimChecklist(out.Checklist)})
}

type cuUpdateChecklistIn struct {
	ChecklistID string `json:"checklist_id" jsonschema:"checklist ID (from clickup_create_checklist or clickup_get_task)"`
	Name        string `json:"name,omitempty" jsonschema:"new checklist name"`
	Position    *int   `json:"position,omitempty" jsonschema:"new position among the task's checklists (0-based)"`
}

// updateChecklistBody maps the input onto ClickUp's PUT /checklist/{id} body.
// Position is a pointer so 0 (a valid, first-position value) is distinct from
// "omitted".
func updateChecklistBody(in cuUpdateChecklistIn) (map[string]any, error) {
	body := map[string]any{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.Position != nil {
		body["position"] = *in.Position
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("nothing to update — provide name and/or position")
	}
	return body, nil
}

// clickupUpdateChecklist renames or repositions a checklist via PUT /checklist/{id}.
func (c *Client) clickupUpdateChecklist(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateChecklistIn) (*mcp.CallToolResult, any, error) {
	if in.ChecklistID == "" {
		return nil, nil, fmt.Errorf("checklist_id is required")
	}
	body, err := updateChecklistBody(in)
	if err != nil {
		return nil, nil, err
	}
	if err := c.clickup(ctx, http.MethodPut, "/checklist/"+url.PathEscape(in.ChecklistID), nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"checklist_id": in.ChecklistID, "status": "updated"})
}

type cuDeleteChecklistIn struct {
	ChecklistID string `json:"checklist_id" jsonschema:"checklist ID to delete (with all its items)"`
}

// clickupDeleteChecklist deletes a checklist (and its items) via DELETE /checklist/{id}.
func (c *Client) clickupDeleteChecklist(ctx context.Context, _ *mcp.CallToolRequest, in cuDeleteChecklistIn) (*mcp.CallToolResult, any, error) {
	if in.ChecklistID == "" {
		return nil, nil, fmt.Errorf("checklist_id is required")
	}
	if err := c.clickup(ctx, http.MethodDelete, "/checklist/"+url.PathEscape(in.ChecklistID), nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"checklist_id": in.ChecklistID, "status": "deleted"})
}

type cuCreateChecklistItemIn struct {
	ChecklistID string `json:"checklist_id" jsonschema:"checklist to add the item to"`
	Name        string `json:"name" jsonschema:"item text"`
	Assignee    *int   `json:"assignee,omitempty" jsonschema:"user ID to assign the item to (resolve with clickup_list_members)"`
}

// clickupCreateChecklistItem adds an item to a checklist via
// POST /checklist/{id}/checklist_item; the response carries the whole checklist,
// so the new item's id comes back in the returned items list.
func (c *Client) clickupCreateChecklistItem(ctx context.Context, _ *mcp.CallToolRequest, in cuCreateChecklistItemIn) (*mcp.CallToolResult, any, error) {
	if in.ChecklistID == "" || in.Name == "" {
		return nil, nil, fmt.Errorf("checklist_id and name are required")
	}
	body := map[string]any{"name": in.Name}
	if in.Assignee != nil {
		body["assignee"] = *in.Assignee
	}
	var out struct {
		Checklist rawCUChecklist `json:"checklist"`
	}
	if err := c.clickup(ctx, http.MethodPost, "/checklist/"+url.PathEscape(in.ChecklistID)+"/checklist_item", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"checklist": slimChecklist(out.Checklist)})
}

type cuUpdateChecklistItemIn struct {
	ChecklistID     string `json:"checklist_id" jsonschema:"parent checklist ID"`
	ChecklistItemID string `json:"checklist_item_id" jsonschema:"item ID (from clickup_create_checklist_item or clickup_get_task)"`
	Name            string `json:"name,omitempty" jsonschema:"new item text"`
	Assignee        *int   `json:"assignee,omitempty" jsonschema:"user ID to assign the item to (resolve with clickup_list_members)"`
	Resolved        *bool  `json:"resolved,omitempty" jsonschema:"mark the item done (true) or not done (false)"`
	Parent          string `json:"parent,omitempty" jsonschema:"nest this item under another checklist item ID (makes it a sub-item)"`
}

// updateChecklistItemBody maps the input onto ClickUp's
// PUT /checklist/{id}/checklist_item/{item} body. Assignee/Resolved are pointers
// so a valid zero/false is distinct from "omitted".
func updateChecklistItemBody(in cuUpdateChecklistItemIn) (map[string]any, error) {
	body := map[string]any{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.Assignee != nil {
		body["assignee"] = *in.Assignee
	}
	if in.Resolved != nil {
		body["resolved"] = *in.Resolved
	}
	if in.Parent != "" {
		body["parent"] = in.Parent
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("nothing to update — provide name, assignee, resolved, or parent")
	}
	return body, nil
}

// clickupUpdateChecklistItem edits a checklist item (rename, (un)resolve,
// (re)assign, or re-nest) via PUT /checklist/{id}/checklist_item/{item}.
func (c *Client) clickupUpdateChecklistItem(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateChecklistItemIn) (*mcp.CallToolResult, any, error) {
	if in.ChecklistID == "" || in.ChecklistItemID == "" {
		return nil, nil, fmt.Errorf("checklist_id and checklist_item_id are required")
	}
	body, err := updateChecklistItemBody(in)
	if err != nil {
		return nil, nil, err
	}
	path := "/checklist/" + url.PathEscape(in.ChecklistID) + "/checklist_item/" + url.PathEscape(in.ChecklistItemID)
	if err := c.clickup(ctx, http.MethodPut, path, nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"checklist_item_id": in.ChecklistItemID, "status": "updated"})
}

type cuDeleteChecklistItemIn struct {
	ChecklistID     string `json:"checklist_id" jsonschema:"parent checklist ID"`
	ChecklistItemID string `json:"checklist_item_id" jsonschema:"item ID to delete"`
}

// clickupDeleteChecklistItem removes one item via
// DELETE /checklist/{id}/checklist_item/{item}.
func (c *Client) clickupDeleteChecklistItem(ctx context.Context, _ *mcp.CallToolRequest, in cuDeleteChecklistItemIn) (*mcp.CallToolResult, any, error) {
	if in.ChecklistID == "" || in.ChecklistItemID == "" {
		return nil, nil, fmt.Errorf("checklist_id and checklist_item_id are required")
	}
	path := "/checklist/" + url.PathEscape(in.ChecklistID) + "/checklist_item/" + url.PathEscape(in.ChecklistItemID)
	if err := c.clickup(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"checklist_item_id": in.ChecklistItemID, "status": "deleted"})
}

// --- comments -----------------------------------------------------------

// commentBody builds the request body for create/update comment, in priority
// order:
//
//	rich     → sent verbatim as ClickUp's structured `comment` array (full rich
//	           text: bullet lists, code blocks, bold, plus its own type:tag
//	           blocks). Overrides text/mentions.
//	mentions → `comment` array of the text followed by one type:tag block per
//	           user id, so each tag is a live @mention that notifies the user.
//	otherwise→ flat `comment_text` (unchanged plain-text behaviour).
//
// NOTE: ClickUp documents the `comment` array for CREATE (POST
// /task/{id}/comment). Its acceptance on UPDATE (PUT /comment/{id}) and the
// actual notification firing are not documented and are unverified against a
// live workspace.
func commentBody(text string, mentions []int, rich []map[string]any) map[string]any {
	if len(rich) > 0 {
		return map[string]any{"comment": rich}
	}
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
	TaskID      string           `json:"task_id" jsonschema:"task to comment on"`
	Comment     string           `json:"comment,omitempty" jsonschema:"comment text (plain); optional if comment_json or mentions is given"`
	Mentions    []int            `json:"mentions,omitempty" jsonschema:"ClickUp user IDs to @mention as live tags that notify them; get IDs from clickup_list_members (for a large workspace whose member list is empty, call it with task_id set to this task)"`
	CommentJSON []map[string]any `json:"comment_json,omitempty" jsonschema:"optional ClickUp rich-text comment array (segments like {text, attributes} and/or {type:tag, user:{id}}); when set it is sent verbatim and overrides comment/mentions — use for bullet lists, code blocks, bold, etc."`
}

func (c *Client) clickupAddComment(ctx context.Context, _ *mcp.CallToolRequest, in cuAddCommentIn) (*mcp.CallToolResult, any, error) {
	body := commentBody(in.Comment, in.Mentions, in.CommentJSON)
	var out struct {
		ID any `json:"id"`
	}
	if err := c.clickup(ctx, http.MethodPost, "/task/"+url.PathEscape(in.TaskID)+"/comment", nil, body, &out); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"comment_id": out.ID, "status": "posted"})
}

// cuComment is the slimmed comment/reply shape. reply_count > 0 means the
// comment has threaded replies — fetch them with clickup_list_comment_replies
// (ClickUp's /task/{id}/comment returns only top-level comments, not replies).
type cuComment struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	By         string `json:"by"`
	Date       string `json:"date"`
	ReplyCount int    `json:"reply_count,omitempty"`
}

type rawCUComment struct {
	ID          string `json:"id"`
	CommentText string `json:"comment_text"`
	User        struct {
		Username string `json:"username"`
	} `json:"user"`
	Date       string `json:"date"`
	ReplyCount int    `json:"reply_count"`
}

func slimComment(cm rawCUComment) cuComment {
	return cuComment{ID: cm.ID, Text: cm.CommentText, By: cm.User.Username, Date: cm.Date, ReplyCount: cm.ReplyCount}
}

func (c *Client) clickupListComments(ctx context.Context, _ *mcp.CallToolRequest, in cuTaskIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		Comments []rawCUComment `json:"comments"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/task/"+url.PathEscape(in.TaskID)+"/comment", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	comments := make([]cuComment, 0, len(out.Comments))
	for _, cm := range out.Comments {
		comments = append(comments, slimComment(cm))
	}
	return textResult(map[string]any{"comments": comments})
}

// cuCommentRepliesIn identifies a parent comment whose threaded replies to list.
type cuCommentRepliesIn struct {
	CommentID string `json:"comment_id" jsonschema:"parent comment ID (from clickup_list_comments; fetch when its reply_count > 0)"`
}

func (c *Client) clickupListCommentReplies(ctx context.Context, _ *mcp.CallToolRequest, in cuCommentRepliesIn) (*mcp.CallToolResult, any, error) {
	var out struct {
		Comments []rawCUComment `json:"comments"`
	}
	if err := c.clickup(ctx, http.MethodGet, "/comment/"+url.PathEscape(in.CommentID)+"/reply", nil, nil, &out); err != nil {
		return nil, nil, err
	}
	replies := make([]cuComment, 0, len(out.Comments))
	for _, cm := range out.Comments {
		replies = append(replies, slimComment(cm))
	}
	return textResult(map[string]any{"comment_id": in.CommentID, "replies": replies})
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
	CommentID   string           `json:"comment_id" jsonschema:"comment ID (from clickup_list_comments or clickup_add_comment)"`
	Comment     string           `json:"comment,omitempty" jsonschema:"new comment text (plain); optional if comment_json or mentions is given"`
	Mentions    []int            `json:"mentions,omitempty" jsonschema:"ClickUp user IDs to @mention as live tags that notify them; get IDs from clickup_list_members (for a large workspace whose member list is empty, call it with task_id set to this task)"`
	CommentJSON []map[string]any `json:"comment_json,omitempty" jsonschema:"optional ClickUp rich-text comment array (segments like {text, attributes} and/or {type:tag, user:{id}}); when set it is sent verbatim and overrides comment/mentions. NOTE: an update replaces the whole comment, so include the full rich text to avoid losing existing formatting (list_comments returns plain text only)."`
}

// clickupUpdateComment edits a comment's text in place via PUT /comment/{id},
// so the comment keeps its id and thread position (unlike delete + re-add).
func (c *Client) clickupUpdateComment(ctx context.Context, _ *mcp.CallToolRequest, in cuUpdateCommentIn) (*mcp.CallToolResult, any, error) {
	body := commentBody(in.Comment, in.Mentions, in.CommentJSON)
	if err := c.clickup(ctx, http.MethodPut, "/comment/"+url.PathEscape(in.CommentID), nil, body, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]string{"comment_id": in.CommentID, "status": "updated"})
}

// --- attachments --------------------------------------------------------

type cuUploadAttachmentIn struct {
	TaskID   string `json:"task_id" jsonschema:"task to attach the file to"`
	Filename string `json:"filename" jsonschema:"file name including extension (e.g. errors.csv)"`
	Content  string `json:"content" jsonschema:"the file's content as text"`
}

// multipartFile builds a single-file multipart/form-data body under the given
// form field, and returns the body plus its Content-Type (with boundary).
func multipartFile(field, filename string, content []byte) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

// clickupUploadAttachment attaches a file to a task via
// POST /task/{id}/attachment — multipart/form-data with a single `attachment`
// field. ClickUp wants the raw token in Authorization (no Bearer), like every
// other ClickUp call, but the multipart body bypasses the JSON `clickup` helper.
func (c *Client) clickupUploadAttachment(ctx context.Context, _ *mcp.CallToolRequest, in cuUploadAttachmentIn) (*mcp.CallToolResult, any, error) {
	if in.TaskID == "" || in.Filename == "" || in.Content == "" {
		return nil, nil, fmt.Errorf("task_id, filename and content are required")
	}
	tok, err := c.token(ServiceClickUp)
	if err != nil {
		return nil, nil, err
	}
	body, ctype, err := multipartFile("attachment", in.Filename, []byte(in.Content))
	if err != nil {
		return nil, nil, err
	}
	u := clickupAPIBase + "/task/" + url.PathEscape(in.TaskID) + "/attachment"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", tok)
	req.Header.Set("Content-Type", ctype)
	var out struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := c.do(req, &out); err != nil {
		return nil, nil, err
	}
	res := map[string]any{"status": "attached", "filename": in.Filename}
	if out.ID != "" {
		res["attachment_id"] = out.ID
	}
	if out.URL != "" {
		res["url"] = out.URL
	}
	return textResult(res)
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
