// Package issuefilter is the tracker read filter (#198): a list of groups,
// each the AND of optional conditions, an issue matching the filter when ANY
// group matches. It is the one model behind tracker.filter, the legacy
// project_slug / assignee / team / labels keys (one implicit group) and the
// automation filter's groups. The Linear and GitHub adapters compile it to
// their own query languages; Match evaluates it in memory on a domain.Issue
// for the reads that cannot ask the tracker (a webhook's by-ID refresh, a
// retry's claim check, GitHub's change listing).
//
// It imports only domain and the standard library, so config, the tracker
// adapters, the orchestrator, the server and cmd/itervox can all share it.
package issuefilter

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// NoProject is the projects entry that stands for issues without a project
// (the same sentinel the Linear project filter uses).
const NoProject = "__no_project__"

// CycleCurrent is the cycle condition for the team's active cycle.
const CycleCurrent = "current"

// Condition names, as written in WORKFLOW.md and the API.
const (
	CondProjects   = "projects"
	CondTeams      = "teams"
	CondAssignee   = "assignee"
	CondLabelsAny  = "labels_any"
	CondLabelsAll  = "labels_all"
	CondLabelsNone = "labels_none"
	CondPriority   = "priority"
	CondStates     = "states"
	CondCycle      = "cycle"
	CondMilestone  = "milestone"
)

// AllConditions lists every condition in display order.
var AllConditions = []string{
	CondProjects, CondTeams, CondAssignee, CondLabelsAny, CondLabelsAll,
	CondLabelsNone, CondPriority, CondStates, CondCycle, CondMilestone,
}

// Group is one AND group: every condition set must hold. An unset
// condition does not filter.
type Group struct {
	// Projects: the issue is in any of these projects (Linear slugs;
	// NoProject for issues without one).
	Projects []string `json:"projects,omitempty"`
	// Teams: the issue is in any of these teams (Linear key or id).
	Teams []string `json:"teams,omitempty"`
	// Assignee: "me" (the API key's user), a user id or an email (Linear),
	// or a login (GitHub).
	Assignee string `json:"assignee,omitempty"`
	// LabelsAny: the issue carries at least one of these labels.
	LabelsAny []string `json:"labels_any,omitempty"`
	// LabelsAll: the issue carries every one of these labels.
	LabelsAll []string `json:"labels_all,omitempty"`
	// LabelsNone: the issue carries none of these labels.
	LabelsNone []string `json:"labels_none,omitempty"`
	// Priority: the issue's priority is one of these (Linear: 0 none,
	// 1 urgent, 2 high, 3 normal, 4 low).
	Priority []int `json:"priority,omitempty"`
	// States narrows tracker.active_states: an issue in an active state
	// matches only in one of these (or the working state, so a dispatched
	// issue stays in scope); issues in other states (backlog, terminal) are
	// not narrowed, so the board and the dependency audit still see them.
	States []string `json:"states,omitempty"`
	// Cycle: CycleCurrent (the team's active cycle) or a cycle id.
	Cycle string `json:"cycle,omitempty"`
	// Milestone: a project milestone (Linear) or milestone (GitHub) name.
	Milestone string `json:"milestone,omitempty"`
}

// Filter is the OR of its groups. A nil or empty Filter filters nothing.
type Filter []Group

// Conditions lists the conditions the group sets, in AllConditions order.
func (g Group) Conditions() []string {
	var out []string
	set := map[string]bool{
		CondProjects: len(g.Projects) > 0, CondTeams: len(g.Teams) > 0,
		CondAssignee: g.Assignee != "", CondLabelsAny: len(g.LabelsAny) > 0,
		CondLabelsAll: len(g.LabelsAll) > 0, CondLabelsNone: len(g.LabelsNone) > 0,
		CondPriority: len(g.Priority) > 0, CondStates: len(g.States) > 0,
		CondCycle: g.Cycle != "", CondMilestone: g.Milestone != "",
	}
	for _, c := range AllConditions {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// Empty reports a group with no condition (it would match every issue).
func (g Group) Empty() bool { return len(g.Conditions()) == 0 }

// Conditions lists every condition any group sets.
func (f Filter) Conditions() []string {
	seen := map[string]bool{}
	for _, g := range f {
		for _, c := range g.Conditions() {
			seen[c] = true
		}
	}
	var out []string
	for _, c := range AllConditions {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// Assignees lists the distinct assignee specs the groups name.
func (f Filter) Assignees() []string {
	var out []string
	for _, g := range f {
		if g.Assignee != "" && !slices.Contains(out, g.Assignee) {
			out = append(out, g.Assignee)
		}
	}
	return out
}

// FromLegacy is the implicit group of the legacy keys (#182): projects AND
// assignee (narrowed by team) AND any of labels. Team narrows only an
// assignee, as before. Nil when nothing is set.
func FromLegacy(projects []string, assignee, team string, labels []string) Filter {
	g := Group{Projects: clean(projects, false), Assignee: strings.TrimSpace(assignee), LabelsAny: clean(labels, true)}
	if t := strings.TrimSpace(team); t != "" && g.Assignee != "" {
		g.Teams = []string{t}
	}
	if g.Empty() {
		return nil
	}
	return Filter{g}
}

// Normalize trims every value and drops empty and duplicate entries (labels
// compared case-insensitively); empty groups are kept so Validate can name
// them.
func (f Filter) Normalize() Filter {
	if f == nil {
		return nil
	}
	out := make(Filter, 0, len(f))
	for _, g := range f {
		n := Group{
			Projects: clean(g.Projects, false), Teams: clean(g.Teams, false),
			Assignee:  strings.TrimSpace(g.Assignee),
			LabelsAny: clean(g.LabelsAny, true), LabelsAll: clean(g.LabelsAll, true),
			LabelsNone: clean(g.LabelsNone, true), States: clean(g.States, true),
			Cycle: strings.TrimSpace(g.Cycle), Milestone: strings.TrimSpace(g.Milestone),
		}
		for _, p := range g.Priority {
			if !slices.Contains(n.Priority, p) {
				n.Priority = append(n.Priority, p)
			}
		}
		if strings.EqualFold(n.Cycle, CycleCurrent) {
			n.Cycle = CycleCurrent
		}
		out = append(out, n)
	}
	return out
}

// Validate checks the filter's shape: no empty group, priorities 0-4, an
// assignee that is one user, and no label both required and excluded.
// where names the key in messages (e.g. "tracker.filter").
func (f Filter) Validate(where string) error {
	for i, g := range f {
		at := fmt.Sprintf("%s[%d]", where, i)
		if g.Empty() {
			return fmt.Errorf("%s: a group needs at least one condition (%s); remove the group to read every issue", at, strings.Join(AllConditions, ", "))
		}
		for _, p := range g.Priority {
			if p < 0 || p > 4 {
				return fmt.Errorf("%s.priority: %d is not a priority (0 none, 1 urgent, 2 high, 3 normal, 4 low)", at, p)
			}
		}
		if strings.ContainsAny(g.Assignee, " \t,") {
			return fmt.Errorf("%s.assignee %q is not one user: use me, a user id, an email or a login", at, g.Assignee)
		}
		for _, l := range g.LabelsNone {
			if containsFold(g.LabelsAll, l) {
				return fmt.Errorf("%s: label %q is in both labels_all and labels_none, so the group matches nothing", at, l)
			}
		}
	}
	return nil
}

// Unsupported returns the first condition the filter sets that is not in
// supported, or "".
func (f Filter) Unsupported(supported []string) string {
	for _, c := range f.Conditions() {
		if !slices.Contains(supported, c) {
			return c
		}
	}
	return ""
}

// Key is a canonical form: equal keys select the same issues (order, case
// of labels and duplicate entries aside). "" for an empty filter.
func (f Filter) Key() string {
	if len(f) == 0 {
		return ""
	}
	groups := make([]string, 0, len(f))
	for _, g := range f.Normalize() {
		c := g
		for _, s := range []*[]string{&c.Projects, &c.Teams, &c.LabelsAny, &c.LabelsAll, &c.LabelsNone, &c.States} {
			*s = lowerSorted(*s, s != &c.Projects && s != &c.Teams)
		}
		slices.Sort(c.Priority)
		b, _ := json.Marshal(c)
		groups = append(groups, string(b))
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	return strings.Join(groups, "|")
}

// Label summarises the filter for people, e.g.
// "ENG · agent|ai · ¬blocked  OR  me · urgent".
func (f Filter) Label() string {
	parts := make([]string, 0, len(f))
	for _, g := range f {
		parts = append(parts, g.Label())
	}
	return strings.Join(parts, "  OR  ")
}

// Label summarises one group.
func (g Group) Label() string {
	var p []string
	if len(g.Projects) > 0 {
		names := make([]string, len(g.Projects))
		for i, s := range g.Projects {
			names[i] = s
			if s == NoProject {
				names[i] = "no project"
			}
		}
		p = append(p, strings.Join(names, ","))
	}
	if len(g.Teams) > 0 {
		p = append(p, "team "+strings.Join(g.Teams, ","))
	}
	if g.Assignee != "" {
		p = append(p, g.Assignee)
	}
	if len(g.LabelsAny) > 0 {
		p = append(p, strings.Join(g.LabelsAny, "|"))
	}
	if len(g.LabelsAll) > 0 {
		p = append(p, strings.Join(g.LabelsAll, "+"))
	}
	for _, l := range g.LabelsNone {
		p = append(p, "¬"+l)
	}
	if len(g.Priority) > 0 {
		ps := make([]string, len(g.Priority))
		for i, v := range g.Priority {
			ps[i] = "P" + strconv.Itoa(v)
		}
		p = append(p, strings.Join(ps, ","))
	}
	if len(g.States) > 0 {
		p = append(p, "state "+strings.Join(g.States, ","))
	}
	if g.Cycle != "" {
		p = append(p, "cycle "+g.Cycle)
	}
	if g.Milestone != "" {
		p = append(p, "milestone "+g.Milestone)
	}
	return strings.Join(p, " · ")
}

func clean(in []string, fold bool) []string {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.ContainsFunc(out, func(o string) bool { return o == v || (fold && strings.EqualFold(o, v)) }) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func lowerSorted(in []string, lower bool) []string {
	out := slices.Clone(in)
	if lower {
		for i := range out {
			out[i] = strings.ToLower(out[i])
		}
	}
	slices.Sort(out)
	return out
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(o string) bool { return strings.EqualFold(o, v) })
}

// SupportedBy lists the conditions a tracker kind can evaluate: every one
// for Linear; for GitHub the ones its issue search expresses (projects,
// teams, priority and cycle have no GitHub equivalent). Nil for a kind
// without filter support.
func SupportedBy(kind string) []string {
	switch kind {
	case "linear":
		return slices.Clone(AllConditions)
	case "github":
		return []string{CondAssignee, CondLabelsAny, CondLabelsAll, CondLabelsNone, CondStates, CondMilestone}
	}
	return nil
}
