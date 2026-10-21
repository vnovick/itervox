package tracker

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
)

// ErrAssigneeNotFound is returned when an assignee (an email or user id)
// matches no user the API key can see (#182).
var ErrAssigneeNotFound = errors.New("tracker: assignee not found")

// AssigneeUser is a tracker user an assignee resolved to.
type AssigneeUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// Label is the user's display name, else email, else id.
func (u AssigneeUser) Label() string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return u.ID
}

// Scope is a tracker's read scope (#182): the AND of a project filter, an
// assignee and a label filter, each optional.
type Scope struct {
	// Projects are the project slugs read (any of them); empty reads every
	// project. "__no_project__" stands for issues without a project.
	Projects []string `json:"projects"`
	// Assignee is "" (anyone), "me", a user id or an email.
	Assignee string `json:"assignee"`
	// Labels: an issue carrying ANY of them is in scope; empty is no label
	// filter.
	Labels []string `json:"labels"`
}

// Key is a canonical form of the scope: equal keys read the same issues
// (order and label case aside).
func (s Scope) Key() string {
	projects := slices.Clone(s.Projects)
	slices.Sort(projects)
	labels := make([]string, 0, len(s.Labels))
	for _, l := range s.Labels {
		labels = append(labels, strings.ToLower(l))
	}
	slices.Sort(labels)
	return "projects:" + strings.Join(projects, "\x00") + "|assignee:" + s.Assignee + "|labels:" + strings.Join(labels, "\x00")
}

// ScopeManager is an optional interface implemented by adapters whose read
// scope combines a project, an assignee and labels (#182, Linear). Callers
// type-assert Tracker to it; the tracker store forwards it. It extends the
// project filter (ProjectManager): SetProjectFilter changes only the project
// part, SetScope all three.
type ScopeManager interface {
	// Scope is the effective scope: the runtime one where set, else the
	// configured one.
	Scope() Scope
	// SetScope replaces the runtime scope. Resolve a new assignee with
	// ResolveUser first: an unresolvable one fails every read.
	SetScope(Scope)
	// ResolveUser resolves an assignee spec to a user and caches it. It
	// makes a tracker request on a cache miss: call it off the event loop.
	ResolveUser(ctx context.Context, spec string) (AssigneeUser, error)
	// ResolveAssignee resolves the current scope's assignee (an error when
	// the scope has none), with ResolveUser's caching.
	ResolveAssignee(ctx context.Context) (AssigneeUser, error)
	// ResolvedAssignee is the current assignee's cached user; ok is false
	// when the scope has no assignee or it is not resolved yet.
	ResolvedAssignee() (AssigneeUser, bool)
	// FetchLabels lists the label names the key can see (the filter's
	// choices).
	FetchLabels(ctx context.Context) ([]string, error)
	// FetchUsers lists the users the key can see (the assignee choices).
	FetchUsers(ctx context.Context) ([]AssigneeUser, error)
	// ScopeKey identifies the effective scope, including the resolved
	// assignee and the team: it changes exactly when the set of issues the
	// adapter reads can change.
	ScopeKey() string
	// ScopeLabel describes the effective scope for people, e.g.
	// "ENG · assigned to me · label: agent".
	ScopeLabel() string
}

// Team is a tracker team (Linear), a filter's teams choice (#198).
type Team struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// FilterPreview is what a filter would read now (#198): how many issues in
// the active states match it, and the first of them.
type FilterPreview struct {
	Count       int      `json:"count"`
	Identifiers []string `json:"identifiers"`
	// Truncated is set when Count stopped at the preview's page limit.
	Truncated bool `json:"truncated,omitempty"`
}

// ScopeMatcher evaluates a tracker's effective filter in memory (#198); the
// orchestrator uses it on a retry's unscoped by-ID read. It makes no
// request (assignees resolve through the cache), so it is safe on the
// event loop.
type ScopeMatcher interface {
	// MatchesFilter evaluates the effective filter on an issue read with
	// its filter fields (see domain.Issue). known is false when the answer
	// depends on an assignee not resolved yet.
	MatchesFilter(issue domain.Issue) (match, known bool)
}

// FilterManager is an optional interface implemented by adapters whose read
// scope is a tracker filter (#198): groups of conditions, ANDed inside a
// group and ORed between groups (issuefilter.Filter). Linear and GitHub
// implement it; the tracker store forwards it. Every read path — the
// candidate poll, FetchIssuesByStates, the incremental pull and the by-ID
// scope check — reads through the same compiled filter, and MatchesFilter
// evaluates it in memory with the same meaning.
type FilterManager interface {
	ScopeMatcher
	// Filter is the effective filter: the runtime one where set, else
	// tracker.filter, else the legacy keys as one implicit group (nil:
	// every issue).
	Filter() issuefilter.Filter
	// FilterMode reports whether a filter (tracker.filter or a saved one)
	// is in effect rather than the legacy keys.
	FilterMode() bool
	// SetFilter replaces the runtime filter; an empty one reads every
	// issue. Resolve its assignees first (ResolveFilter): an unresolvable
	// one fails every read.
	SetFilter(issuefilter.Filter)
	// FilterConditions lists the conditions the adapter supports.
	FilterConditions() []string
	// ResolveFilter resolves every assignee f names, caching them (a
	// tracker request on a miss: call it off the event loop).
	ResolveFilter(ctx context.Context, f issuefilter.Filter) error
	// PreviewFilter counts the issues in the active states f reads, up to
	// limit identifiers, without changing the scope.
	PreviewFilter(ctx context.Context, f issuefilter.Filter, limit int) (FilterPreview, error)
	// FetchTeams lists the teams the key can read (none for GitHub).
	FetchTeams(ctx context.Context) ([]Team, error)
	FetchLabels(ctx context.Context) ([]string, error)
	FetchUsers(ctx context.Context) ([]AssigneeUser, error)
	ScopeKey() string
	ScopeLabel() string
}
