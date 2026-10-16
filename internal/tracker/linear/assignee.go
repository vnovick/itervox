package linear

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/tracker"
)

// The assignee scope (#182): instead of one project, the tracker reads the
// issues assigned to one user ("me" = the API key's own user, a user id or
// an email), in any project of any team the key can read, optionally
// narrowed to one team. Every read path — the candidate poll and
// FetchIssuesByStates (and with them the dependency analyzer's issue set and
// the automation poll), the incremental pull, and the by-ID scope check the
// webhook refresh uses — builds its filter from readScope, so they cannot
// disagree about which issues are in scope.

var (
	_ tracker.AssigneeScoper = (*Client)(nil)
)

// assigneeMe is the assignee spec for the API key's own user.
const assigneeMe = "me"

// readScope is the scope one read uses, derived from the runtime filter and
// the configured project_slug / assignee.
type readScope struct {
	assignee bool
	all      bool
	slugs    []string // project slugs, noProjectSentinel for "no project"
}

// currentScope resolves the scope the next read uses. A nil runtime filter
// falls back to WORKFLOW.md: the assignee when configured, else
// project_slug, else every project. A filter holding
// tracker.AssigneeScopeFilter is the assignee scope.
func (c *Client) currentScope() readScope {
	filter := c.GetProjectFilter()
	if filter == nil {
		switch slug := strings.TrimSpace(c.cfg.ProjectSlug); {
		case strings.TrimSpace(c.cfg.Assignee) != "":
			return readScope{assignee: true}
		case slug != "" && slug != "*":
			return readScope{slugs: []string{slug}}
		}
		return readScope{all: true}
	}
	if slices.Contains(filter, tracker.AssigneeScopeFilter) {
		return readScope{assignee: true}
	}
	if len(filter) == 0 {
		return readScope{all: true}
	}
	return readScope{slugs: filter}
}

// AssigneeSpec implements tracker.AssigneeScoper.
func (c *Client) AssigneeSpec() string {
	if a := strings.TrimSpace(c.cfg.Assignee); a != "" {
		return a
	}
	return assigneeMe
}

// ResolvedAssignee implements tracker.AssigneeScoper.
func (c *Client) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	c.assigneeMu.Lock()
	defer c.assigneeMu.Unlock()
	if c.assignee == nil {
		return tracker.AssigneeUser{}, false
	}
	return *c.assignee, true
}

// ResolveAssignee implements tracker.AssigneeScoper: "me" through Linear's
// viewer, an email through users(filter: {email}), anything else as a user
// id. The answer is cached for the life of the client (a WORKFLOW.md reload
// builds a new client, and so resolves again).
func (c *Client) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	if u, ok := c.ResolvedAssignee(); ok {
		return u, nil
	}
	spec := c.AssigneeSpec()
	u, err := c.lookupUser(ctx, spec)
	if err != nil {
		return tracker.AssigneeUser{}, fmt.Errorf("linear: resolve assignee %q: %w", spec, err)
	}
	c.assigneeMu.Lock()
	c.assignee = &u
	c.assigneeMu.Unlock()
	return u, nil
}

const (
	queryViewerUser = `query ItervoxAssigneeViewer { viewer { id name email } }`
	queryUserByMail = `query ItervoxAssigneeByEmail($email: String!) {
  users(filter: {email: {eq: $email}}, first: 2) { nodes { id name email } }
}`
	queryUserByID = `query ItervoxAssigneeById($id: String!) { user(id: $id) { id name email } }`
)

// lookupUser asks Linear who spec is.
func (c *Client) lookupUser(ctx context.Context, spec string) (tracker.AssigneeUser, error) {
	var (
		query string
		vars  map[string]any
		path  []string
	)
	switch {
	case strings.EqualFold(spec, assigneeMe):
		query, path = queryViewerUser, []string{"viewer"}
	case strings.Contains(spec, "@"):
		query, vars, path = queryUserByMail, map[string]any{"email": spec}, []string{"users"}
	default:
		query, vars, path = queryUserByID, map[string]any{"id": spec}, []string{"user"}
	}
	body, err := c.graphql(ctx, query, vars)
	if err != nil {
		return tracker.AssigneeUser{}, err
	}
	data, ok := body["data"].(map[string]any)
	if !ok || data == nil {
		err := decodeError(body)
		var nf *tracker.NotFoundError
		if errors.As(err, &nf) {
			return tracker.AssigneeUser{}, tracker.ErrAssigneeNotFound
		}
		return tracker.AssigneeUser{}, err
	}
	node, _ := data[path[0]].(map[string]any)
	if path[0] == "users" {
		nodes, _ := node["nodes"].([]any)
		node = nil
		if len(nodes) > 0 {
			node, _ = nodes[0].(map[string]any)
		}
	}
	if node == nil {
		if errs, ok := body["errors"]; ok && !graphQLErrorsIndicateNotFound(errs) {
			return tracker.AssigneeUser{}, &tracker.GraphQLError{Message: fmt.Sprintf("%v", errs)}
		}
		return tracker.AssigneeUser{}, tracker.ErrAssigneeNotFound
	}
	u := tracker.AssigneeUser{ID: stringValue(node["id"]), Name: stringValue(node["name"]), Email: stringValue(node["email"])}
	if u.ID == "" {
		return tracker.AssigneeUser{}, tracker.ErrAssigneeNotFound
	}
	return u, nil
}

// assigneeFilter returns the GraphQL parameter declarations, the issue
// filter fragment (ending in ", ") and the variables of the assignee scope,
// resolving the assignee first if it is not cached. A scope whose user
// cannot be resolved fails the read: it never widens to every issue.
func (c *Client) assigneeFilter(ctx context.Context) (params, filter string, vars map[string]any, err error) {
	u, err := c.ResolveAssignee(ctx)
	if err != nil {
		return "", "", nil, err
	}
	params = ", $assigneeId: ID!"
	filter = "assignee: {id: {eq: $assigneeId}}, "
	vars = map[string]any{"assigneeId": u.ID}
	if team := strings.TrimSpace(c.cfg.Team); team != "" {
		if uuidRe.MatchString(team) {
			params += ", $team: ID!"
			filter += "team: {id: {eq: $team}}, "
		} else {
			params += ", $team: String!"
			filter += "team: {key: {eq: $team}}, "
		}
		vars["team"] = team
	}
	return params, filter, vars, nil
}

// assigneeCovers reports whether a raw issue node (read with its assignee
// and team) is in the assignee scope u.
func (c *Client) assigneeCovers(u tracker.AssigneeUser, node map[string]any) bool {
	a, _ := node["assignee"].(map[string]any)
	if a == nil || stringValue(a["id"]) != u.ID {
		return false
	}
	team := strings.TrimSpace(c.cfg.Team)
	if team == "" {
		return true
	}
	t, _ := node["team"].(map[string]any)
	return t != nil && (stringValue(t["id"]) == team || strings.EqualFold(stringValue(t["key"]), team))
}

// ScopeKey implements tracker.AssigneeScoper.
func (c *Client) ScopeKey() string {
	sc := c.currentScope()
	switch {
	case sc.assignee:
		who := "spec:" + c.AssigneeSpec()
		if u, ok := c.ResolvedAssignee(); ok {
			who = "id:" + u.ID
		}
		return "assignee:" + who + "|team:" + strings.TrimSpace(c.cfg.Team)
	case sc.all:
		return "all"
	}
	sorted := slices.Clone(sc.slugs)
	slices.Sort(sorted)
	return "projects:" + strings.Join(sorted, "\x00")
}

// ScopeLabel implements tracker.AssigneeScoper.
func (c *Client) ScopeLabel() string {
	sc := c.currentScope()
	switch {
	case sc.assignee:
		return "assigned to " + c.AssigneeLabel()
	case sc.all:
		return "all projects"
	}
	names := make([]string, 0, len(sc.slugs))
	for _, s := range sc.slugs {
		if s == noProjectSentinel {
			s = "no project"
		}
		names = append(names, s)
	}
	if len(names) == 1 {
		return "project " + names[0]
	}
	return "projects " + strings.Join(names, ", ")
}

// AssigneeLabel names who the assignee scope means: the resolved user's
// name (or the spec before resolution), with the team when narrowed.
func (c *Client) AssigneeLabel() string {
	who := c.AssigneeSpec()
	if u, ok := c.ResolvedAssignee(); ok {
		who = u.Label()
	}
	if team := strings.TrimSpace(c.cfg.Team); team != "" {
		who += " · team " + team
	}
	return who
}
