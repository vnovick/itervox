package linear

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// Issue editing for the local issue sync (#118): create an issue in the
// configured project without a source issue, and edit its title,
// description, labels, priority and blockers.

var _ tracker.IssueEditor = (*Client)(nil)

// queryProjectContext resolves project_slug to the project and its first
// team, with the team's workflow states and labels.
const queryProjectContext = `
query ItervoxSyncProjectContext($slug: String!) {
  projects(filter: {slugId: {eq: $slug}}, first: 1) {
    nodes {
      id
      teams(first: 1) {
        nodes {
          id
          states(first: 100) { nodes { id name } }
          labels(first: 250) { nodes { id name } }
        }
      }
    }
  }
  issueLabels(first: 250, filter: {team: {null: true}}) { nodes { id name } }
}`

// queryIssueEditContext reads what an edit needs: the issue's team labels
// and its current blocking relations.
const queryIssueEditContext = `
query ItervoxSyncIssueContext($id: String!) {
  issue(id: $id) {
    id
    team { id labels(first: 250) { nodes { id name } } }
    inverseRelations(first: 100) { nodes { id type issue { id } } }
  }
  issueLabels(first: 250, filter: {team: {null: true}}) { nodes { id name } }
}`

const mutationIssueUpdateFields = `
mutation ItervoxSyncIssueUpdate($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) { success }
}`

const mutationRelationCreate = `
mutation ItervoxSyncRelationCreate($input: IssueRelationCreateInput!) {
  issueRelationCreate(input: $input) { success }
}`

const mutationRelationDelete = `
mutation ItervoxSyncRelationDelete($id: String!) {
  issueRelationDelete(id: $id) { success }
}`

const queryIssueByKey = `
query ItervoxSyncIssueByKey($key: ID!) {
  issues(filter: {id: {eq: $key}}, first: 1, includeArchived: true) {
    nodes {
      id identifier title description priority state { name } branchName url trashed
      labels { nodes { name } }
      createdAt updatedAt
    }
  }
}`

// CreateScopedIssue implements tracker.IssueEditor: it creates the issue in
// project_slug's project (and that project's first team), with draft.Key as
// the issue's own id so a retry cannot create a second one.
func (c *Client) CreateScopedIssue(ctx context.Context, draft tracker.IssueDraft) (*domain.Issue, error) {
	slug := strings.TrimSpace(c.cfg.ProjectSlug)
	if slug == "" {
		return nil, errors.New("linear_create_scoped_issue: tracker.sync needs project_slug to create issues")
	}
	resp, err := c.graphql(ctx, queryProjectContext, map[string]any{"slug": slug})
	if err != nil {
		return nil, fmt.Errorf("linear_create_scoped_issue: resolve project: %w", err)
	}
	data, _ := resp["data"].(map[string]any)
	projects, _ := nodesOf(data, "projects")
	if len(projects) == 0 {
		return nil, fmt.Errorf("linear_create_scoped_issue: project %q not found", slug)
	}
	project := projects[0]
	teams, _ := nodesOf(project, "teams")
	if len(teams) == 0 {
		return nil, fmt.Errorf("linear_create_scoped_issue: project %q has no team", slug)
	}
	team := teams[0]
	input := map[string]any{
		"teamId":      stringValue(team["id"]),
		"projectId":   stringValue(project["id"]),
		"title":       draft.Title,
		"description": draft.Description,
	}
	if draft.Key != "" {
		input["id"] = draft.Key
	}
	if draft.State != "" {
		states, _ := nodesOf(team, "states")
		id := findByName(states, draft.State)
		if id == "" {
			return nil, fmt.Errorf("linear_create_scoped_issue: state %q not found in the project's team", draft.State)
		}
		input["stateId"] = id
	}
	if draft.Priority != nil {
		input["priority"] = linearPriority(*draft.Priority)
	}
	if len(draft.Labels) > 0 {
		teamLabels, _ := nodesOf(team, "labels")
		wsLabels, _ := nodesOf(data, "issueLabels")
		input["labelIds"] = labelIDs(draft.Labels, append(teamLabels, wsLabels...))
	}

	const mutation = `
mutation ItervoxSyncIssueCreate($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    issue {
      id identifier title description priority state { name } branchName url
      labels { nodes { name } }
      createdAt updatedAt
    }
    success
  }
}`
	created, err := c.graphql(ctx, mutation, map[string]any{"input": input})
	if err != nil {
		return nil, fmt.Errorf("linear_create_scoped_issue: issueCreate: %w", err)
	}
	d, _ := created["data"].(map[string]any)
	ic, _ := d["issueCreate"].(map[string]any)
	node, _ := ic["issue"].(map[string]any)
	if ok, _ := ic["success"].(bool); !ok || node == nil {
		return nil, fmt.Errorf("linear_create_scoped_issue: unexpected response: %v", created)
	}
	issue := normalizeIssue(node)
	if issue == nil {
		return nil, fmt.Errorf("linear_create_scoped_issue: unexpected issue shape: %v", node)
	}
	for _, blocker := range draft.BlockedBy {
		if err := c.createBlocksRelation(ctx, blocker, issue.ID); err != nil {
			return nil, err
		}
	}
	return issue, nil
}

// UpdateIssueFields implements tracker.IssueEditor.
func (c *Client) UpdateIssueFields(ctx context.Context, issueID string, patch tracker.IssueFieldsPatch) error {
	if patch.Empty() {
		return nil
	}
	var issueCtx map[string]any
	var data map[string]any
	if patch.Labels != nil || patch.BlockedBy != nil {
		resp, err := c.graphql(ctx, queryIssueEditContext, map[string]any{"id": issueID})
		if err != nil {
			return fmt.Errorf("linear_update_fields: read issue: %w", err)
		}
		data, _ = resp["data"].(map[string]any)
		issueCtx, _ = data["issue"].(map[string]any)
		if issueCtx == nil {
			return fmt.Errorf("linear_update_fields: issue %q not found", issueID)
		}
	}
	input := map[string]any{}
	if patch.Title != nil {
		input["title"] = *patch.Title
	}
	if patch.Description != nil {
		input["description"] = *patch.Description
	}
	if patch.Priority != nil {
		input["priority"] = linearPriority(*patch.Priority)
	}
	if patch.Labels != nil {
		team, _ := issueCtx["team"].(map[string]any)
		teamLabels, _ := nodesOf(team, "labels")
		wsLabels, _ := nodesOf(data, "issueLabels")
		input["labelIds"] = labelIDs(*patch.Labels, append(teamLabels, wsLabels...))
	}
	if len(input) > 0 {
		resp, err := c.graphql(ctx, mutationIssueUpdateFields, map[string]any{"id": issueID, "input": input})
		if err != nil {
			return fmt.Errorf("linear_update_fields: issueUpdate: %w", err)
		}
		if !mutationOK(resp, "issueUpdate") {
			return fmt.Errorf("linear_update_fields: issueUpdate returned non-success: %v", resp)
		}
	}
	if patch.BlockedBy != nil {
		return c.setBlockers(ctx, issueID, issueCtx, *patch.BlockedBy)
	}
	return nil
}

// FindIssueByKey implements tracker.IssueEditor with a filtered collection
// query, whose empty result is unambiguous (a single-issue query errors on a
// missing id, which cannot be told apart from a failed request).
func (c *Client) FindIssueByKey(ctx context.Context, key string) (*domain.Issue, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	resp, err := c.graphql(ctx, queryIssueByKey, map[string]any{"key": key})
	if err != nil {
		return nil, false, fmt.Errorf("linear_find_issue_by_key: %w", err)
	}
	if errs, ok := resp["errors"].([]any); ok && len(errs) > 0 {
		return nil, false, fmt.Errorf("linear_find_issue_by_key: graphql errors: %v", errs)
	}
	data, _ := resp["data"].(map[string]any)
	issues, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("linear_find_issue_by_key: unexpected response: %v", resp)
	}
	nodes, ok := issues["nodes"].([]any)
	if !ok {
		return nil, false, fmt.Errorf("linear_find_issue_by_key: missing nodes: %v", issues)
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	node, _ := nodes[0].(map[string]any)
	if issue := normalizeIssue(node); issue != nil {
		return issue, true, nil
	}
	return nil, false, fmt.Errorf("linear_find_issue_by_key: unexpected issue shape: %v", nodes[0])
}

// setBlockers makes blockerIDs the issue's complete set of "blocks"
// relations: missing ones are created, extra ones deleted.
func (c *Client) setBlockers(ctx context.Context, issueID string, issueCtx map[string]any, blockerIDs []string) error {
	rels, _ := nodesOf(issueCtx, "inverseRelations")
	have := map[string]string{} // blocker issue ID → relation ID
	for _, r := range rels {
		if stringValue(r["type"]) != "blocks" {
			continue
		}
		if from, ok := r["issue"].(map[string]any); ok {
			have[stringValue(from["id"])] = stringValue(r["id"])
		}
	}
	for _, b := range blockerIDs {
		if _, ok := have[b]; ok {
			continue
		}
		if err := c.createBlocksRelation(ctx, b, issueID); err != nil {
			return err
		}
	}
	for blocker, relID := range have {
		if slices.Contains(blockerIDs, blocker) || relID == "" {
			continue
		}
		resp, err := c.graphql(ctx, mutationRelationDelete, map[string]any{"id": relID})
		if err != nil {
			return fmt.Errorf("linear_update_fields: issueRelationDelete: %w", err)
		}
		if !mutationOK(resp, "issueRelationDelete") {
			return fmt.Errorf("linear_update_fields: issueRelationDelete returned non-success: %v", resp)
		}
	}
	return nil
}

func (c *Client) createBlocksRelation(ctx context.Context, blockerID, issueID string) error {
	resp, err := c.graphql(ctx, mutationRelationCreate, map[string]any{"input": map[string]any{
		"issueId": blockerID, "relatedIssueId": issueID, "type": "blocks",
	}})
	if err != nil {
		return fmt.Errorf("linear_update_fields: issueRelationCreate: %w", err)
	}
	if !mutationOK(resp, "issueRelationCreate") {
		return fmt.Errorf("linear_update_fields: issueRelationCreate returned non-success: %v", resp)
	}
	return nil
}

// linearPriority maps the patch convention (-1 clears) to Linear's (0 is no
// priority).
func linearPriority(p int) int {
	return max(p, 0)
}

// labelIDs resolves label names (ignoring case) to ids; a name with no
// label in the team or workspace is left out.
func labelIDs(names []string, labels []map[string]any) []string {
	ids := []string{}
	for _, n := range names {
		if id := findByName(labels, n); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// findByName returns the id of the node whose name matches name, ignoring
// case and surrounding space.
func findByName(nodes []map[string]any, name string) string {
	for _, n := range nodes {
		if strings.EqualFold(strings.TrimSpace(stringValue(n["name"])), strings.TrimSpace(name)) {
			return stringValue(n["id"])
		}
	}
	return ""
}

// nodesOf returns m[key].nodes as maps.
func nodesOf(m map[string]any, key string) ([]map[string]any, bool) {
	conn, ok := m[key].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := conn["nodes"].([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if n, ok := r.(map[string]any); ok {
			out = append(out, n)
		}
	}
	return out, true
}

func mutationOK(resp map[string]any, field string) bool {
	d, _ := resp["data"].(map[string]any)
	m, _ := d[field].(map[string]any)
	ok, _ := m["success"].(bool)
	return ok
}
