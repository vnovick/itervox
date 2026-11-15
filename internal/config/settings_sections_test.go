package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/issuefilter"
)

// TestTrackerHooksDepsAutomationsLoadFromSettingsLayers (#239): tracker,
// hooks, dependencies and automations are layered sections. Each loads from
// the user, project and local settings files with the same merge rules as
// the other sections (later layers win, maps merge key by key, the
// automations list is replaced whole), a $VAR secret resolves from the
// environment, and Explain names the layer each value came from.
func TestTrackerHooksDepsAutomationsLoadFromSettingsLayers(t *testing.T) {
	t.Setenv("ITERVOX_TEST_TRACKER_KEY", "fixture-not-a-real-key")
	user := `// my defaults for every project
{
  "tracker": {"kind": "linear", "api_key": "$ITERVOX_TEST_TRACKER_KEY", "push_interval_ms": 0},
  "hooks": {"timeout_ms": 1234, "before_run": "echo user"},
  "dependencies": {"ordering": "simple", "staleness_hours": 24},
}`
	project := `{
  "tracker": {
    "project_slug": "core",
    "active_states": ["Todo", "Doing"],
    "filter": [{"labels_any": ["agent"]}],
    "store": {"enabled": false},
  },
  "hooks": {"after_create": "echo created"},
  "dependencies": {"analysis_mode": "manual"},
  "automations": [
    {"id": "nightly", "enabled": true, "profile": "helper", "trigger": {"type": "cron", "cron": "0 3 * * *"}},
    {"id": "weekly", "enabled": false, "profile": "helper", "trigger": {"type": "cron", "cron": "0 4 * * 1"}},
  ],
}`
	wf := layeredProject(t, "", user, project)
	writeSettings(t, config.LocalSettingsPath(wf), `{
  "tracker": {"completion_state": "Review"},
  "hooks": {"before_run": "echo local"},
  "automations": [{"id": "mine", "enabled": true, "profile": "helper", "trigger": {"type": "cron", "cron": "0 5 * * *"}}],
}`)
	// The workflow sets none of these sections: everything comes from files.
	require.NoError(t, os.WriteFile(wf, []byte("---\nitervox_schema_version: 2\n---\nDo {{ issue.title }}\n"), 0o644))

	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, "linear", cfg.Tracker.Kind, "user layer")
	assert.Equal(t, "fixture-not-a-real-key", cfg.Tracker.APIKey, "a $VAR reference resolves from the environment")
	assert.Equal(t, "https://api.linear.app/graphql", cfg.Tracker.Endpoint, "the kind's default still applies")
	assert.Equal(t, 0, cfg.Tracker.PushIntervalMs, "an explicit 0 survives")
	assert.Equal(t, "core", cfg.Tracker.ProjectSlug, "project layer")
	assert.Equal(t, []string{"Todo", "Doing"}, cfg.Tracker.ActiveStates)
	assert.Equal(t, issuefilter.Filter{{LabelsAny: []string{"agent"}}}, cfg.Tracker.Filter)
	assert.False(t, cfg.Tracker.Store.Enabled)
	assert.Equal(t, config.DefaultTrackerStoreSyncIntervalMs, cfg.Tracker.Store.SyncIntervalMs, "maps merge key by key")
	assert.Equal(t, "Review", cfg.Tracker.CompletionState, "local layer")

	assert.Equal(t, 1234, cfg.Hooks.TimeoutMs)
	assert.Equal(t, "echo created", cfg.Hooks.AfterCreate)
	assert.Equal(t, "echo local", cfg.Hooks.BeforeRun, "local wins over user")

	assert.Equal(t, "simple", cfg.Dependencies.Ordering)
	assert.Equal(t, 24, cfg.Dependencies.StalenessHours)
	assert.Equal(t, "manual", cfg.Dependencies.AnalysisMode)

	require.Len(t, cfg.Automations, 1, "the local list replaces the project's whole")
	assert.Equal(t, "mine", cfg.Automations[0].ID)
	assert.Empty(t, cfg.FrontMatter.Deprecated)
	assert.Empty(t, cfg.FrontMatter.Conflicts)

	settings, err := config.Explain(wf)
	require.NoError(t, err)
	layers := map[string]config.Setting{}
	for _, s := range settings {
		layers[s.Key] = s
	}
	assert.Equal(t, config.LayerUser, layers["tracker.kind"].Layer)
	assert.Equal(t, "(set, hidden)", layers["tracker.api_key"].Value, "the resolved key is never shown")
	assert.Equal(t, config.LayerProject, layers["tracker.active_states"].Layer)
	assert.Equal(t, config.LayerLocal, layers["tracker.completion_state"].Layer)
	assert.Equal(t, config.LayerLocal, layers["hooks.before_run"].Layer)
	assert.Equal(t, config.LayerProject, layers["dependencies.analysis_mode"].Layer)
	assert.Equal(t, config.LayerLocal, layers["automations"].Layer)

	// A list section must be a list.
	writeSettings(t, config.ProjectSettingsPath(wf), `{"automations": {"id": "x"}}`)
	_, err = config.Load(wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"automations" must be a list`)
	assert.Contains(t, err.Error(), filepath.Join(".itervox", "settings.jsonc"))
}

// TestFrontMatterKeysAreReportedOncePerKey (#239): Load names each layered
// key the front matter still sets as deprecated (the keys WORKFLOW.md keeps
// for its editors are not), and each one a settings file also sets as one
// conflict naming the highest layer that sets it.
func TestFrontMatterKeysAreReportedOncePerKey(t *testing.T) {
	wf := layeredProject(t, "agent:\n  max_turns: 2\n  available_models: {claude: []}\nhooks:\n  timeout_ms: 5\n",
		`{"agent": {"max_turns": 3}}`, `{"agent": {"max_turns": 4}}`)
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, []string{"agent.max_turns", "hooks.timeout_ms", "tracker.api_key", "tracker.kind", "tracker.project_slug"}, cfg.FrontMatter.Deprecated, "agent.available_models stays in WORKFLOW.md")
	assert.Equal(t, []config.SettingsConflict{{Key: "agent.max_turns", Layer: config.LayerProject, Path: config.ProjectSettingsPath(wf)}},
		cfg.FrontMatter.Conflicts, "one conflict per key, naming the layer in effect")
	assert.Equal(t, 4, cfg.Agent.MaxTurns)
	assert.Contains(t, config.DeprecationMessage("hooks.timeout_ms"), "moves to "+config.FrontMatterMovesTo)
}
