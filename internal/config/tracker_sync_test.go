package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

func localWorkflow(extra string) string {
	return "---\nitervox_schema_version: 2\ntracker:\n  kind: local\n" + extra + "---\n\nPrompt.\n"
}

// TestTrackerSyncConfig (#118): tracker.sync is off when absent; present, it
// reads the remote's connection keys with the same defaults as tracker:
// itself ($VAR api keys, the Linear endpoint, state lists), a state map,
// the pulled states and the intervals.
func TestTrackerSyncConfig(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, localWorkflow("")))
	require.NoError(t, err)
	assert.False(t, cfg.Tracker.Sync.Enabled, "off when absent")

	t.Setenv("ITERVOX_TEST_SYNC_KEY", "lin_api_test")
	cfg, err = config.Load(workflowWithContent(t, localWorkflow(
		"  sync:\n    kind: linear\n    api_key: $ITERVOX_TEST_SYNC_KEY\n    project_slug: eng-board\n"+
			"    state_map:\n      Review: In Review\n    interval_ms: 30000\n")))
	require.NoError(t, err)
	s := cfg.Tracker.Sync
	assert.True(t, s.Enabled)
	assert.Equal(t, "linear", s.Kind)
	assert.Equal(t, "lin_api_test", s.APIKey)
	assert.Equal(t, "eng-board", s.ProjectSlug)
	assert.Equal(t, "https://api.linear.app/graphql", s.Endpoint)
	assert.Equal(t, []string{"Todo", "In Progress"}, s.ActiveStates)
	assert.Equal(t, []string{"Backlog"}, s.BacklogStates)
	assert.Equal(t, map[string]string{"Review": "In Review"}, s.StateMap)
	assert.Equal(t, []string{"Todo", "In Progress", "Backlog"}, s.PullStates, "default: active and backlog states")
	assert.Equal(t, 30000, s.IntervalMs)
	assert.Equal(t, config.DefaultTrackerSyncFullIntervalMs, s.FullSyncIntervalMs)

	cfg, err = config.Load(workflowWithContent(t, localWorkflow(
		"  sync:\n    kind: github\n    api_key: ghp_x\n    project_slug: o/r\n    pull_states: [todo]\n    completion_state: in-review\n")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Tracker.Sync.Endpoint, "GitHub's client picks its own endpoint")
	assert.Equal(t, []string{"todo"}, cfg.Tracker.Sync.PullStates)
	assert.Equal(t, config.DefaultTrackerSyncIntervalMs, cfg.Tracker.Sync.IntervalMs)

	for _, tc := range []struct{ yaml, err string }{
		{"  sync:\n    api_key: k\n    project_slug: p\n", "tracker.sync.kind must be linear or github"},
		{"  sync:\n    kind: jira\n    api_key: k\n    project_slug: p\n", "tracker.sync.kind must be linear or github"},
		{"  sync:\n    kind: linear\n    project_slug: p\n", "tracker.sync.api_key is empty"},
		{"  sync:\n    kind: github\n    api_key: k\n", "tracker.sync.project_slug is required for github"},
		{"  sync:\n    kind: linear\n    api_key: k\n", "tracker.sync.project_slug is required for linear"},
	} {
		_, err := config.Load(workflowWithContent(t, localWorkflow(tc.yaml)))
		assert.ErrorContains(t, err, tc.err, tc.yaml)
	}
	_, err = config.Load(workflowWithContent(t, minimal("  sync:\n    kind: github\n    api_key: k\n    project_slug: o/r\n")))
	assert.ErrorContains(t, err, "tracker.sync is for tracker.kind: local")
}
