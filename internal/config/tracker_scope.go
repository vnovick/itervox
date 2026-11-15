package config

import (
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/issuefilter"
)

// The tracker's read scope (#182) is the AND of three filters, each optional:
// the project (project_slug), the assignee (assignee, narrowed by team) and
// the labels (labels, any-of). None set reads every issue, as before.

// validateTrackerScope checks the scope keys: assignee, team and labels are
// Linear-only, and an assignee is one user (me, an id or an email).
func validateTrackerScope(t TrackerConfig) error {
	if err := validateTrackerFilter(t); err != nil {
		return err
	}
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

// validateTrackerFilter checks tracker.filter (#198): its shape, that the
// legacy scope keys are not also set (they are one implicit group; both
// would be two scopes), and that the tracker can express every condition —
// GitHub search has no projects, teams, priority or cycle, and a condition
// silently ignored would read issues the filter excludes.
func validateTrackerFilter(t TrackerConfig) error {
	if len(t.Filter) == 0 {
		return nil
	}
	if err := t.Filter.Validate("tracker.filter"); err != nil {
		return err
	}
	var legacy []string
	if t.Kind == "linear" && strings.TrimSpace(t.ProjectSlug) != "" {
		legacy = append(legacy, "project_slug")
	}
	if strings.TrimSpace(t.Assignee) != "" {
		legacy = append(legacy, "assignee")
	}
	if strings.TrimSpace(t.Team) != "" {
		legacy = append(legacy, "team")
	}
	if len(t.Labels) > 0 {
		legacy = append(legacy, "labels")
	}
	if len(legacy) > 0 {
		return fmt.Errorf("tracker.filter cannot be combined with tracker.%s: the legacy keys are one implicit filter group — move them into a filter group (e.g. filter: [{projects: [...], assignee: me, labels_any: [...]}]) or remove tracker.filter",
			strings.Join(legacy, ", tracker."))
	}
	supported := issuefilter.SupportedBy(t.Kind)
	if supported == nil {
		return fmt.Errorf("tracker.filter is only supported with tracker.kind: linear or github (got %q)", t.Kind)
	}
	if c := t.Filter.Unsupported(supported); c != "" {
		return fmt.Errorf("tracker.filter: the %q condition is not supported by tracker.kind: %s (supported: %s)", c, t.Kind, strings.Join(supported, ", "))
	}
	if t.Kind == "github" {
		for i, g := range t.Filter {
			if strings.Contains(g.Assignee, "@") {
				return fmt.Errorf("tracker.filter[%d].assignee %q: GitHub assignees are logins (or me), not emails", i, g.Assignee)
			}
		}
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

// EffectiveFilter is the tracker's read filter (#198): tracker.filter when
// set, else the legacy project_slug (Linear) / assignee / team / labels as
// one implicit group, else nil (every issue).
func (t TrackerConfig) EffectiveFilter() issuefilter.Filter {
	if len(t.Filter) > 0 {
		return t.Filter
	}
	var projects []string
	if t.Kind == "linear" {
		for _, slug := range strings.Split(t.ProjectSlug, ",") {
			if slug = strings.TrimSpace(slug); slug != "" {
				projects = append(projects, slug)
			}
		}
	}
	return issuefilter.FromLegacy(projects, t.Assignee, t.Team, t.Labels)
}
