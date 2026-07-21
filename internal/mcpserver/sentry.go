package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sentryProject is the slimmed project shape. Sentry's raw project payloads
// carry dozens of settings fields; keep the ones that identify a project and
// let a caller resolve a human slug to the numeric id the issues API wants.
type sentryProject struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Platform     string   `json:"platform,omitempty"`
	Environments []string `json:"environments,omitempty"`
}

// sentryIssue is the slimmed issue shape — one digest row. Note count is a
// STRING in Sentry's API ("348"), while userCount is an integer.
type sentryIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Count     string `json:"count"`
	UserCount int    `json:"userCount"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	Permalink string `json:"permalink"`
	Project   struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"project"`
}

// sentryIssueDetail extends the digest row with the extra context the issue
// detail endpoint returns. Pointer/omitempty fields stay absent when Sentry
// omits them, so an unexpectedly-shaped payload degrades to the base fields
// rather than failing to decode.
type sentryIssueDetail struct {
	sentryIssue
	Metadata struct {
		Title string `json:"title,omitempty"`
		Type  string `json:"type,omitempty"`
		Value string `json:"value,omitempty"`
	} `json:"metadata"`
	NumComments  int            `json:"numComments,omitempty"`
	FirstRelease *sentryRelease `json:"firstRelease,omitempty"`
	LastRelease  *sentryRelease `json:"lastRelease,omitempty"`
}

type sentryRelease struct {
	Version string `json:"version,omitempty"`
}

// sentryOrg resolves the org slug for a call: the per-call override wins, then
// the configured default (mcp.sentry.org). Empty is an actionable error.
func (c *Client) sentryOrg(override string) (string, error) {
	org := strings.TrimSpace(override)
	if org == "" {
		org = strings.TrimSpace(c.SentryOrg)
	}
	if org == "" {
		return "", fmt.Errorf("no Sentry organization set — pass organization_slug, or run `aimonitor config set mcp.sentry.org <slug>`")
	}
	return org, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- sentry_list_projects -------------------------------------------------

type sentryListProjectsIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
}

func (c *Client) sentryListProjects(ctx context.Context, _ *mcp.CallToolRequest, in sentryListProjectsIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	// One page (Sentry returns up to ~100 projects; the vast majority of orgs
	// fit). Pagination via the Link header is a follow-up if it's ever needed.
	var projects []sentryProject
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/projects/", nil, nil, &projects); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{
		"organization": org,
		"count":        len(projects),
		"projects":     projects,
	})
}

// resolveProjectID maps a project slug to its numeric id (the issues API's
// `project` filter wants the id). A numeric input is returned unchanged.
func (c *Client) resolveProjectID(ctx context.Context, org, project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", nil
	}
	if isAllDigits(project) {
		return project, nil
	}
	var projects []sentryProject
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/projects/", nil, nil, &projects); err != nil {
		return "", err
	}
	for _, p := range projects {
		if strings.EqualFold(p.Slug, project) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no project with slug %q in org %q", project, org)
}

// --- sentry_search_issues -------------------------------------------------

var sentrySortValues = map[string]bool{"freq": true, "date": true, "new": true, "user": true}

type sentrySearchIssuesIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Query            string `json:"query,omitempty" jsonschema:"Sentry search query (e.g. 'is:unresolved firstSeen:-24h'); defaults to is:unresolved"`
	Project          string `json:"project,omitempty" jsonschema:"Project slug or numeric id to filter by"`
	Sort             string `json:"sort,omitempty" jsonschema:"Sort order: freq (event volume), date (last seen), new (first seen), or user (users affected); default freq"`
	StatsPeriod      string `json:"stats_period,omitempty" jsonschema:"Relative time window for stats, e.g. 24h or 14d"`
	Environment      string `json:"environment,omitempty" jsonschema:"Environment name to filter by"`
	Limit            int    `json:"limit,omitempty" jsonschema:"Max issues to return (1-100, default 25)"`
}

func (c *Client) sentrySearchIssues(ctx context.Context, _ *mcp.CallToolRequest, in sentrySearchIssuesIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		query = "is:unresolved"
	}
	q.Set("query", query)

	sort := strings.TrimSpace(in.Sort)
	if sort == "" {
		sort = "freq"
	}
	if !sentrySortValues[sort] {
		return nil, nil, fmt.Errorf("invalid sort %q — want freq, date, new, or user", sort)
	}
	q.Set("sort", sort)

	if in.StatsPeriod != "" {
		q.Set("statsPeriod", in.StatsPeriod)
	}
	if in.Environment != "" {
		q.Set("environment", in.Environment)
	}
	if in.Project != "" {
		pid, err := c.resolveProjectID(ctx, org, in.Project)
		if err != nil {
			return nil, nil, err
		}
		q.Set("project", pid)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	q.Set("limit", strconv.Itoa(limit))

	var issues []sentryIssue
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/", q, nil, &issues); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{
		"organization": org,
		"query":        query,
		"sort":         sort,
		"count":        len(issues),
		"issues":       issues,
	})
}

// --- sentry_get_issue -----------------------------------------------------

type sentryGetIssueIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId (e.g. 12345 or PRICING-SERVICE-9V)"`
}

func (c *Client) sentryGetIssue(ctx context.Context, _ *mcp.CallToolRequest, in sentryGetIssueIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	var issue sentryIssueDetail
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/", nil, nil, &issue); err != nil {
		return nil, nil, err
	}
	return textResult(issue)
}

// resolveIssueID maps a shortId (e.g. PRICING-SERVICE-9V) to the numeric id the
// issue endpoints take; a numeric input is returned unchanged.
func (c *Client) resolveIssueID(ctx context.Context, org, idOrShort string) (string, error) {
	id := strings.TrimSpace(idOrShort)
	if id == "" {
		return "", fmt.Errorf("issue is required (numeric id or shortId)")
	}
	if isAllDigits(id) {
		return id, nil
	}
	q := url.Values{}
	q.Set("query", id)
	q.Set("shortIdLookup", "1")
	q.Set("limit", "1")
	var matches []sentryIssue
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/", q, nil, &matches); err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no issue matching %q in org %q", id, org)
	}
	return matches[0].ID, nil
}

// --- sentry_get_latest_event ---------------------------------------------

// sentryFrame is one slimmed stack frame (root-cause essentials).
type sentryFrame struct {
	Filename string `json:"filename,omitempty"`
	Function string `json:"function,omitempty"`
	Module   string `json:"module,omitempty"`
	LineNo   int    `json:"lineNo,omitempty"`
	ColNo    int    `json:"colNo,omitempty"`
	InApp    bool   `json:"inApp,omitempty"`
}

type sentryException struct {
	Type   string        `json:"type,omitempty"`
	Value  string        `json:"value,omitempty"`
	Module string        `json:"module,omitempty"`
	Frames []sentryFrame `json:"frames,omitempty"`
}

type sentryEventKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type sentryEvent struct {
	ID          string            `json:"id,omitempty"`
	EventID     string            `json:"eventID,omitempty"`
	Title       string            `json:"title,omitempty"`
	Message     string            `json:"message,omitempty"`
	Culprit     string            `json:"culprit,omitempty"`
	Platform    string            `json:"platform,omitempty"`
	DateCreated string            `json:"dateCreated,omitempty"`
	Tags        []sentryEventKV   `json:"tags,omitempty"`
	Contexts    map[string]any    `json:"contexts,omitempty"`
	Exceptions  []sentryException `json:"exceptions,omitempty"`
}

// rawSentryEvent decodes the event with its polymorphic entries; the exception
// entry carries the stacktrace we slim into sentryEvent.Exceptions.
type rawSentryEvent struct {
	ID          string          `json:"id"`
	EventID     string          `json:"eventID"`
	Title       string          `json:"title"`
	Message     string          `json:"message"`
	Culprit     string          `json:"culprit"`
	Platform    string          `json:"platform"`
	DateCreated string          `json:"dateCreated"`
	Tags        []sentryEventKV `json:"tags"`
	Contexts    map[string]any  `json:"contexts"`
	Entries     []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"entries"`
}

type rawExceptionData struct {
	Values []struct {
		Type       string `json:"type"`
		Value      string `json:"value"`
		Module     string `json:"module"`
		Stacktrace *struct {
			Frames []sentryFrame `json:"frames"`
		} `json:"stacktrace"`
	} `json:"values"`
}

// maxEventFrames caps the returned stacktrace. Sentry orders frames
// caller→callee, so the LAST ones are closest to where the error was raised —
// keep those.
const maxEventFrames = 30

type sentryGetLatestEventIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId (e.g. PRICING-SERVICE-9V)"`
}

func (c *Client) sentryGetLatestEvent(ctx context.Context, _ *mcp.CallToolRequest, in sentryGetLatestEventIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	var raw rawSentryEvent
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/events/latest/", nil, nil, &raw); err != nil {
		return nil, nil, err
	}
	ev := sentryEvent{
		ID: raw.ID, EventID: raw.EventID, Title: raw.Title, Message: raw.Message,
		Culprit: raw.Culprit, Platform: raw.Platform, DateCreated: raw.DateCreated,
		Tags: raw.Tags, Contexts: raw.Contexts,
	}
	for _, e := range raw.Entries {
		if e.Type != "exception" {
			continue
		}
		var xd rawExceptionData
		if err := json.Unmarshal(e.Data, &xd); err != nil {
			continue
		}
		for _, v := range xd.Values {
			ex := sentryException{Type: v.Type, Value: v.Value, Module: v.Module}
			if v.Stacktrace != nil {
				fr := v.Stacktrace.Frames
				if len(fr) > maxEventFrames {
					fr = fr[len(fr)-maxEventFrames:]
				}
				ex.Frames = fr
			}
			ev.Exceptions = append(ev.Exceptions, ex)
		}
	}
	return textResult(map[string]any{"issue": id, "event": ev})
}

// --- sentry_issue_tags ----------------------------------------------------

type sentryTagValue struct {
	Value     string `json:"value"`
	Count     int    `json:"count"`
	FirstSeen string `json:"firstSeen,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

type sentryTag struct {
	Key          string           `json:"key"`
	Name         string           `json:"name,omitempty"`
	UniqueValues int              `json:"uniqueValues,omitempty"`
	TotalValues  int              `json:"totalValues,omitempty"`
	TopValues    []sentryTagValue `json:"topValues,omitempty"`
}

type sentryIssueTagsIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId"`
	Key              string `json:"key,omitempty" jsonschema:"A specific tag key (e.g. shop.id, environment, user) for its full value distribution + counts — answers 'how many X affected'. Omit to list every tag key with its top values."`
}

func (c *Client) sentryIssueTags(ctx context.Context, _ *mcp.CallToolRequest, in sentryIssueTagsIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	baseP := "/organizations/" + url.PathEscape(org) + "/issues/" + url.PathEscape(id) + "/tags/"
	if key := strings.TrimSpace(in.Key); key != "" {
		var tag sentryTag
		if err := c.sentry(ctx, http.MethodGet, baseP+url.PathEscape(key)+"/", nil, nil, &tag); err != nil {
			return nil, nil, err
		}
		return textResult(map[string]any{"issue": id, "tag": tag})
	}
	var tags []sentryTag
	if err := c.sentry(ctx, http.MethodGet, baseP, nil, nil, &tags); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"issue": id, "count": len(tags), "tags": tags})
}

// --- sentry_update_issue (write) -----------------------------------------

var sentryStatusValues = map[string]bool{"resolved": true, "unresolved": true, "ignored": true}

type sentryUpdateIssueIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId"`
	Status           string `json:"status,omitempty" jsonschema:"New status: resolved, unresolved, or ignored"`
	AssignedTo       string `json:"assigned_to,omitempty" jsonschema:"Assignee: an email, 'user:<id>', or 'team:<id>'"`
}

func (c *Client) sentryUpdateIssue(ctx context.Context, _ *mcp.CallToolRequest, in sentryUpdateIssueIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	if s := strings.TrimSpace(in.Status); s != "" {
		if !sentryStatusValues[s] {
			return nil, nil, fmt.Errorf("invalid status %q — want resolved, unresolved, or ignored", s)
		}
		body["status"] = s
	}
	if a := strings.TrimSpace(in.AssignedTo); a != "" {
		body["assignedTo"] = a
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update — set status and/or assigned_to")
	}
	var issue sentryIssueDetail
	if err := c.sentry(ctx, http.MethodPut, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/", nil, body, &issue); err != nil {
		return nil, nil, err
	}
	return textResult(issue)
}

// --- sentry_add_comment (write) ------------------------------------------

type sentryComment struct {
	ID          string `json:"id"`
	DateCreated string `json:"dateCreated,omitempty"`
	Data        struct {
		Text string `json:"text,omitempty"`
	} `json:"data,omitempty"`
}

type sentryAddCommentIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId"`
	Text             string `json:"text" jsonschema:"The comment body to post on the issue"`
}

func (c *Client) sentryAddComment(ctx context.Context, _ *mcp.CallToolRequest, in sentryAddCommentIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, nil, fmt.Errorf("text is required")
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	var note sentryComment
	if err := c.sentry(ctx, http.MethodPost, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/comments/", nil, map[string]any{"text": text}, &note); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"issue": id, "comment_id": note.ID, "posted": true})
}

// --- sentry_delete_comment (write) ---------------------------------------

type sentryDeleteCommentIn struct {
	OrganizationSlug string `json:"organization_slug,omitempty" jsonschema:"Sentry org slug; defaults to the configured mcp.sentry.org"`
	Issue            string `json:"issue" jsonschema:"Issue numeric id or shortId"`
	CommentID        string `json:"comment_id" jsonschema:"The comment id to delete (returned by sentry_add_comment)"`
}

func (c *Client) sentryDeleteComment(ctx context.Context, _ *mcp.CallToolRequest, in sentryDeleteCommentIn) (*mcp.CallToolResult, any, error) {
	org, err := c.sentryOrg(in.OrganizationSlug)
	if err != nil {
		return nil, nil, err
	}
	cid := strings.TrimSpace(in.CommentID)
	if cid == "" {
		return nil, nil, fmt.Errorf("comment_id is required")
	}
	id, err := c.resolveIssueID(ctx, org, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	if err := c.sentry(ctx, http.MethodDelete, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/comments/"+url.PathEscape(cid)+"/", nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(map[string]any{"issue": id, "comment_id": cid, "deleted": true})
}
