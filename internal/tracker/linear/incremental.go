package linear

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// The incremental pull (#114) reads the issues updated since a time, in any
// state, including archived and trashed ones so a deletion is seen as one.

const updatedSinceNodeFields = `
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      trashed
      archivedAt
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue { id identifier url branchName state { name } }
        }
      }
      children(first: $relationFirst) {
        nodes { id identifier url branchName state { name } }
      }
      createdAt
      updatedAt
    }
    pageInfo { hasNextPage endCursor }`

func updatedSinceQuery(name, params, filter string) string {
	return "query " + name + "($since: DateTimeOrDuration!, $first: Int!, $relationFirst: Int!, $after: String" + params + ") {\n" +
		"  issues(filter: {" + filter + "updatedAt: {gte: $since}}, includeArchived: true, orderBy: updatedAt, first: $first, after: $after) {" +
		updatedSinceNodeFields + "\n  }\n}"
}

// Queries for the three scopes fetchWithProjectFilter knows: a project, all
// projects, and issues without a project.
var (
	QueryIssuesUpdatedSince          = updatedSinceQuery("ItervoxLinearChanges", ", $projectSlug: String!", "project: {slugId: {eq: $projectSlug}}, ")
	QueryIssuesUpdatedSinceAll       = updatedSinceQuery("ItervoxLinearChangesAll", "", "")
	QueryIssuesUpdatedSinceNoProject = updatedSinceQuery("ItervoxLinearChangesNoProject", "", "project: {null: true}, ")
)

// FetchIssuesUpdatedSince implements tracker.IncrementalFetcher (#114) over
// the same scope as FetchCandidateIssues (the runtime project filter, else
// the assignee or project_slug, else every issue): one paginated request per project in the
// filter. A trashed or archived issue is reported gone. An issue moved to a
// project outside the scope does not appear: the store's full pull catches
// it.
func (c *Client) FetchIssuesUpdatedSince(ctx context.Context, since time.Time) ([]domain.Issue, []string, error) {
	base := map[string]any{"since": since.UTC().Format(time.RFC3339Nano), "relationFirst": pageSize}
	with := func(k string, v any) map[string]any {
		m := maps.Clone(base)
		m[k] = v
		return m
	}
	sc := c.currentScope()
	switch {
	case sc.assignee:
		// #182: the issues assigned to the scope's user. One reassigned
		// away does not appear (it is no longer assigned); the store's full
		// pull catches it, as it catches a move to another project.
		params, filter, vars, err := c.assigneeFilter(ctx)
		if err != nil {
			return nil, nil, err
		}
		maps.Copy(vars, base)
		return c.fetchUpdatedPages(ctx, updatedSinceQuery("ItervoxLinearChangesAssignee", params, filter), vars)
	case sc.all:
		return c.fetchUpdatedPages(ctx, QueryIssuesUpdatedSinceAll, base)
	}
	var changed []domain.Issue
	var gone []string
	seen := map[string]struct{}{}
	for _, slug := range sc.slugs {
		query, vars := QueryIssuesUpdatedSince, with("projectSlug", slug)
		if slug == noProjectSentinel {
			query, vars = QueryIssuesUpdatedSinceNoProject, base
		}
		ch, g, err := c.fetchUpdatedPages(ctx, query, vars)
		if err != nil {
			return nil, nil, err
		}
		for _, iss := range ch {
			if _, dup := seen[iss.ID]; !dup {
				seen[iss.ID] = struct{}{}
				changed = append(changed, iss)
			}
		}
		gone = append(gone, g...)
	}
	return changed, gone, nil
}

// fetchUpdatedPages pages through one updated-since query.
func (c *Client) fetchUpdatedPages(ctx context.Context, query string, baseVars map[string]any) ([]domain.Issue, []string, error) {
	var changed []domain.Issue
	var gone []string
	var cursor *string
	for range maxPaginationPages {
		vars := maps.Clone(baseVars)
		vars["first"] = pageSize
		vars["after"] = cursor
		body, err := c.graphql(ctx, query, vars)
		if err != nil {
			return nil, nil, err
		}
		data, ok := body["data"].(map[string]any)
		if !ok {
			return nil, nil, decodeError(body)
		}
		block, ok := data["issues"].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("linear_unknown_payload: missing issues block")
		}
		nodes, ok := block["nodes"].([]any)
		if !ok {
			return nil, nil, fmt.Errorf("linear_unknown_payload: missing nodes")
		}
		for _, n := range nodes {
			node, ok := n.(map[string]any)
			if !ok {
				continue
			}
			if isGone(node) {
				if id, _ := node["id"].(string); id != "" {
					gone = append(gone, id)
				}
				continue
			}
			if issue := normalizeIssue(node); issue != nil {
				changed = append(changed, *issue)
			}
		}
		pi := extractPageInfo(block)
		if !pi.HasNextPage {
			return changed, gone, nil
		}
		if pi.EndCursor == "" {
			return nil, nil, fmt.Errorf("linear_missing_end_cursor: hasNextPage=true but endCursor is empty")
		}
		cursor = &pi.EndCursor
	}
	return nil, nil, fmt.Errorf("linear_pagination_limit: exceeded %d pages", maxPaginationPages)
}

// isGone reports a trashed or archived issue.
func isGone(node map[string]any) bool {
	if trashed, _ := node["trashed"].(bool); trashed {
		return true
	}
	archived, _ := node["archivedAt"].(string)
	return strings.TrimSpace(archived) != ""
}

var _ tracker.IncrementalFetcher = (*Client)(nil)
