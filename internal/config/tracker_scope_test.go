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

// TestTrackerAssigneeRoundTrip (#182): tracker.assignee and tracker.team are
// parsed (trimmed) from WORKFLOW.md, and default to empty when absent.
func TestTrackerAssigneeRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, linearScopeWorkflow("  assignee: \" ada@example.com \"\n  team: ENG\n")))
	require.NoError(t, err)
	assert.Equal(t, "ada@example.com", cfg.Tracker.Assignee)
	assert.Equal(t, "ENG", cfg.Tracker.Team)
	assert.Empty(t, cfg.Tracker.ProjectSlug)
	assert.Equal(t, "assignee-ada@example.com-ENG", cfg.Tracker.ScopeName())
	assert.Empty(t, cfg.Tracker.LinearProjectSlug(), "an assignee scope filters by no project")

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  project_slug: proj\n")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Tracker.Assignee, "absent assignee defaults to empty")
	assert.Empty(t, cfg.Tracker.Team, "absent team defaults to empty")
	assert.Equal(t, "proj", cfg.Tracker.LinearProjectSlug())
	assert.Equal(t, "proj", cfg.Tracker.ScopeName())

	cfg, err = config.Load(workflowWithContent(t, linearScopeWorkflow("  project_slug: \"*\"\n")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Tracker.LinearProjectSlug(), `"*" is every project`)
	assert.Empty(t, cfg.Tracker.ScopeName())
}

// TestValidateDispatchLinearScopeExactlyOne (#182): a Linear tracker names
// exactly one of project_slug and assignee; both or neither is an error with
// a message that says how to fix it. assignee/team are Linear-only.
func TestValidateDispatchLinearScopeExactlyOne(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"project", linearScopeWorkflow("  project_slug: proj\n"), ""},
		{"every project", linearScopeWorkflow("  project_slug: \"*\"\n"), ""},
		{"assignee me", linearScopeWorkflow("  assignee: me\n"), ""},
		{"assignee email with team", linearScopeWorkflow("  assignee: ada@example.com\n  team: ENG\n"), ""},
		{"assignee id", linearScopeWorkflow("  assignee: 2f1c3a9e-1111-4222-8333-944455556666\n"), ""},
		{"both", linearScopeWorkflow("  project_slug: proj\n  assignee: me\n"), "both set"},
		{"neither", linearScopeWorkflow(""), `project_slug: "*"`},
		{"assignee with spaces", linearScopeWorkflow("  assignee: Ada Lovelace\n"), "is not a user"},
		{"assignee on github", "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: k\n  project_slug: o/r\n  assignee: me\n---\n\nPrompt.\n", "only supported with tracker.kind: linear"},
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

// TestTrackerSyncRefusesAnAssigneeScope (#182): the local sync creates issues
// in its project, so it keeps a project scope.
func TestTrackerSyncRefusesAnAssigneeScope(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: local\n  sync:\n    kind: linear\n    api_key: k\n    project_slug: proj\n    assignee: me\n---\n\nPrompt.\n"
	_, err := config.Load(workflowWithContent(t, content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.sync.assignee")
}
