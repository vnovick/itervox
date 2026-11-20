package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

func linearScopeWorkflow(scope string) string {
	return "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: k\n" + scope + "---\n\nPrompt.\n"
}

// TestTrackerAssigneeRoundTrip (#182): tracker.assignee, tracker.team and
// tracker.labels are parsed (trimmed) from WORKFLOW.md and default to empty.
func TestTrackerAssigneeRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, linearScopeWorkflow("  assignee: \" ada@example.com \"\n  team: ENG\n  labels: [\"agent\", \" bug \"]\n")))
	require.NoError(t, err)
	assert.Equal(t, "ada@example.com", cfg.Tracker.Assignee)
	assert.Equal(t, "ENG", cfg.Tracker.Team)
	assert.Equal(t, []string{"agent", "bug"}, cfg.Tracker.Labels)
	assert.Equal(t, "assignee-ada@example.com-ENG", cfg.Tracker.ScopeName())

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  project_slug: proj\n")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Tracker.Assignee, "absent assignee defaults to empty")
	assert.Empty(t, cfg.Tracker.Team, "absent team defaults to empty")
	assert.Empty(t, cfg.Tracker.Labels, "absent labels default to none")
	assert.Equal(t, "proj", cfg.Tracker.ScopeName())

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  project_slug: proj\n  assignee: me\n")))
	require.NoError(t, err)
	assert.Equal(t, "proj", cfg.Tracker.ScopeName(), "a project keeps its name when an assignee narrows it")
}

// TestLinearScopeCombinations (#182): the scope keys AND, so every
// combination of project, assignee and labels — none included — is valid;
// only a malformed assignee or a scope key on another tracker kind is not.
func TestLinearScopeCombinations(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"neither (every project)", linearScopeWorkflow(""), ""},
		{"project", linearScopeWorkflow("  project_slug: proj\n"), ""},
		{"assignee", linearScopeWorkflow("  assignee: me\n"), ""},
		{"assignee email with team", linearScopeWorkflow("  assignee: ada@example.com\n  team: ENG\n"), ""},
		{"project and assignee", linearScopeWorkflow("  project_slug: proj\n  assignee: me\n"), ""},
		{"labels only", linearScopeWorkflow("  labels: [agent]\n"), ""},
		{"project and labels", linearScopeWorkflow("  project_slug: proj\n  labels: [agent, bug]\n"), ""},
		{"project, assignee and labels", linearScopeWorkflow("  project_slug: proj\n  assignee: 2f1c3a9e-1111-4222-8333-944455556666\n  labels: [agent]\n"), ""},
		{"assignee with spaces", linearScopeWorkflow("  assignee: Ada Lovelace\n"), "is not a user"},
		{"assignee on github", "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: k\n  project_slug: o/r\n  assignee: me\n---\n\nPrompt.\n", "only supported with tracker.kind: linear"},
		{"labels on github", "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: k\n  project_slug: o/r\n  labels: [agent]\n---\n\nPrompt.\n", "only supported with tracker.kind: linear"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, tc.content))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestJiraScopeKeys: a Jira tracker accepts tracker.assignee (me or an
// account id) and tracker.labels, still refuses tracker.team and
// tracker.filter, and refuses an email or a name as the assignee.
func TestJiraScopeKeys(t *testing.T) {
	jira := func(scope string) string {
		return "---\nitervox_schema_version: 2\ntracker:\n  kind: jira\n  endpoint: https://your-site.atlassian.net\n" +
			"  project_slug: PROJ\n  api_key: me@example.com:token123\n" + scope + "---\n\nPrompt.\n"
	}
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"no scope", jira(""), ""},
		{"assignee me", jira("  assignee: me\n"), ""},
		{"assignee account id", jira("  assignee: \"557058:f58131cb-b67d-43c7-b30d-6b58d40bd077\"\n"), ""},
		{"labels", jira("  labels: [agent, bot]\n"), ""},
		{"assignee and labels", jira("  assignee: me\n  labels: [agent]\n"), ""},
		{"team", jira("  team: ENG\n"), "tracker.team is not supported with tracker.kind: jira"},
		{"team with assignee", jira("  assignee: me\n  team: ENG\n"), "tracker.team is not supported with tracker.kind: jira"},
		{"assignee email", jira("  assignee: ada@example.com\n"), "use me or an Atlassian account id"},
		{"assignee name", jira("  assignee: Ada Lovelace\n"), "use me or an Atlassian account id"},
		{"filter", jira("  filter: [{labels_any: [agent]}]\n"), "tracker.filter is only supported with tracker.kind: linear or github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, tc.content))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestTrackerSyncAssigneeNeedsAProject (#182): the local sync creates issues
// in its project, so an assignee narrows that project (accepted) but never
// replaces it.
func TestTrackerSyncAssigneeNeedsAProject(t *testing.T) {
	sync := func(keys string) string {
		return "---\nitervox_schema_version: 2\ntracker:\n  kind: local\n  sync:\n    kind: linear\n    api_key: k\n" + keys + "---\n\nPrompt.\n"
	}
	cfg, err := config.Load(workflowWithContent(t, sync("    project_slug: proj\n    assignee: me\n    labels: [agent]\n")))
	require.NoError(t, err, "project + assignee is the intersection")
	assert.Equal(t, "me", cfg.Tracker.Sync.Assignee)
	assert.Equal(t, []string{"agent"}, cfg.Tracker.Sync.Labels)

	_, err = config.Load(workflowWithContent(t, sync("    assignee: me\n")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.sync.assignee needs tracker.sync.project_slug")
}
