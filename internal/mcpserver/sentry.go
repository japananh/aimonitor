package mcpserver

import (
	"context"
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
	id := strings.TrimSpace(in.Issue)
	if id == "" {
		return nil, nil, fmt.Errorf("issue is required (numeric id or shortId)")
	}
	// A shortId (has non-digits) must be resolved to the numeric id the detail
	// endpoint takes; shortIdLookup makes the issues search do that mapping.
	if !isAllDigits(id) {
		q := url.Values{}
		q.Set("query", id)
		q.Set("shortIdLookup", "1")
		q.Set("limit", "1")
		var matches []sentryIssue
		if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/", q, nil, &matches); err != nil {
			return nil, nil, err
		}
		if len(matches) == 0 {
			return nil, nil, fmt.Errorf("no issue matching %q in org %q", id, org)
		}
		id = matches[0].ID
	}
	var issue sentryIssueDetail
	if err := c.sentry(ctx, http.MethodGet, "/organizations/"+url.PathEscape(org)+"/issues/"+url.PathEscape(id)+"/", nil, nil, &issue); err != nil {
		return nil, nil, err
	}
	return textResult(issue)
}
