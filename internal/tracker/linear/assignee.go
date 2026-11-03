package linear

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// The tracker scope (#182) is the AND of three optional filters: the
// project (project_slug, or the runtime project filter), the assignee ("me"
// = the API key's own user, a user id or an email; optionally narrowed to
// one team) and the labels (any of them). Every read path — the candidate
// poll and FetchIssuesByStates (and with them the store's views, the
// dependency analyzer's issue set and the automation poll), the incremental
// pull, and the by-ID scope check the webhook refresh uses — builds its
// filter from the effective filter (filter.go, #198), of which this scope is
// the legacy one-group form, so they cannot disagree about which issues are
// in scope. A scope with neither an assignee nor labels takes the original
// project-only queries unchanged.

var _ tracker.ScopeManager = (*Client)(nil)

// assigneeMe is the assignee spec for the API key's own user.
const assigneeMe = "me"

// Scope implements tracker.ScopeManager: the runtime project filter,
// assignee and labels where set, else WORKFLOW.md's.
func (c *Client) Scope() tracker.Scope {
	c.projectFilterMu.RLock()
	defer c.projectFilterMu.RUnlock()
	var sc tracker.Scope
	if c.projectFilter != nil {
		sc.Projects = slices.Clone(*c.projectFilter)
	} else {
		// The picker persists several projects as "a, b"; each is a
		// project (a Linear slug has no comma).
		for _, slug := range strings.Split(c.cfg.ProjectSlug, ",") {
			if slug = strings.TrimSpace(slug); slug != "" {
				sc.Projects = append(sc.Projects, slug)
			}
		}
	}
	sc.Assignee = strings.TrimSpace(c.cfg.Assignee)
	if c.assigneeOverride != nil {
		sc.Assignee = *c.assigneeOverride
	}
	sc.Labels = slices.Clone(c.cfg.Labels)
	if c.labelsOverride != nil {
		sc.Labels = slices.Clone(*c.labelsOverride)
	}
	return sc
}

// SetScope implements tracker.ScopeManager. Projects set the runtime
// project filter (empty: every project), as SetProjectFilter does.
func (c *Client) SetScope(sc tracker.Scope) {
	projects := append([]string{}, sc.Projects...)
	assignee := strings.TrimSpace(sc.Assignee)
	var labels []string
	for _, l := range sc.Labels {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	c.projectFilterMu.Lock()
	defer c.projectFilterMu.Unlock()
	c.projectFilter = &projects
	c.assigneeOverride = &assignee
	c.labelsOverride = &labels
	// #198: a legacy scope save replaces any filter (the save comments
	// tracker.filter out).
	c.filterOverride = &issuefilter.Filter{}
}

// narrowed reports whether the scope filters beyond projects.
func narrowed(sc tracker.Scope) bool { return sc.Assignee != "" || len(sc.Labels) > 0 }

// ResolveUser implements tracker.ScopeManager: "me" through Linear's viewer,
// an email through users(filter: {email}), anything else as a user id. The
// answer is cached per spec for the life of the client (a WORKFLOW.md
// reload builds a new client, and so resolves again).
func (c *Client) ResolveUser(ctx context.Context, spec string) (tracker.AssigneeUser, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return tracker.AssigneeUser{}, errors.New("linear: no assignee to resolve")
	}
	if u, ok := c.cachedUser(spec); ok {
		return u, nil
	}
	u, err := c.lookupUser(ctx, spec)
	if err != nil {
		return tracker.AssigneeUser{}, fmt.Errorf("linear: resolve assignee %q: %w", spec, err)
	}
	c.assigneeMu.Lock()
	c.users[spec] = u
	c.assigneeMu.Unlock()
	return u, nil
}

func (c *Client) cachedUser(spec string) (tracker.AssigneeUser, bool) {
	c.assigneeMu.Lock()
	defer c.assigneeMu.Unlock()
	u, ok := c.users[spec]
	return u, ok
}

// ResolveAssignee implements tracker.ScopeManager.
func (c *Client) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	return c.ResolveUser(ctx, c.Scope().Assignee)
}

// ResolvedAssignee implements tracker.ScopeManager.
func (c *Client) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	spec := c.Scope().Assignee
	if spec == "" {
		return tracker.AssigneeUser{}, false
	}
	return c.cachedUser(spec)
}

const (
	queryViewerUser = `query ItervoxAssigneeViewer { viewer { id name email } }`
	queryUserByMail = `query ItervoxAssigneeByEmail($email: String!) {
  users(filter: {email: {eq: $email}}, first: 2) { nodes { id name email } }
}`
	queryUserByID = `query ItervoxAssigneeById($id: String!) { user(id: $id) { id name email } }`
	queryUsers    = `query ItervoxScopeUsers { users(first: 250) { nodes { id name email active } } }`
	queryLabels   = `query ItervoxScopeLabels { issueLabels(first: 250) { nodes { name } } }`
)

// lookupUser asks Linear who spec is.
func (c *Client) lookupUser(ctx context.Context, spec string) (tracker.AssigneeUser, error) {
	var (
		query string
		vars  map[string]any
		field string
	)
	switch {
	case strings.EqualFold(spec, assigneeMe):
		query, field = queryViewerUser, "viewer"
	case strings.Contains(spec, "@"):
		query, vars, field = queryUserByMail, map[string]any{"email": spec}, "users"
	default:
		query, vars, field = queryUserByID, map[string]any{"id": spec}, "user"
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
	node, _ := data[field].(map[string]any)
	if field == "users" {
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

// FetchUsers implements tracker.ScopeManager: the active users, by name.
func (c *Client) FetchUsers(ctx context.Context) ([]tracker.AssigneeUser, error) {
	body, err := c.graphql(ctx, queryUsers, nil)
	if err != nil {
		return nil, fmt.Errorf("linear_fetch_users: %w", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeError(body)
	}
	nodes, _ := nodesOf(data, "users")
	out := make([]tracker.AssigneeUser, 0, len(nodes))
	for _, n := range nodes {
		if active, ok := n["active"].(bool); ok && !active {
			continue
		}
		if id := stringValue(n["id"]); id != "" {
			out = append(out, tracker.AssigneeUser{ID: id, Name: stringValue(n["name"]), Email: stringValue(n["email"])})
		}
	}
	slices.SortFunc(out, func(a, b tracker.AssigneeUser) int { return strings.Compare(a.Label(), b.Label()) })
	return out, nil
}

// FetchLabels implements tracker.ScopeManager: the distinct label names
// (workspace and team labels), sorted.
func (c *Client) FetchLabels(ctx context.Context) ([]string, error) {
	body, err := c.graphql(ctx, queryLabels, nil)
	if err != nil {
		return nil, fmt.Errorf("linear_fetch_labels: %w", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeError(body)
	}
	nodes, _ := nodesOf(data, "issueLabels")
	var out []string
	for _, n := range nodes {
		if name := strings.TrimSpace(stringValue(n["name"])); name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ScopeKey implements tracker.ScopeManager.
func (c *Client) ScopeKey() string {
	if f, mode := c.effectiveFilter(); mode {
		return c.filterScopeKey(f)
	}
	sc := c.Scope()
	key := sc.Key()
	if sc.Assignee != "" {
		if u, ok := c.cachedUser(sc.Assignee); ok {
			key += "|id:" + u.ID
		}
		key += "|team:" + strings.TrimSpace(c.cfg.Team)
	}
	return key
}

// ScopeLabel implements tracker.ScopeManager, e.g.
// "ENG · assigned to me · label: agent".
func (c *Client) ScopeLabel() string {
	if f, mode := c.effectiveFilter(); mode {
		return f.Label()
	}
	sc := c.Scope()
	var parts []string
	switch {
	case len(sc.Projects) == 0:
		parts = append(parts, "all projects")
	default:
		names := make([]string, 0, len(sc.Projects))
		for _, s := range sc.Projects {
			if s == noProjectSentinel {
				s = "no project"
			}
			names = append(names, s)
		}
		parts = append(parts, strings.Join(names, ", "))
	}
	if sc.Assignee != "" {
		who := sc.Assignee
		if !strings.EqualFold(who, assigneeMe) {
			if u, ok := c.cachedUser(sc.Assignee); ok {
				who = u.Label()
			}
		}
		if team := strings.TrimSpace(c.cfg.Team); team != "" {
			who += " (team " + team + ")"
		}
		parts = append(parts, "assigned to "+who)
	}
	switch len(sc.Labels) {
	case 0:
	case 1:
		parts = append(parts, "label: "+sc.Labels[0])
	default:
		parts = append(parts, "labels: any of "+strings.Join(sc.Labels, ", "))
	}
	return strings.Join(parts, " · ")
}
