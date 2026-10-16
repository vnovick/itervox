package tracker

import (
	"context"
	"errors"
)

// AssigneeScopeFilter is the project-filter entry that selects the assignee
// scope (#182): a tracker whose runtime filter is [AssigneeScopeFilter]
// reads the issues assigned to its assignee (AssigneeScoper.AssigneeSpec)
// instead of a project's. It is never combined with project slugs.
const AssigneeScopeFilter = "__assignee__"

// ErrAssigneeNotFound is returned when an assignee scope's user (an email or
// user id) matches no user the API key can see (#182).
var ErrAssigneeNotFound = errors.New("tracker: assignee not found")

// AssigneeUser is the user an assignee scope resolved to.
type AssigneeUser struct {
	ID    string
	Name  string
	Email string
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

// AssigneeScoper is an optional interface implemented by adapters whose read
// scope can be an assignee instead of a project (#182, Linear). Callers
// type-assert Tracker to it; the tracker store forwards it.
type AssigneeScoper interface {
	// ResolveAssignee resolves the assignee scope's user and caches it, so
	// later reads cost no extra request. It makes a tracker request on a
	// cache miss: call it off the orchestrator event loop (startup, reload,
	// the scope switch handler).
	ResolveAssignee(ctx context.Context) (AssigneeUser, error)
	// AssigneeSpec is who the assignee scope means: tracker.assignee as
	// configured, or "me" when the tracker was configured with a project.
	AssigneeSpec() string
	// ResolvedAssignee is the cached user, ok false before a resolution.
	ResolvedAssignee() (AssigneeUser, bool)
	// ScopeKey identifies the current read scope: it changes exactly when
	// the set of issues the adapter reads can change (project filter,
	// assignee, team).
	ScopeKey() string
	// ScopeLabel describes the current read scope for people, e.g.
	// "assigned to Ada Lovelace · team ENG", "project my-proj",
	// "all projects".
	ScopeLabel() string
}
