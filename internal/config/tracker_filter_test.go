package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/issuefilter"
)

func githubFilterWorkflow(keys string) string {
	return "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: k\n  project_slug: o/r\n" + keys + "---\n\nPrompt.\n"
}

// TestFilterConfigRoundTrip (#198): tracker.filter parses every condition
// from WORKFLOW.md (lists or single values, trimmed, de-duplicated) and is
// absent by default.
func TestFilterConfigRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, linearScopeWorkflow(`  filter:
    - projects: [ENG]
      labels_any: [agent, ai, Agent]
      labels_none: [blocked, needs-human]
    - assignee: me
      labels_all: urgent
      priority: [1, 2]
      teams: [OPS]
      states: [Todo]
      cycle: Current
      milestone: " Beta "
`)))
	require.NoError(t, err)
	assert.Equal(t, issuefilter.Filter{
		{Projects: []string{"ENG"}, LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked", "needs-human"}},
		{Assignee: "me", LabelsAll: []string{"urgent"}, Priority: []int{1, 2}, Teams: []string{"OPS"}, States: []string{"Todo"}, Cycle: "current", Milestone: "Beta"},
	}, cfg.Tracker.Filter)
	require.NoError(t, config.ValidateDispatch(cfg))

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("")))
	require.NoError(t, err)
	assert.Nil(t, cfg.Tracker.Filter, "absent filter: none")
	assert.Nil(t, cfg.Tracker.EffectiveFilter(), "no filter and no legacy keys: every issue")

	_, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  filter:\n    - labels: [agent]\n")))
	require.ErrorContains(t, err, `tracker.filter[0]: unknown condition "labels"`, "a typo fails the load instead of widening the filter")
	_, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  filter:\n    - priority: [high]\n")))
	require.ErrorContains(t, err, "tracker.filter[0].priority")
}

// TestFilterConfigLegacyKeysAreOneGroup (#198): project_slug, assignee,
// team and labels keep working as one implicit filter group (team narrowing
// the assignee, as in #182), and the same group written as tracker.filter
// has the same key.
func TestFilterConfigLegacyKeysAreOneGroup(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, linearScopeWorkflow("  project_slug: \"alpha, beta\"\n  assignee: me\n  team: ENG\n  labels: [agent, bug]\n")))
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	legacy := cfg.Tracker.EffectiveFilter()
	assert.Equal(t, issuefilter.Filter{{Projects: []string{"alpha", "beta"}, Teams: []string{"ENG"}, Assignee: "me", LabelsAny: []string{"agent", "bug"}}}, legacy)

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  filter:\n    - {projects: [beta, alpha], teams: [ENG], assignee: me, labels_any: [bug, agent]}\n")))
	require.NoError(t, err)
	assert.Equal(t, legacy.Key(), cfg.Tracker.EffectiveFilter().Key(), "the same group, the same scope")

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  team: ENG\n  labels: [agent]\n")))
	require.NoError(t, err)
	assert.Equal(t, issuefilter.Filter{{LabelsAny: []string{"agent"}}}, cfg.Tracker.EffectiveFilter(), "team narrows only an assignee")
}

// TestFilterConfigConflictIsError (#198): tracker.filter with any legacy
// scope key is a validation error naming the keys; a malformed group is
// one too.
func TestFilterConfigConflictIsError(t *testing.T) {
	const filter = "  filter:\n    - labels_any: [agent]\n"
	cases := []struct{ name, content, want string }{
		{"project_slug", linearScopeWorkflow("  project_slug: proj\n" + filter), "tracker.filter cannot be combined with tracker.project_slug"},
		{"assignee and labels", linearScopeWorkflow("  assignee: me\n  labels: [bug]\n" + filter), "tracker.assignee, tracker.labels"},
		{"team", linearScopeWorkflow("  team: ENG\n" + filter), "tracker.team"},
		{"github assignee", githubFilterWorkflow("  assignee: me\n" + filter), "tracker.filter cannot be combined with tracker.assignee"},
		{"empty group", linearScopeWorkflow("  filter:\n    - {}\n"), "tracker.filter[0]: a group needs at least one condition"},
		{"bad priority", linearScopeWorkflow("  filter:\n    - priority: [7]\n"), "7 is not a priority"},
		{"all and none", linearScopeWorkflow("  filter:\n    - {labels_all: [a], labels_none: [A]}\n"), "in both labels_all and labels_none"},
		{"local tracker", "---\nitervox_schema_version: 2\ntracker:\n  kind: local\n" + filter + "---\n\nPrompt.\n", "only supported with tracker.kind: linear or github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, tc.content))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	// project_slug is GitHub's repository, not a scope key: allowed.
	cfg, err := config.Load(workflowWithContent(t, githubFilterWorkflow(filter)))
	require.NoError(t, err)
	assert.NoError(t, config.ValidateDispatch(cfg))
}

// TestGitHubFilterRejectsUnsupportedConditions (#198): a condition GitHub
// search cannot express is a config error naming it, never ignored.
func TestGitHubFilterRejectsUnsupportedConditions(t *testing.T) {
	for cond, yaml := range map[string]string{
		"projects": "projects: [ENG]", "teams": "teams: [ENG]", "priority": "priority: [1]", "cycle": "cycle: current",
	} {
		t.Run(cond, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, githubFilterWorkflow("  filter:\n    - labels_any: [agent]\n    - {"+yaml+"}\n")))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `the "`+cond+`" condition is not supported by tracker.kind: github`)
		})
	}
	for _, ok := range []string{"assignee: me", "labels_all: [a, b]", "labels_none: [x]", "states: [todo]", "milestone: v1", "labels_any: [a, b]"} {
		cfg, err := config.Load(workflowWithContent(t, githubFilterWorkflow("  filter:\n    - {"+ok+"}\n")))
		require.NoError(t, err)
		assert.NoError(t, config.ValidateDispatch(cfg), ok)
	}
	cfg, err := config.Load(workflowWithContent(t, githubFilterWorkflow("  filter:\n    - assignee: ada@example.com\n")))
	require.NoError(t, err)
	assert.ErrorContains(t, config.ValidateDispatch(cfg), "GitHub assignees are logins")
}
