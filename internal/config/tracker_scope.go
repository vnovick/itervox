package config

import (
	"fmt"
	"strings"
)

// The tracker's read scope (#182) is the AND of three filters, each optional:
// the project (project_slug), the assignee (assignee, narrowed by team) and
// the labels (labels, any-of). None set reads every issue, as before.

// validateTrackerScope checks the scope keys: assignee, team and labels are
// Linear-only, and an assignee is one user (me, an id or an email).
func validateTrackerScope(t TrackerConfig) error {
	assignee := strings.TrimSpace(t.Assignee)
	if t.Kind != "linear" {
		if assignee != "" || strings.TrimSpace(t.Team) != "" || len(t.Labels) > 0 {
			return fmt.Errorf("tracker.assignee, tracker.team and tracker.labels are only supported with tracker.kind: linear (got %q)", t.Kind)
		}
		return nil
	}
	if assignee != "" && strings.ContainsAny(assignee, " \t,") {
		return fmt.Errorf("tracker.assignee %q is not a user: use me, a user id or an email", assignee)
	}
	return nil
}

// ScopeName is a short, stable name for the tracker scope, for keying
// per-project state (the logs directory, the run-history key): the project
// slug when one is set (so adding an assignee or labels keeps the
// directory), else "assignee-<who>[-<team>]" for an assignee-only scope,
// else "" (no single project or user).
func (t TrackerConfig) ScopeName() string {
	if slug := strings.TrimSpace(t.ProjectSlug); slug != "" {
		return slug
	}
	if a := strings.TrimSpace(t.Assignee); a != "" {
		name := "assignee-" + a
		if team := strings.TrimSpace(t.Team); team != "" {
			name += "-" + team
		}
		return name
	}
	return ""
}
