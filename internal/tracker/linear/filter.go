package linear

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// The tracker filter (#198) generalises the #182 scope: groups of
// conditions, ANDed inside a group and ORed between groups. The legacy keys
// (project_slug or the runtime project filter, assignee narrowed by team,
// labels) are one implicit group. Every list read compiles the effective
// filter to ONE IssueFilter variable, {and: [<state or updatedAt>, {or:
// [<group>, ...]}]}, so the candidate poll, FetchIssuesByStates (and with
// them the store's views, the dependency analyzer and the automation poll)
// and the incremental pull read the same set; the by-ID scope check (the
// webhook refresh) evaluates the same filter in memory with
// issuefilter.Match. A legacy scope with neither an assignee nor labels
// keeps the original project-only queries.

var _ tracker.FilterManager = (*Client)(nil)

// FilterConditions implements tracker.FilterManager: Linear supports every
// condition.
func (c *Client) FilterConditions() []string { return issuefilter.SupportedBy("linear") }

// effectiveFilter is the runtime filter where set, else tracker.filter,
// else the legacy keys as one group; mode is false for the legacy keys.
func (c *Client) effectiveFilter() (f issuefilter.Filter, mode bool) {
	c.projectFilterMu.RLock()
	ov := c.filterOverride
	c.projectFilterMu.RUnlock()
	switch {
	case ov != nil && len(*ov) > 0:
		return slices.Clone(*ov), true
	case ov == nil && len(c.cfg.Filter) > 0:
		return slices.Clone(c.cfg.Filter), true
	}
	sc := c.Scope()
	return issuefilter.FromLegacy(sc.Projects, sc.Assignee, c.cfg.Team, sc.Labels), false
}

// Filter implements tracker.FilterManager.
func (c *Client) Filter() issuefilter.Filter { f, _ := c.effectiveFilter(); return f }

// FilterMode implements tracker.FilterManager.
func (c *Client) FilterMode() bool { _, mode := c.effectiveFilter(); return mode }

// SetFilter implements tracker.FilterManager. A non-empty filter replaces
// the legacy scope; an empty one reads every issue (the legacy parts are
// cleared too, as the save comments their keys out).
func (c *Client) SetFilter(f issuefilter.Filter) {
	f = f.Normalize()
	if f == nil {
		f = issuefilter.Filter{}
	}
	c.projectFilterMu.Lock()
	defer c.projectFilterMu.Unlock()
	c.filterOverride = &f
	if len(f) == 0 {
		none, anyone, noLabels := []string{}, "", []string{}
		c.projectFilter, c.assigneeOverride, c.labelsOverride = &none, &anyone, &noLabels
	}
}

// ResolveFilter implements tracker.FilterManager.
func (c *Client) ResolveFilter(ctx context.Context, f issuefilter.Filter) error {
	for _, spec := range f.Assignees() {
		if _, err := c.ResolveUser(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

// matchEnv is the in-memory matcher's environment: cached assignees and
// the state lists.
func (c *Client) matchEnv() issuefilter.Env {
	c.statesMu.RLock()
	active := slices.Clone(c.cfg.ActiveStates)
	c.statesMu.RUnlock()
	return issuefilter.Env{
		Assignee: func(spec string) (string, bool) {
			u, ok := c.cachedUser(spec)
			return u.ID, ok
		},
		ActiveStates: active,
		WorkingState: c.cfg.WorkingState,
	}
}

// MatchesFilter implements tracker.FilterManager.
func (c *Client) MatchesFilter(issue domain.Issue) (match, known bool) {
	return c.Filter().Match(issue, c.matchEnv())
}

// compiledScope is the effective scope compiled to the groups' {or: [...]}
// IssueFilter, or ok false for a legacy project-only scope (served by the
// project queries). Assignees are resolved first: a scope whose user cannot
// be resolved fails the read and never widens.
func (c *Client) compiledScope(ctx context.Context) (groups map[string]any, ok bool, err error) {
	f, mode := c.effectiveFilter()
	if !mode && !narrowed(c.Scope()) {
		return nil, false, nil
	}
	groups, err = c.compileFilter(ctx, f)
	return groups, err == nil, err
}

// compileFilter compiles f to {or: [{and: [...]}, ...]}.
func (c *Client) compileFilter(ctx context.Context, f issuefilter.Filter) (map[string]any, error) {
	if err := c.ResolveFilter(ctx, f); err != nil {
		return nil, err
	}
	env := c.matchEnv()
	groups := make([]any, 0, len(f))
	for _, g := range f {
		groups = append(groups, compileGroup(g, env))
	}
	return map[string]any{"or": groups}, nil
}

// compileGroup compiles one group to {and: [...]}, one element per
// condition. labels_all is one `some` per label: `every` would mean "every
// label of the issue is in the set", which an issue carrying more labels
// fails and an unlabelled one passes. labels_none is `every` label outside
// the set, or no labels at all (an explicit length 0, so the answer does
// not hang on how an empty `every` evaluates).
func compileGroup(g issuefilter.Group, env issuefilter.Env) map[string]any {
	and := []any{}
	if len(g.Projects) > 0 {
		var slugs []string
		var parts []any
		for _, p := range g.Projects {
			if p != issuefilter.NoProject {
				slugs = append(slugs, p)
			}
		}
		if len(slugs) > 0 {
			parts = append(parts, map[string]any{"project": map[string]any{"slugId": map[string]any{"in": slugs}}})
		}
		if slices.Contains(g.Projects, issuefilter.NoProject) {
			parts = append(parts, map[string]any{"project": map[string]any{"null": true}})
		}
		and = append(and, anyOf(parts))
	}
	if len(g.Teams) > 0 {
		var keys, ids []string
		for _, t := range g.Teams {
			if uuidRe.MatchString(t) {
				ids = append(ids, t)
			} else {
				keys = append(keys, t)
			}
		}
		var parts []any
		if len(keys) > 0 {
			parts = append(parts, map[string]any{"team": map[string]any{"key": map[string]any{"in": keys}}})
		}
		if len(ids) > 0 {
			parts = append(parts, map[string]any{"team": map[string]any{"id": map[string]any{"in": ids}}})
		}
		and = append(and, anyOf(parts))
	}
	if g.Assignee != "" {
		id, _ := env.Assignee(g.Assignee)
		and = append(and, map[string]any{"assignee": map[string]any{"id": map[string]any{"eq": id}}})
	}
	if len(g.LabelsAny) > 0 {
		and = append(and, map[string]any{"labels": map[string]any{"some": map[string]any{"name": map[string]any{"in": g.LabelsAny}}}})
	}
	for _, l := range g.LabelsAll {
		and = append(and, map[string]any{"labels": map[string]any{"some": map[string]any{"name": map[string]any{"eq": l}}}})
	}
	if len(g.LabelsNone) > 0 {
		and = append(and, map[string]any{"or": []any{
			map[string]any{"labels": map[string]any{"length": map[string]any{"eq": 0}}},
			map[string]any{"labels": map[string]any{"every": map[string]any{"name": map[string]any{"nin": g.LabelsNone}}}},
		}})
	}
	if len(g.Priority) > 0 {
		and = append(and, map[string]any{"priority": map[string]any{"in": g.Priority}})
	}
	if len(g.States) > 0 {
		// An issue in an active state only in one of the states (or the
		// working state); any other state is not narrowed.
		allowed := slices.Clone(g.States)
		if env.WorkingState != "" {
			allowed = append(allowed, env.WorkingState)
		}
		parts := []any{map[string]any{"state": map[string]any{"name": map[string]any{"in": allowed}}}}
		if len(env.ActiveStates) > 0 {
			parts = append(parts, map[string]any{"state": map[string]any{"name": map[string]any{"nin": env.ActiveStates}}})
		}
		and = append(and, anyOf(parts))
	}
	switch {
	case g.Cycle == issuefilter.CycleCurrent:
		and = append(and, map[string]any{"cycle": map[string]any{"isActive": map[string]any{"eq": true}}})
	case g.Cycle != "":
		and = append(and, map[string]any{"cycle": map[string]any{"id": map[string]any{"eq": g.Cycle}}})
	}
	if g.Milestone != "" {
		and = append(and, map[string]any{"projectMilestone": map[string]any{"name": map[string]any{"eqIgnoreCase": g.Milestone}}})
	}
	return map[string]any{"and": and}
}

func anyOf(parts []any) any {
	if len(parts) == 1 {
		return parts[0]
	}
	return map[string]any{"or": parts}
}

// filteredPollQuery is a paginated issue read over one IssueFilter
// variable.
func filteredPollQuery(name string) string {
	return "query " + name + "($filter: IssueFilter!, $first: Int!, $relationFirst: Int!, $after: String) {\n" +
		"  issues(filter: $filter, first: $first, after: $after) {" + candidateNodeFields + "\n  }\n}"
}

// filteredChangesQuery is the incremental pull over one IssueFilter
// variable, archived and trashed issues included.
func filteredChangesQuery(name string) string {
	return "query " + name + "($filter: IssueFilter!, $first: Int!, $relationFirst: Int!, $after: String) {\n" +
		"  issues(filter: $filter, includeArchived: true, orderBy: updatedAt, first: $first, after: $after) {" +
		updatedSinceNodeFields + "\n  }\n}"
}

// pollFilter is the full poll's IssueFilter: the states AND the groups.
func pollFilter(states []string, groups map[string]any) map[string]any {
	return map[string]any{"and": []any{
		map[string]any{"state": map[string]any{"name": map[string]any{"in": states}}},
		groups,
	}}
}

// PreviewFilter implements tracker.FilterManager: f over the active states,
// through the same compiled query the poll uses.
func (c *Client) PreviewFilter(ctx context.Context, f issuefilter.Filter, limit int) (tracker.FilterPreview, error) {
	f = f.Normalize()
	c.statesMu.RLock()
	active := slices.Clone(c.cfg.ActiveStates)
	c.statesMu.RUnlock()
	var issues []domain.Issue
	var err error
	if len(f) == 0 {
		issues, err = c.fetchByStatesPage(ctx, QueryCandidateIssuesAll, map[string]any{"stateNames": active, "relationFirst": pageSize}, nil, nil)
	} else {
		var groups map[string]any
		if groups, err = c.compileFilter(ctx, f); err == nil {
			issues, err = c.fetchByStatesPage(ctx, filteredPollQuery("ItervoxLinearPollScoped"),
				map[string]any{"filter": pollFilter(active, groups), "relationFirst": pageSize}, nil, nil)
		}
	}
	if err != nil {
		return tracker.FilterPreview{}, fmt.Errorf("linear_filter_preview: %w", err)
	}
	p := tracker.FilterPreview{Count: len(issues), Identifiers: []string{}}
	for _, is := range issues[:min(len(issues), max(limit, 0))] {
		p.Identifiers = append(p.Identifiers, is.Identifier)
	}
	return p, nil
}

const queryTeams = `query ItervoxScopeTeams { teams(first: 250) { nodes { id key name } } }`

// FetchTeams implements tracker.FilterManager: the teams the key can read,
// by key.
func (c *Client) FetchTeams(ctx context.Context) ([]tracker.Team, error) {
	body, err := c.graphql(ctx, queryTeams, nil)
	if err != nil {
		return nil, fmt.Errorf("linear_fetch_teams: %w", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeError(body)
	}
	nodes, _ := nodesOf(data, "teams")
	out := make([]tracker.Team, 0, len(nodes))
	for _, n := range nodes {
		if id := stringValue(n["id"]); id != "" {
			out = append(out, tracker.Team{ID: id, Key: stringValue(n["key"]), Name: stringValue(n["name"])})
		}
	}
	slices.SortFunc(out, func(a, b tracker.Team) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// filterScopeKey is ScopeKey for a filter: the filter's canonical key plus
// each assignee's resolved id.
func (c *Client) filterScopeKey(f issuefilter.Filter) string {
	key := "filter:" + f.Key()
	specs := f.Assignees()
	slices.Sort(specs)
	for _, spec := range specs {
		if u, ok := c.cachedUser(spec); ok {
			key += "|id:" + spec + "=" + u.ID
		}
	}
	return key
}
