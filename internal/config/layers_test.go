package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// #129 — settings layers: defaults < user < project < WORKFLOW.md.

func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// layeredProject is a workflow with a user settings file (named by
// ITERVOX_USER_SETTINGS) and, when project is not empty, a project file.
func layeredProject(t *testing.T, workflowExtras, user, project string) string {
	t.Helper()
	wf := workflowWithContent(t, minimalV2(workflowExtras))
	userPath := filepath.Join(t.TempDir(), "settings.jsonc")
	t.Setenv(config.UserSettingsEnv, userPath)
	if user != "" {
		writeSettings(t, userPath, user)
	}
	if project != "" {
		writeSettings(t, config.ProjectSettingsPath(wf), project)
	}
	return wf
}

func TestUserSettingAppliesToAProjectThatDoesNotSetIt(t *testing.T) {
	wf := layeredProject(t, "",
		`// shared by every project
{
  "agent": {"max_concurrent_agents": 7, "max_turns": 11,},
  "polling": {"interval_ms": 12345}, // trailing commas are fine
}`, "")
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.Agent.MaxConcurrentAgents)
	assert.Equal(t, 11, cfg.Agent.MaxTurns)
	assert.Equal(t, 12345, cfg.Polling.IntervalMs)
}

func TestProjectSettingOverridesTheUserSetting(t *testing.T) {
	wf := layeredProject(t, "",
		`{"agent": {"max_concurrent_agents": 7, "max_turns": 11}}`,
		`{"agent": {"max_turns": 4}}`)
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 4, cfg.Agent.MaxTurns, "the project file wins over the user file")
	assert.Equal(t, 7, cfg.Agent.MaxConcurrentAgents, "maps merge key by key: the user value it does not set stays")
}

func TestWorkflowOverridesTheSettingsFiles(t *testing.T) {
	wf := layeredProject(t, "agent:\n  max_turns: 2\n",
		`{"agent": {"max_turns": 11, "max_concurrent_agents": 7}}`,
		`{"agent": {"max_turns": 4}}`)
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.Agent.MaxTurns, "WORKFLOW.md wins")
	assert.Equal(t, 7, cfg.Agent.MaxConcurrentAgents)
}

func TestNoSettingsFilesLeavesTheDefaults(t *testing.T) {
	wf := layeredProject(t, "", "", "")
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 30000, cfg.Polling.IntervalMs)
}

func TestUserSettingsLayerIsOffUnderTestsUnlessNamed(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "")
	assert.Empty(t, config.UserSettingsPath(), "go test never reads the developer's own ~/.itervox/settings.jsonc")
	t.Setenv(config.UserSettingsEnv, "off")
	assert.Empty(t, config.UserSettingsPath())
	t.Setenv(config.UserSettingsEnv, "/x/settings.jsonc")
	assert.Equal(t, "/x/settings.jsonc", config.UserSettingsPath())
}

func TestSettingsFileCannotSetTheTrackerOrBreakTheLoad(t *testing.T) {
	wf := layeredProject(t, "", `{"tracker": {"kind": "github"}}`, "")
	_, err := config.Load(wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"tracker" cannot be set here`)

	wf = layeredProject(t, "", "", `{"agent": {"max_turns": }`)
	_, err = config.Load(wf)
	require.Error(t, err, "a settings file that does not parse fails the load")
	assert.Contains(t, err.Error(), "settings.jsonc")
}

func TestSetSettingKeepsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "settings.jsonc")
	writeSettings(t, path, `// team defaults
{
  // how many agents at once
  "agent": {
    "max_turns": 3, // keep me
  },
}
`)
	require.NoError(t, config.SetSetting(path, "agent.max_turns", 5))
	require.NoError(t, config.SetSetting(path, "server.metrics.enabled", true))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, c := range []string{"// team defaults", "// how many agents at once", "// keep me"} {
		assert.Contains(t, string(got), c, "a write keeps every comment")
	}
	data, err := config.ReadSettingsFile(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"agent":  map[string]any{"max_turns": 5},
		"server": map[string]any{"metrics": map[string]any{"enabled": true}},
	}, data)

	require.NoError(t, config.SetSetting(path, "agent.max_turns", nil))
	data, err = config.ReadSettingsFile(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, data["agent"], "unset removes the key")
	got, _ = os.ReadFile(path)
	assert.Contains(t, string(got), "// team defaults")

	err = config.SetSetting(path, "tracker.kind", "github")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be set in a settings file")
}

func TestSetSettingCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "settings.jsonc")
	require.NoError(t, config.SetSetting(path, "agent.max_turns", nil), "removing from a missing file is a no-op")
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, config.SetSetting(path, "agent.max_concurrent_agents", 2))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "// Itervox settings")
	data, err := config.ReadSettingsFile(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"agent": map[string]any{"max_concurrent_agents": 2}}, data)
}

func TestExplainNamesTheLayerOfEachValue(t *testing.T) {
	wf := layeredProject(t, "agent:\n  max_turns: 2\n",
		`{"agent": {"max_concurrent_agents": 7}, "polling": {"interval_ms": 1000}}`,
		`{"polling": {"interval_ms": 5000}}`)
	settings, err := config.Explain(wf)
	require.NoError(t, err)
	byKey := map[string]config.Setting{}
	for _, s := range settings {
		byKey[s.Key] = s
	}
	assert.Equal(t, config.Setting{Key: "agent.max_turns", Value: 2, Layer: config.LayerWorkflow}, byKey["agent.max_turns"])
	assert.Equal(t, config.LayerUser, byKey["agent.max_concurrent_agents"].Layer)
	assert.Equal(t, 7, byKey["agent.max_concurrent_agents"].Value)
	assert.NotEmpty(t, byKey["agent.max_concurrent_agents"].Path)
	assert.Equal(t, config.LayerProject, byKey["polling.interval_ms"].Layer)
	assert.Equal(t, 5000, byKey["polling.interval_ms"].Value)
	assert.Equal(t, config.ProjectSettingsPath(wf), byKey["polling.interval_ms"].Path)
	assert.Equal(t, config.Setting{Key: "agent.max_retries", Value: 5, Layer: config.LayerDefault}, byKey["agent.max_retries"])
	assert.Equal(t, "(set, hidden)", byKey["tracker.api_key"].Value, "secrets are never printed")
	assert.Equal(t, config.LayerWorkflow, byKey["tracker.api_key"].Layer)
	assert.Contains(t, byKey, "server.metrics.enabled", "nested sections are listed")
}

func explainByKey(t *testing.T, wf string) map[string]config.Setting {
	t.Helper()
	settings, err := config.Explain(wf)
	require.NoError(t, err)
	byKey := map[string]config.Setting{}
	for _, s := range settings {
		byKey[s.Key] = s
	}
	return byKey
}

// Keys the parser reads by hand, and values it corrects after reading, are
// listed with the value in effect.
func TestExplainListsHandReadKeysWithTheirEffectiveValue(t *testing.T) {
	wf := layeredProject(t, "", `{"server": {"port": 9123, "allow_unauthenticated": true}}`, "")
	byKey := explainByKey(t, wf)
	assert.Equal(t, config.Setting{Key: "server.port", Value: 9123, Layer: config.LayerUser, Path: os.Getenv(config.UserSettingsEnv)}, byKey["server.port"])
	assert.Equal(t, true, byKey["server.allow_unauthenticated"].Value)
	assert.Equal(t, config.LayerUser, byKey["server.allow_unauthenticated"].Layer)
	assert.Contains(t, byKey, "dependencies.analysis_mode")
	assert.Contains(t, byKey, "agent.available_models", "maps read whole are listed")

	t.Setenv(config.EnvServerPort, "7000")
	t.Setenv(config.EnvServerHost, "0.0.0.0")
	byKey = explainByKey(t, wf)
	assert.Equal(t, config.Setting{Key: "server.port", Value: 7000, Layer: config.LayerEnvironment, Path: config.EnvServerPort}, byKey["server.port"])
	assert.Equal(t, config.Setting{Key: "server.host", Value: "0.0.0.0", Layer: config.LayerEnvironment, Path: config.EnvServerHost}, byKey["server.host"])
}

func TestExplainCreditsMergedMapsAndBlamesNoLayerForAnIgnoredValue(t *testing.T) {
	wf := layeredProject(t, "",
		`{"agent": {"ssh_host_descriptions": {"a": "from user"}, "max_turns": "lots", "max_turn": 4}}`,
		`{"agent": {"ssh_host_descriptions": {"b": "from project"}}}`)
	byKey := explainByKey(t, wf)

	hosts := byKey["agent.ssh_host_descriptions"]
	assert.Equal(t, map[string]string{"a": "from user", "b": "from project"}, hosts.Value)
	assert.Equal(t, config.LayerProject, hosts.Layer, "a merged map is credited to the highest layer that set part of it")

	turns := byKey["agent.max_turns"]
	assert.Equal(t, config.LayerDefault, turns.Layer, "the user value was not used")
	assert.Equal(t, 20, turns.Value)
	assert.Contains(t, turns.Note, `user value "lots" ignored: not a number`)

	typo := byKey["agent.max_turn"]
	assert.True(t, typo.Unread, "a key nothing reads is listed and flagged")
	assert.Equal(t, 4, typo.Value)
	assert.Equal(t, config.LayerUser, typo.Layer)
}

func TestExplainHidesPasswordsInURLs(t *testing.T) {
	wf := layeredProject(t, "", "", `{"workspace": {"clone_url": "https://bot:s3cret@example.com/r.git"}}`)
	byKey := explainByKey(t, wf)
	assert.Equal(t, "https://bot:hidden@example.com/r.git", byKey["workspace.clone_url"].Value)
}

func TestSettingsSectionMustBeAnObject(t *testing.T) {
	wf := layeredProject(t, "", `{"agent": 5}`, "")
	_, err := config.Load(wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"agent" must be an object`)
}
