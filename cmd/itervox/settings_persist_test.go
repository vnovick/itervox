package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// persistFixture is a WORKFLOW.md with hand-written comments around the
// layered keys a dashboard save moves out.
const persistFixture = `---
# Team workflow — comments and key order must survive a dashboard save.
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done", "Cancelled"]
agent:
  command: claude
  # Concurrency: how many agents run at once.
  max_concurrent_agents: 3 # bumped for the launch
  max_turns: 12
server:
  allow_unauthenticated_lan: false
workspace:
  root: ./ws
---

Prompt with agent:
  max_concurrent_agents: 1
`

// persistSettingsFixture is a project settings file a teammate wrote by hand.
const persistSettingsFixture = `// Project settings — this header comment must survive.
{
  // Agent defaults shared by the team.
  "agent": {
    "max_turns": 20, // trailing comment
  },
}
`

func newPersistTestAdapter(t *testing.T, workflowContent, settingsContent string) (*orchestratorAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(workflowContent), 0o644))
	if settingsContent != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
		require.NoError(t, os.WriteFile(config.ProjectSettingsPath(path), []byte(settingsContent), 0o644))
	}
	cfg, err := config.Load(path)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	return &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: path, notify: func() {}}, path
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func explainKey(t *testing.T, workflowPath, key string) config.Setting {
	t.Helper()
	settings, err := config.Explain(workflowPath)
	require.NoError(t, err)
	for _, s := range settings {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("config.Explain does not list %s", key)
	return config.Setting{}
}

// TestDashboardSaveWritesTheProjectSettingsFile (#129): a dashboard save of a
// layered key goes to .itervox/settings.jsonc through the real setter, keeps
// the file's comments, and is what config.Load then uses (layer "project").
// WORKFLOW.md is untouched when it did not set the key, and loses only that
// key's line when it did.
func TestDashboardSaveWritesTheProjectSettingsFile(t *testing.T) {
	t.Run("key not in WORKFLOW.md", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)

		require.NoError(t, adapter.SetMaxRetries(7))

		jsonc := readString(t, config.ProjectSettingsPath(path))
		assert.Contains(t, jsonc, `"max_retries": 7`)
		for _, comment := range []string{
			"// Project settings — this header comment must survive.",
			"// Agent defaults shared by the team.",
			"// trailing comment",
		} {
			assert.Contains(t, jsonc, comment)
		}
		assert.Regexp(t, `"max_turns":\s+20`, jsonc, "the file's other keys stay")
		assert.Equal(t, persistFixture, readString(t, path), "WORKFLOW.md did not set the key and must not change")
		assert.Equal(t, 7, adapter.orch.MaxRetriesCfg())

		loaded, err := config.Load(path)
		require.NoError(t, err)
		assert.Equal(t, 7, loaded.Agent.MaxRetries)
		s := explainKey(t, path, "agent.max_retries")
		assert.Equal(t, config.LayerProject, s.Layer)
		assert.EqualValues(t, 7, s.Value)
	})

	t.Run("key in WORKFLOW.md is moved out", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, "")

		require.NoError(t, adapter.SetWorkers(9))

		want := strings.Replace(persistFixture, "  max_concurrent_agents: 3 # bumped for the launch\n", "", 1)
		require.NotEqual(t, persistFixture, want)
		assert.Equal(t, want, readString(t, path),
			"only the saved key's line may leave WORKFLOW.md; comments, order and the body stay byte-identical")
		jsonc := readString(t, config.ProjectSettingsPath(path))
		assert.Contains(t, jsonc, `"max_concurrent_agents": 9`)
		assert.Equal(t, 9, adapter.orch.MaxWorkers())

		loaded, err := config.Load(path)
		require.NoError(t, err)
		assert.Equal(t, 9, loaded.Agent.MaxConcurrentAgents)
		s := explainKey(t, path, "agent.max_concurrent_agents")
		assert.Equal(t, config.LayerProject, s.Layer)
		assert.EqualValues(t, 9, s.Value)
	})
}

// TestDashboardSaveOfNonLayeredKeyStaysInWorkflow (#129): tracker.* and
// dependencies.* are not layered, so their saves still write WORKFLOW.md and
// neither create nor touch the project settings file.
func TestDashboardSaveOfNonLayeredKeyStaysInWorkflow(t *testing.T) {
	t.Run("no settings file", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, "")
		require.NoError(t, adapter.SetFailedState("Cancelled"))
		require.NoError(t, adapter.SetDepsAnalysisMode("auto"))

		wf := readString(t, path)
		assert.Contains(t, wf, `failed_state: "Cancelled"`)
		assert.Contains(t, wf, "analysis_mode")
		_, err := os.Stat(config.ProjectSettingsPath(path))
		assert.ErrorIs(t, err, os.ErrNotExist, "a non-layered save must not create .itervox/settings.jsonc")
	})

	t.Run("existing settings file", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)
		require.NoError(t, adapter.SetFailedState("Cancelled"))

		assert.Contains(t, readString(t, path), `failed_state: "Cancelled"`)
		assert.Equal(t, persistSettingsFixture, readString(t, config.ProjectSettingsPath(path)),
			"a non-layered save must not modify .itervox/settings.jsonc")
	})
}

// TestDashboardSaveRestoresSettingsFileWhenWorkflowWriteFails (#129): the
// save writes the JSONC first and WORKFLOW.md second. When the WORKFLOW.md
// step fails, the JSONC is put back (its old bytes, or gone when it did not
// exist), WORKFLOW.md is unchanged, memory is unchanged and the error is
// returned.
func TestDashboardSaveRestoresSettingsFileWhenWorkflowWriteFails(t *testing.T) {
	failWorkflowStep := func(t *testing.T, path string) {
		t.Helper()
		restore := setSettingsWorkflowStep(func(workflowPath string) error {
			if workflowPath == path {
				return errors.New("injected: WORKFLOW.md write failed")
			}
			return nil
		})
		t.Cleanup(restore)
	}

	t.Run("existing settings file is restored", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)
		failWorkflowStep(t, path)
		before := adapter.orch.MaxWorkers()

		err := adapter.SetWorkers(9)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "injected")
		assert.Equal(t, persistSettingsFixture, readString(t, config.ProjectSettingsPath(path)))
		assert.Equal(t, persistFixture, readString(t, path))
		assert.Equal(t, before, adapter.orch.MaxWorkers(), "memory must not change when the save failed")
	})

	t.Run("new settings file is removed", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, "")
		failWorkflowStep(t, path)
		before := adapter.orch.MaxRetriesCfg()

		require.Error(t, adapter.SetMaxRetries(before+3))
		_, err := os.Stat(config.ProjectSettingsPath(path))
		assert.ErrorIs(t, err, os.ErrNotExist, "a settings file the failed save created must be removed")
		_, err = os.Stat(filepath.Dir(config.ProjectSettingsPath(path)))
		assert.ErrorIs(t, err, os.ErrNotExist, "the .itervox directory the failed save created must be removed")
		assert.Equal(t, persistFixture, readString(t, path))
		assert.Equal(t, before, adapter.orch.MaxRetriesCfg())
	})
}

// TestDashboardSaveMovesTheAliasSpellingOutOfWorkflow (#129): a save of
// server.allow_unauthenticated also removes the deprecated
// server.allow_unauthenticated_lan from WORKFLOW.md. Left there, WORKFLOW.md
// (the higher layer) would override the saved value under the old spelling.
func TestDashboardSaveMovesTheAliasSpellingOutOfWorkflow(t *testing.T) {
	_, path := newPersistTestAdapter(t, persistFixture, "")

	require.NoError(t, persistSettings(path, []setting{{Key: "server.allow_unauthenticated", Value: true}}))

	want := strings.Replace(persistFixture, "  allow_unauthenticated_lan: false\n", "", 1)
	require.NotEqual(t, persistFixture, want)
	assert.Equal(t, want, readString(t, path))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, loaded.Server.AllowUnauthenticatedLAN, "the saved value must win at the next load")
}

// TestDeleteReviewerProfileAfterDashboardReviewerSave (#129): the reviewer
// the dashboard saved lives in .itervox/settings.jsonc, so deleting that
// profile must clear it there too. Clearing it only in WORKFLOW.md would let
// the settings file's reviewer show through at the next load and fail
// validation (the profile is gone).
func TestDeleteReviewerProfileAfterDashboardReviewerSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    reviewer:
      command: claude
` + testProfileFileFields(t, dir, "reviewer") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: path, notify: func() {}}

	require.NoError(t, adapter.SetReviewerConfig("reviewer", true))
	require.NoError(t, adapter.DeleteProfile("reviewer"))

	reloaded, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "", reloaded.Agent.ReviewerProfile)
	assert.False(t, reloaded.Agent.AutoReview)
	require.NoError(t, config.ValidateDispatch(reloaded), "the deleted reviewer must not come back from the settings file")
	assert.NotContains(t, readString(t, path), "reviewer:", "the profile itself is still written to WORKFLOW.md")
}
