package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const defaultEndpoint = "https://api.linear.app/graphql"
const httpTimeout = 30 * time.Second

// ClientConfig holds configuration for the Linear GraphQL client.
type ClientConfig struct {
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	TerminalStates []string
	Endpoint       string
}

// linearProject is the internal representation of a Linear project.
type linearProject struct {
	ID   string
	Name string
	Slug string
}

// rateLimitSnapshot holds the most recently observed API rate limit headers.
type rateLimitSnapshot struct {
	requestsLimit       int
	requestsRemaining   int
	requestsReset       *time.Time
	complexityLimit     int
	complexityRemaining int
}

// Client is the Linear GraphQL tracker adapter.
type Client struct {
	cfg           ClientConfig
	httpClient    *http.Client
	rateMu        sync.RWMutex
	lastRateLimit *rateLimitSnapshot

	// projectFilterMu guards projectFilter.
	// nil = use cfg.ProjectSlug (WORKFLOW.md default, backward compat).
	// non-nil empty slice = all issues, no project filter.
	// non-nil with slugs = filter to those projects.
	projectFilterMu sync.RWMutex
	projectFilter   *[]string
}

// NewClient creates a new Linear Client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// SetProjectFilter updates the runtime project filter.
// Pass nil to revert to the WORKFLOW.md project_slug default.
// Pass an empty slice to fetch all issues with no project restriction.
// Pass slug strings to restrict to specific projects; use the noProjectSentinel
// constant ("__no_project__") to include issues that have no project assigned.
func (c *Client) SetProjectFilter(slugs []string) {
	c.projectFilterMu.Lock()
	defer c.projectFilterMu.Unlock()
	if slugs == nil {
		c.projectFilter = nil
		return
	}
	copy := append([]string{}, slugs...)
	c.projectFilter = &copy
}

// GetProjectFilter returns the current runtime project filter.
// nil means "using WORKFLOW.md default"; empty slice means "all issues".
func (c *Client) GetProjectFilter() []string {
	c.projectFilterMu.RLock()
	defer c.projectFilterMu.RUnlock()
	if c.projectFilter == nil {
		return nil
	}
	return append([]string{}, *c.projectFilter...)
}

// FetchProjects returns all projects accessible to the API key.
// Used by the interactive project picker in the TUI and web dashboard.
// Implements tracker.ProjectManager.
func (c *Client) FetchProjects(ctx context.Context) ([]domain.Project, error) {
	body, err := c.graphql(ctx, QueryListProjects, nil)
	if err != nil {
		return nil, fmt.Errorf("linear_fetch_projects: %w", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_fetch_projects: missing data")
	}
	projects, ok := data["projects"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_fetch_projects: missing projects block")
	}
	nodes, ok := projects["nodes"].([]any)
	if !ok {
		return nil, fmt.Errorf("linear_fetch_projects: missing nodes")
	}
	result := make([]linearProject, 0, len(nodes))
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		id, _ := node["id"].(string)
		name, _ := node["name"].(string)
		slug, _ := node["slugId"].(string)
		if id == "" || name == "" {
			continue
		}
		result = append(result, linearProject{ID: id, Name: name, Slug: slug})
	}
	out := make([]domain.Project, len(result))
	for i, p := range result {
		out[i] = domain.Project{ID: p.ID, Name: p.Name, Slug: p.Slug}
	}
	return out, nil
}

// RateLimits returns the last observed API rate limit snapshot, or nil if unknown.
func (c *Client) RateLimits() (reqLimit, reqRemaining int, reset *time.Time, complexLimit, complexRemaining int) {
	c.rateMu.RLock()
	defer c.rateMu.RUnlock()
	if c.lastRateLimit == nil {
		return 0, 0, nil, 0, 0
	}
	return c.lastRateLimit.requestsLimit, c.lastRateLimit.requestsRemaining, c.lastRateLimit.requestsReset,
		c.lastRateLimit.complexityLimit, c.lastRateLimit.complexityRemaining
}

// RateLimitSnapshot implements tracker.RateLimiter so callers can type-assert
// Tracker to tracker.RateLimiter without importing this concrete package.
func (c *Client) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	reqLim, reqRem, reset, cplxLim, cplxRem := c.RateLimits()
	if reqLim == 0 && cplxLim == 0 {
		return nil
	}
	return &tracker.RateLimitSnapshot{
		RequestsLimit:       reqLim,
		RequestsRemaining:   reqRem,
		Reset:               reset,
		ComplexityLimit:     cplxLim,
		ComplexityRemaining: cplxRem,
	}
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

// FetchCandidateIssues returns paginated active-state issues respecting the
// current runtime project filter. If no runtime filter is set, it falls back
// to the WORKFLOW.md project_slug value (or all issues if that is also empty).
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	filter := c.GetProjectFilter()

	// nil = runtime filter not set; fall back to WORKFLOW.md project_slug.
	if filter == nil {
		if c.cfg.ProjectSlug != "" {
			return c.fetchByProjectSlug(ctx, c.cfg.ActiveStates, c.cfg.ProjectSlug, nil, nil)
		}
		return c.fetchByStatesPage(ctx, QueryCandidateIssuesAll, map[string]any{
			"stateNames":    c.cfg.ActiveStates,
			"relationFirst": pageSize,
		}, nil, nil)
	}

	// Empty filter = all issues regardless of project.
	if len(filter) == 0 {
		return c.fetchByStatesPage(ctx, QueryCandidateIssuesAll, map[string]any{
			"stateNames":    c.cfg.ActiveStates,
			"relationFirst": pageSize,
		}, nil, nil)
	}

	// Specific project/no-project filter: fetch each and merge.
	seen := make(map[string]struct{})
	var all []domain.Issue
	for _, slug := range filter {
		var issues []domain.Issue
		var err error
		if slug == noProjectSentinel {
			issues, err = c.fetchByStatesPage(ctx, QueryCandidateIssuesNoProject, map[string]any{
				"stateNames":    c.cfg.ActiveStates,
				"relationFirst": pageSize,
			}, nil, nil)
		} else {
			issues, err = c.fetchByProjectSlug(ctx, c.cfg.ActiveStates, slug, nil, nil)
		}
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if _, dup := seen[issue.ID]; !dup {
				seen[issue.ID] = struct{}{}
				all = append(all, issue)
			}
		}
	}
	return all, nil
}

// FetchIssuesByStates returns issues for the given state names.
// Empty stateNames returns empty slice without any API call.
// Uses the WORKFLOW.md project_slug if set; otherwise fetches all issues by state.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	if c.cfg.ProjectSlug != "" {
		return c.fetchByProjectSlug(ctx, stateNames, c.cfg.ProjectSlug, nil, nil)
	}
	return c.fetchByStatesPage(ctx, QueryCandidateIssuesAll, map[string]any{
		"stateNames":    stateNames,
		"relationFirst": pageSize,
	}, nil, nil)
}

// FetchIssueStatesByIDs returns current state for the given issue IDs.
// Empty issueIDs returns empty slice without any API call.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}
	return c.fetchByIDsPage(ctx, issueIDs, nil)
}

// UpdateIssueState transitions the issue to the named workflow state.
// It resolves the state name to a Linear workflow-state UUID by querying the
// issue's own team states (matching the Elixir implementation), then calls issueUpdate.
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	// Step 1: resolve state name → UUID via the issue's team.
	// Linear workflow states are team-scoped, so we query via issue → team → states.
	const stateQuery = `
query ItervoxResolveStateId($issueId: String!, $stateName: String!) {
  issue(id: $issueId) {
    team {
      states(filter: { name: { eq: $stateName } }, first: 1) {
        nodes { id }
      }
    }
  }
}`
	body, err := c.graphql(ctx, stateQuery, map[string]any{
		"issueId":   issueID,
		"stateName": stateName,
	})
	if err != nil {
		return fmt.Errorf("linear_update_state: fetch state id: %w", err)
	}
	data, _ := body["data"].(map[string]any)
	issue, _ := data["issue"].(map[string]any)
	team, _ := issue["team"].(map[string]any)
	states, _ := team["states"].(map[string]any)
	nodes, _ := states["nodes"].([]any)
	if len(nodes) == 0 {
		return fmt.Errorf("linear_update_state: state %q not found for issue %q", stateName, issueID)
	}
	// G-19 (gaps_280426_2): comma-ok the chained type assertion. Previously a
	// single-value `nodes[0].(map[string]any)["id"].(string)` would panic if
	// Linear ever returned a non-map element here.
	firstNode, ok := nodes[0].(map[string]any)
	if !ok {
		return fmt.Errorf("linear_update_state: unexpected node[0] shape for state %q", stateName)
	}
	stateID, _ := firstNode["id"].(string)
	if stateID == "" {
		return fmt.Errorf("linear_update_state: empty state id for %q", stateName)
	}

	// Step 2: update the issue.
	const mutation = `
mutation ItervoxUpdateIssueState($issueId: String!, $stateId: String!) {
  issueUpdate(id: $issueId, input: { stateId: $stateId }) {
    success
  }
}`
	resp, err := c.graphql(ctx, mutation, map[string]any{
		"issueId": issueID,
		"stateId": stateID,
	})
	if err != nil {
		return fmt.Errorf("linear_update_state: issueUpdate: %w", err)
	}
	if d, ok := resp["data"].(map[string]any); ok {
		if iu, ok := d["issueUpdate"].(map[string]any); ok {
			if success, _ := iu["success"].(bool); success {
				return nil
			}
		}
	}
	return fmt.Errorf("linear_update_state: issueUpdate returned non-success: %v", resp)
}

// CreateComment posts a comment body on the given Linear issue ID.
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	const mutation = `
mutation ItervoxCreateComment($issueId: String!, $body: String!) {
  commentCreate(input: {issueId: $issueId, body: $body}) {
    comment {
      id
      body
      createdAt
      user { id name }
    }
    success
  }
}`
	vars := map[string]any{"issueId": issueID, "body": body}
	resp, err := c.graphql(ctx, mutation, vars)
	if err != nil {
		return nil, err
	}
	if data, ok := resp["data"].(map[string]any); ok {
		if cc, ok := data["commentCreate"].(map[string]any); ok {
			if success, _ := cc["success"].(bool); success {
				comment := &domain.Comment{Body: body}
				if rawComment, ok := cc["comment"].(map[string]any); ok {
					comment.ID, _ = rawComment["id"].(string)
					comment.Body, _ = rawComment["body"].(string)
					comment.CreatedAt = tracker.ParseTime(rawComment["createdAt"])
					if user, ok := rawComment["user"].(map[string]any); ok {
						comment.AuthorID, _ = user["id"].(string)
						comment.AuthorName, _ = user["name"].(string)
					}
				}
				return comment, nil
			}
		}
	}
	return nil, fmt.Errorf("linear_create_comment: unexpected response: %v", resp)
}

// CreateIssue creates a follow-up issue in the same team/project context as
// sourceIssueID and assigns it to the configured state/column.
func (c *Client) CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error) {
	contextResp, err := c.graphql(ctx, QueryCreateIssueContext, map[string]any{"id": sourceIssueID})
	if err != nil {
		return nil, fmt.Errorf("linear_create_issue: resolve context: %w", err)
	}
	data, ok := contextResp["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_create_issue: missing data")
	}
	sourceIssue, ok := data["issue"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_create_issue: missing issue context")
	}
	team, ok := sourceIssue["team"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_create_issue: missing team context")
	}
	teamID, _ := team["id"].(string)
	if teamID == "" {
		return nil, fmt.Errorf("linear_create_issue: empty team id")
	}
	states, _ := team["states"].(map[string]any)
	nodes, _ := states["nodes"].([]any)
	stateID := ""
	for _, rawNode := range nodes {
		node, ok := rawNode.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringValue(node["name"])), stateName) {
			stateID = stringValue(node["id"])
			break
		}
	}
	if stateID == "" {
		return nil, fmt.Errorf("linear_create_issue: state %q not found in source issue team", stateName)
	}

	input := map[string]any{
		"teamId":      teamID,
		"title":       title,
		"description": body,
		"stateId":     stateID,
	}
	if project, ok := sourceIssue["project"].(map[string]any); ok {
		if projectID, _ := project["id"].(string); projectID != "" {
			input["projectId"] = projectID
		}
	}

	const mutation = `
mutation ItervoxCreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    issue {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      labels { nodes { name } }
      inverseRelations(first: 50) {
        nodes {
          type
          issue { id identifier url state { name } }
        }
      }
      createdAt
      updatedAt
    }
    success
  }
}`

	resp, err := c.graphql(ctx, mutation, map[string]any{"input": input})
	if err != nil {
		return nil, fmt.Errorf("linear_create_issue: issueCreate: %w", err)
	}
	if data, ok := resp["data"].(map[string]any); ok {
		if created, ok := data["issueCreate"].(map[string]any); ok {
			if success, _ := created["success"].(bool); success {
				if issueNode, ok := created["issue"].(map[string]any); ok {
					issue := normalizeIssue(issueNode)
					if issue != nil {
						return issue, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("linear_create_issue: unexpected response: %v", resp)
}

// SetIssueBranch updates the branchName field on the Linear issue so retried
// workers can resume from the correct branch.
func (c *Client) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	const mutation = `
mutation ItervoxSetBranchName($issueId: String!, $branchName: String!) {
  issueUpdate(id: $issueId, input: { branchName: $branchName }) {
    success
  }
}`
	resp, err := c.graphql(ctx, mutation, map[string]any{
		"issueId":    issueID,
		"branchName": branchName,
	})
	if err != nil {
		return fmt.Errorf("linear_set_branch: issueUpdate: %w", err)
	}
	if d, ok := resp["data"].(map[string]any); ok {
		if iu, ok := d["issueUpdate"].(map[string]any); ok {
			if success, _ := iu["success"].(bool); success {
				return nil
			}
		}
	}
	return fmt.Errorf("linear_set_branch: issueUpdate returned non-success: %v", resp)
}

// FetchIssueDetail returns a single issue with full details including comments.
func (c *Client) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	body, err := c.graphql(ctx, QueryIssueDetail, map[string]any{"id": issueID})
	if err != nil {
		return nil, fmt.Errorf("linear_fetch_detail: %w", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeError(body)
	}
	rawIssue, ok := data["issue"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_fetch_detail: missing issue in response")
	}
	issue := normalizeIssue(rawIssue)
	if issue == nil {
		return nil, fmt.Errorf("linear_fetch_detail: could not normalize issue")
	}
	// Extract comments
	if commentsBlock, ok := rawIssue["comments"].(map[string]any); ok {
		if nodes, ok := commentsBlock["nodes"].([]any); ok {
			for _, n := range nodes {
				node, ok := n.(map[string]any)
				if !ok {
					continue
				}
				b, _ := node["body"].(string)
				if b == "" {
					continue
				}
				c := domain.Comment{
					ID:        stringValue(node["id"]),
					Body:      b,
					CreatedAt: tracker.ParseTime(node["createdAt"]),
				}
				if user, ok := node["user"].(map[string]any); ok {
					c.AuthorID = stringValue(user["id"])
					c.AuthorName, _ = user["name"].(string)
				}
				issue.Comments = append(issue.Comments, c)
			}
		}
	}
	return issue, nil
}

// FetchIssueByIdentifier returns a single issue by its human-readable identifier
// (e.g. "ENG-42"). The Linear GraphQL `issue(id:)` field accepts both UUIDs and
// identifier strings, so this delegates directly to FetchIssueDetail.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	return c.FetchIssueDetail(ctx, identifier)
}

// maxPaginationPages is the maximum number of pages fetched per query to prevent
// unbounded iteration when a tracker response always returns hasNextPage=true.
const maxPaginationPages = 200

// fetchByStatesPage is a generic paginator for any candidate-issues query.
// query is the GraphQL query string; baseVars are merged with pagination vars each page.
// Iterative (not recursive) to avoid stack overflow on deep pagination.
func (c *Client) fetchByStatesPage(ctx context.Context, query string, baseVars map[string]any, afterCursor *string, acc []domain.Issue) ([]domain.Issue, error) {
	cursor := afterCursor
	for range maxPaginationPages {
		vars := make(map[string]any, len(baseVars)+2)
		maps.Copy(vars, baseVars)
		vars["first"] = pageSize
		vars["after"] = cursor

		body, err := c.graphql(ctx, query, vars)
		if err != nil {
			return nil, err
		}

		issues, pi, err := decodePageResponse(body)
		if err != nil {
			return nil, err
		}

		acc = append(acc, issues...)

		if !pi.HasNextPage {
			return acc, nil
		}
		if pi.EndCursor == "" {
			return nil, fmt.Errorf("linear_missing_end_cursor: hasNextPage=true but endCursor is empty")
		}
		cursor = &pi.EndCursor
	}
	return nil, fmt.Errorf("linear_pagination_limit: exceeded %d pages", maxPaginationPages)
}

// fetchByProjectSlug fetches paginated issues filtered to a specific project slug.
func (c *Client) fetchByProjectSlug(ctx context.Context, states []string, slug string, afterCursor *string, acc []domain.Issue) ([]domain.Issue, error) {
	return c.fetchByStatesPage(ctx, QueryCandidateIssues, map[string]any{
		"projectSlug":   slug,
		"stateNames":    states,
		"relationFirst": pageSize,
	}, afterCursor, acc)
}

// fetchByIDsPage fetches issues by ID list in batches.
// Iterative (not recursive) to avoid stack overflow on large ID lists.
func (c *Client) fetchByIDsPage(ctx context.Context, ids []string, acc []domain.Issue) ([]domain.Issue, error) {
	for len(ids) > 0 {
		batch := ids
		if len(ids) > pageSize {
			batch = ids[:pageSize]
			ids = ids[pageSize:]
		} else {
			ids = nil
		}

		vars := map[string]any{
			"ids":           batch,
			"first":         len(batch),
			"relationFirst": pageSize,
		}
		body, err := c.graphql(ctx, QueryIssuesByIDs, vars)
		if err != nil {
			return nil, err
		}

		issues, err := decodeResponse(body)
		if err != nil {
			return nil, err
		}
		acc = append(acc, issues...)
	}
	return acc, nil
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

func decodePageResponse(body map[string]any) ([]domain.Issue, pageInfo, error) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, pageInfo{}, decodeError(body)
	}
	issuesBlock, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, pageInfo{}, fmt.Errorf("linear_unknown_payload: missing issues block")
	}
	pi := extractPageInfo(issuesBlock)
	issues, err := extractNodes(issuesBlock)
	if err != nil {
		return nil, pageInfo{}, err
	}
	return issues, pi, nil
}

func decodeResponse(body map[string]any) ([]domain.Issue, error) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeError(body)
	}
	issuesBlock, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload: missing issues block")
	}
	return extractNodes(issuesBlock)
}

func decodeError(body map[string]any) error {
	if errs, ok := body["errors"]; ok {
		return &tracker.GraphQLError{Message: fmt.Sprintf("%v", errs)}
	}
	return fmt.Errorf("linear_unknown_payload: %v", body)
}

func extractPageInfo(issuesBlock map[string]any) pageInfo {
	pi, ok := issuesBlock["pageInfo"].(map[string]any)
	if !ok {
		return pageInfo{}
	}
	hasNext, _ := pi["hasNextPage"].(bool)
	endCursor, _ := pi["endCursor"].(string)
	return pageInfo{HasNextPage: hasNext, EndCursor: endCursor}
}

func extractNodes(issuesBlock map[string]any) ([]domain.Issue, error) {
	nodesRaw, ok := issuesBlock["nodes"].([]any)
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload: missing nodes")
	}
	var result []domain.Issue
	for _, n := range nodesRaw {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		if issue := normalizeIssue(node); issue != nil {
			result = append(result, *issue)
		}
	}
	return result, nil
}

// graphql executes a GraphQL query and returns the decoded response body.
func (c *Client) graphql(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: build request: %w", err)
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Surface the GraphQL error body in daemon logs so a 400 from a bad
		// filter shape (or any other vendor-side validation) is debuggable
		// without HTTP capture. Body may be empty; we just truncate to keep
		// the log line bounded. The returned error wraps the status code so
		// callers can still classify it with errors.As(*APIStatusError).
		body, _ := io.ReadAll(resp.Body)
		if snippet := strings.TrimSpace(string(body)); snippet != "" {
			const maxLen = 512
			if len(snippet) > maxLen {
				snippet = snippet[:maxLen] + "…"
			}
			slog.Warn("linear: graphql error response",
				"status", resp.StatusCode,
				"body", snippet)
		}
		return nil, &tracker.APIStatusError{Adapter: "linear", Status: resp.StatusCode}
	}

	// Capture rate limit headers when present.
	snap := &rateLimitSnapshot{}
	if lim := resp.Header.Get("X-RateLimit-Requests-Limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil {
			snap.requestsLimit = n
		}
	}
	if rem := resp.Header.Get("X-RateLimit-Requests-Remaining"); rem != "" {
		if n, err := strconv.Atoi(rem); err == nil {
			snap.requestsRemaining = n
		}
	}
	if rs := resp.Header.Get("X-RateLimit-Requests-Reset"); rs != "" {
		if n, err := strconv.ParseInt(rs, 10, 64); err == nil {
			if n > 1e11 {
				n = n / 1000
			}
			t := time.Unix(n, 0)
			snap.requestsReset = &t
		}
	}
	if lim := resp.Header.Get("X-RateLimit-Complexity-Limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil {
			snap.complexityLimit = n
		}
	}
	if rem := resp.Header.Get("X-RateLimit-Complexity-Remaining"); rem != "" {
		if n, err := strconv.Atoi(rem); err == nil {
			snap.complexityRemaining = n
		}
	}
	if snap.requestsLimit > 0 || snap.complexityLimit > 0 {
		c.rateMu.Lock()
		c.lastRateLimit = snap
		c.rateMu.Unlock()
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: read body: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return nil, fmt.Errorf("linear_api_request: decode json: %w", err)
	}
	return result, nil
}
