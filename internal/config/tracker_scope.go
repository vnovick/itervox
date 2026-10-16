package config

import (
	"errors"
	"fmt"
	"strings"
)

// LinearAllProjects is the tracker.project_slug value that scopes a Linear
// tracker to every project the API key can read (#182). Before #182 an
// absent project_slug meant the same; a Linear scope must now be named, so
// "every project" is spelled out. Quote it in YAML: project_slug: "*".
const LinearAllProjects = "*"

// AssigneeMe is the tracker.assignee value for the API key's own user (#182).
const AssigneeMe = "me"

// errLinearScope is the message for a Linear tracker whose scope is not
// exactly one of project_slug and assignee.
const linearScopeHelp = `set exactly one of tracker.project_slug (a project slug, or "*" for every project the key can read) and tracker.assignee (me, a user id or an email)`

// validateTrackerScope checks the tracker's read scope (#182): a Linear
// tracker names exactly one of project_slug and assignee; assignee and team
// are Linear-only.
func validateTrackerScope(t TrackerConfig) error {
	assignee := strings.TrimSpace(t.Assignee)
	slug := strings.TrimSpace(t.ProjectSlug)
	if t.Kind != "linear" {
		if assignee != "" || strings.TrimSpace(t.Team) != "" {
			return fmt.Errorf("tracker.assignee and tracker.team are only supported with tracker.kind: linear (got %q)", t.Kind)
		}
		return nil
	}
	switch {
	case assignee != "" && slug != "":
		return fmt.Errorf("tracker.project_slug and tracker.assignee are both set: %s", linearScopeHelp)
	case assignee == "" && slug == "":
		return errors.New("missing tracker scope for Linear: " + linearScopeHelp +
			`. Before #182 an absent project_slug meant every project; write project_slug: "*" to keep that`)
	case assignee != "" && strings.ContainsAny(assignee, " \t,"):
		return fmt.Errorf("tracker.assignee %q is not a user: use me, a user id or an email", assignee)
	}
	return nil
}

// LinearProjectSlug is the project slug the Linear client filters by: ""
// for every project (LinearAllProjects) or an assignee scope.
func (t TrackerConfig) LinearProjectSlug() string {
	slug := strings.TrimSpace(t.ProjectSlug)
	if slug == LinearAllProjects || strings.TrimSpace(t.Assignee) != "" {
		return ""
	}
	return slug
}

// ScopeName is a short, stable name for the tracker scope, for keying
// per-project state (the logs directory, the run-history key) and labelling
// the header: the project slug, "assignee-<spec>" for an assignee scope
// (with "-<team>" when narrowed), or "" when no single project or user is
// named (every project, or none configured).
func (t TrackerConfig) ScopeName() string {
	if a := strings.TrimSpace(t.Assignee); a != "" {
		name := "assignee-" + a
		if team := strings.TrimSpace(t.Team); team != "" {
			name += "-" + team
		}
		return name
	}
	slug := strings.TrimSpace(t.ProjectSlug)
	if slug == LinearAllProjects {
		return ""
	}
	return slug
}
