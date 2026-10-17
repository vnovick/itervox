package tracker

import (
	"context"
	"errors"
	"slices"
	"strings"
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
