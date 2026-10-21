package issuefilter

import (
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
)

// Env is what Match needs beyond the issue.
type Env struct {
	// Assignee resolves an assignee spec ("me", an id, an email, a login) to
	// the id the issue's Assignees carry; ok is false when it is not
	// resolved. Nil compares specs to the issue's Assignees as they are.
	Assignee func(spec string) (id string, ok bool)
	// ActiveStates and WorkingState give the states condition its meaning
	// (see Group.States). Without ActiveStates (an automation filter) the
	// states condition is plain membership.
	ActiveStates []string
	WorkingState string
}

// Match reports whether the issue matches the filter: an empty filter
// matches everything, otherwise any group must. known is false when a
// group that could have matched names an assignee env cannot resolve, so
// the answer is not definitive (the caller should not act on a false).
func (f Filter) Match(is domain.Issue, env Env) (match, known bool) {
	if len(f) == 0 {
		return true, true
	}
	known = true
	for _, g := range f {
		m, k := g.Match(is, env)
		if m {
			return true, true
		}
		if !k {
			known = false
		}
	}
	return false, known
}

// Match evaluates one group: every condition it sets must hold.
func (g Group) Match(is domain.Issue, env Env) (match, known bool) {
	if len(g.Projects) > 0 && !slices.ContainsFunc(g.Projects, func(p string) bool {
		return p == is.ProjectSlug || (p == NoProject && is.ProjectSlug == "")
	}) {
		return false, true
	}
	if len(g.Teams) > 0 && !slices.ContainsFunc(g.Teams, func(t string) bool {
		return (is.TeamID != "" && t == is.TeamID) || (is.TeamKey != "" && strings.EqualFold(t, is.TeamKey))
	}) {
		return false, true
	}
	if !g.matchLabels(is.Labels) {
		return false, true
	}
	if len(g.Priority) > 0 && (is.Priority == nil || !slices.Contains(g.Priority, *is.Priority)) {
		return false, true
	}
	if len(g.States) > 0 && !g.matchState(is.State, env) {
		return false, true
	}
	if g.Cycle != "" {
		if g.Cycle == CycleCurrent && !is.CycleActive {
			return false, true
		}
		if g.Cycle != CycleCurrent && g.Cycle != is.CycleID {
			return false, true
		}
	}
	if g.Milestone != "" && !strings.EqualFold(g.Milestone, is.Milestone) {
		return false, true
	}
	if g.Assignee != "" {
		id := g.Assignee
		if env.Assignee != nil {
			resolved, ok := env.Assignee(g.Assignee)
			if !ok {
				return false, false
			}
			id = resolved
		}
		if !slices.ContainsFunc(is.Assignees, func(a string) bool { return strings.EqualFold(a, id) }) {
			return false, true
		}
	}
	return true, true
}

// matchLabels checks labels_any, labels_all and labels_none, by name, any
// case.
func (g Group) matchLabels(labels []string) bool {
	has := func(l string) bool { return containsFold(labels, l) }
	if len(g.LabelsAny) > 0 && !slices.ContainsFunc(g.LabelsAny, has) {
		return false
	}
	for _, l := range g.LabelsAll {
		if !has(l) {
			return false
		}
	}
	return !slices.ContainsFunc(g.LabelsNone, has)
}

// matchState: an issue in an active state matches only in one of States or
// the working state; one in any other state is not narrowed.
func (g Group) matchState(state string, env Env) bool {
	if len(env.ActiveStates) == 0 {
		return containsFold(g.States, state) // no state lists: plain membership (automations)
	}
	if containsFold(g.States, state) || (env.WorkingState != "" && strings.EqualFold(env.WorkingState, state)) {
		return true
	}
	return !containsFold(env.ActiveStates, state)
}

// StatesAllowed is the states condition as a state list for a tracker
// query: the group's states plus the working state, or, for an issue not in
// an active state, any state — returned as the list of states (from
// candidates) the group admits.
func (g Group) StatesAllowed(candidates []string, env Env) []string {
	if len(g.States) == 0 {
		return slices.Clone(candidates)
	}
	var out []string
	for _, s := range candidates {
		if g.matchState(s, env) {
			out = append(out, s)
		}
	}
	return out
}
