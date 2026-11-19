package jira

import (
	"context"
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// issueFieldList is the one list of fields every search and fetch requests.
const issueFieldList = "summary,description,status,priority,labels,issuelinks,assignee,created,updated"

func issueFields() []string { return strings.Split(issueFieldList, ",") }

// jqlQuote returns s as a double-quoted JQL string, escaping backslash and
// double quote so a value cannot break out of its quotes.
func jqlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// statesJQL builds the project-bounded query for the given state names.
func (c *Client) statesJQL(states []string) string {
	quoted := make([]string, len(states))
	for i, s := range states {
		quoted[i] = jqlQuote(s)
	}
	return fmt.Sprintf("project = %s AND status in (%s) ORDER BY created ASC",
		jqlQuote(c.cfg.ProjectSlug), strings.Join(quoted, ", "))
}

// FetchCandidateIssues returns the project's issues in an active state, oldest
// first. With no active states it returns an empty slice and makes no request.
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	return c.FetchIssuesByStates(ctx, c.activeStates())
}

// FetchIssuesByStates returns the project's issues in any of the named
// states. Empty names return an empty slice and make no request.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	raws, err := c.search(ctx, c.statesJQL(stateNames))
	if err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}
	return c.normalizeAll(raws), nil
}

// search runs jql through POST /rest/api/3/search/jql, following nextPageToken
// until the server reports no more pages. It gives up after c.maxPages
// pages so a server that never ends cannot loop forever.
func (c *Client) search(ctx context.Context, jql string) ([]map[string]any, error) {
	ctx = tracker.WithReadIntent(ctx) // a POST that only reads
	var all []map[string]any
	token := ""
	for range c.maxPages {
		var page struct {
			Issues        []map[string]any `json:"issues"`
			NextPageToken string           `json:"nextPageToken"`
			IsLast        bool             `json:"isLast"`
		}
		body := map[string]any{"jql": jql, "fields": issueFields(), "maxResults": 100}
		if token != "" {
			body["nextPageToken"] = token
		}
		if err := c.do(ctx, "POST", "/rest/api/3/search/jql", body, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Issues...)
		if page.NextPageToken == "" || page.IsLast {
			return all, nil
		}
		token = page.NextPageToken
	}
	return nil, fmt.Errorf("pagination did not end after %d pages", c.maxPages)
}

// bulkChunk is the most ids one bulkfetch request carries.
const bulkChunk = 100

// FetchIssueStatesByIDs returns the current snapshot of the given issue ids
// through POST /rest/api/3/issue/bulkfetch, in chunks of 100. Every id must be
// digits only. An id the response omits (a deleted issue) is skipped, not an
// error. Issues Jira could not read (issueErrors) fail the call, so a retriable
// failure never looks like a deletion. Empty ids return an empty slice and make no request.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	ctx = tracker.WithReadIntent(ctx) // a POST that only reads
	out := []domain.Issue{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	for _, id := range issueIDs {
		if !issueIDPattern.MatchString(id) {
			return nil, fmt.Errorf("jira_invalid_issue_id: %q is not a numeric issue id", id)
		}
	}
	for start := 0; start < len(issueIDs); start += bulkChunk {
		chunk := issueIDs[start:min(start+bulkChunk, len(issueIDs))]
		var resp struct {
			Issues      []map[string]any `json:"issues"`
			IssueErrors []struct {
				ErrorMessage string `json:"errorMessage"`
			} `json:"issueErrors"`
		}
		body := map[string]any{"issueIdsOrKeys": chunk, "fields": issueFields()}
		if err := c.do(ctx, "POST", "/rest/api/3/issue/bulkfetch", body, &resp); err != nil {
			return nil, fmt.Errorf("jira_bulk_fetch: %w", err)
		}
		// Jira lists only retriable failures here; a deleted issue is simply
		// absent. Failing keeps a retriable error from looking like a deletion.
		if n := len(resp.IssueErrors); n > 0 {
			return nil, fmt.Errorf("jira_bulk_fetch: %d issue(s) could not be read: %s", n, resp.IssueErrors[0].ErrorMessage)
		}
		out = append(out, c.normalizeAll(resp.Issues)...)
	}
	return out, nil
}
