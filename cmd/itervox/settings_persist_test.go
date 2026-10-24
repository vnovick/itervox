package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
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

// TestDashboardSaveOfTrackerAndDependenciesKeysMovesThemToSettings (#239,
// replaces #129's TestDashboardSaveOfNonLayeredKeyStaysInWorkflow): tracker.*
// and dependencies.* are layered now, so the dedicated saves write the
// project settings file — keeping its comments — and move the keys out of
// WORKFLOW.md, as every other layered save does.
func TestDashboardSaveOfTrackerAndDependenciesKeysMovesThemToSettings(t *testing.T) {
	t.Run("no settings file", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, "")
		require.NoError(t, adapter.SetFailedState("Cancelled"))
		require.NoError(t, adapter.SetDepsAnalysisMode("manual"))
		require.NoError(t, adapter.UpdateTrackerStates([]string{"Todo"}, []string{"Done"}, "Done"))

		wf := readString(t, path)
		assert.NotContains(t, wf, "active_states:", "the saved keys leave WORKFLOW.md")
		assert.Contains(t, wf, "  kind: linear", "the keys the save did not touch stay")
		assert.Contains(t, wf, "# Team workflow — comments and key order must survive a dashboard save.")
		data, err := config.ReadSettingsFile(config.ProjectSettingsPath(path))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"failed_state": "Cancelled", "active_states": []any{"Todo"}, "terminal_states": []any{"Done"}, "completion_state": "Done",
		}, data["tracker"])
		assert.Equal(t, map[string]any{"analysis_mode": "manual"}, data["dependencies"])
		cfg, err := config.Load(path)
		require.NoError(t, err)
		assert.Equal(t, "Cancelled", cfg.Tracker.FailedState)
		assert.Equal(t, "manual", cfg.Dependencies.AnalysisMode)
		assert.Equal(t, []string{"Todo"}, cfg.Tracker.ActiveStates)
		assert.Equal(t, "Cancelled", adapter.orch.FailedStateCfg())
	})

	t.Run("existing settings file keeps its comments", func(t *testing.T) {
		adapter, path := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)
		require.NoError(t, adapter.SetFailedState("Cancelled"))

		jsonc := readString(t, config.ProjectSettingsPath(path))
		assert.Contains(t, jsonc, "// Project settings — this header comment must survive.")
		assert.Contains(t, jsonc, "// trailing comment")
		assert.Regexp(t, `"failed_state":\s+"Cancelled"`, jsonc)
	})
}

// TestSettingsSaveForTrackerWritesChosenLayer (#239): a save of tracker
// settings writes the settings layer the operator picks — on the Settings
// page by its layer tab (user, project or local), and through the dedicated
// tracker endpoints the layer that already holds the keys (local when it
// sets them, else project) — through the one comment-preserving writer. The
// keys leave WORKFLOW.md (never for a user-layer save), the chosen project or
// local file's value is the one in effect (a user-file value stays under the
// project's front matter), and the Live state lists reach the running daemon
// through their cfgMu setters with no reload.
func TestSettingsSaveForTrackerWritesChosenLayer(t *testing.T) {
	for _, layer := range config.SettingsLayerNames {
		t.Run("settings page "+layer, func(t *testing.T) {
			t.Setenv(config.UserSettingsEnv, filepath.Join(t.TempDir(), "home", "settings.jsonc"))
			a, wf := newPersistTestAdapter(t, persistFixture, "")
			mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "1", Identifier: "ENG-1", State: "Doing"}}, []string{"Todo"}, []string{"Done"})
			a.tr = mt
			chosen := config.SettingsLayerPath(wf, layer)
			require.NoError(t, os.MkdirAll(filepath.Dir(chosen), 0o755))
			require.NoError(t, os.WriteFile(chosen, []byte("// my "+layer+" notes\n{}\n"), 0o644))
			srv := settingsLayersServer(t, a)
			view := getSettingsLayers(t, srv)

			rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/"+layer, map[string]any{
				"baseVersion": layerVersion(view, layer),
				"values": map[string]any{
					"tracker.active_states": []string{"Todo", "Doing"}, "tracker.completion_state": "Review",
					"tracker.push_interval_ms": 0,
				},
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var res server.SettingsLayerSaveResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
			assert.Equal(t, layer, res.Layer)
			if layer == config.LayerUser {
				// The user file holds personal defaults: the project's own
				// front matter (active_states) still wins over it.
				assert.Equal(t, map[string]string{"tracker.active_states": config.LayerWorkflow}, res.Shadowed)
				assert.Equal(t, []string{"tracker.completion_state"}, res.Applied, "only the key no higher layer sets changes")
			} else {
				assert.ElementsMatch(t, []string{"tracker.active_states", "tracker.completion_state"}, res.Applied, "Live keys apply at once")
				assert.Empty(t, res.Shadowed, "no layer wins over the chosen one")
			}
			assert.Equal(t, []string{"tracker.push_interval_ms"}, res.RestartRequired)

			text := readString(t, chosen)
			assert.Contains(t, text, "// my "+layer+" notes", "the file keeps its comments")
			data, err := config.ReadSettingsFile(chosen)
			require.NoError(t, err)
			assert.Equal(t, []any{"Todo", "Doing"}, data["tracker"].(map[string]any)["active_states"])
			for _, other := range config.SettingsLayerNames {
				if other != layer {
					_, err := os.Stat(config.SettingsLayerPath(wf, other))
					assert.ErrorIs(t, err, os.ErrNotExist, "only the chosen layer is written (%s)", other)
				}
			}
			if layer == config.LayerUser {
				assert.Equal(t, persistFixture, readString(t, wf), "a user save never edits the project's WORKFLOW.md")
			} else {
				assert.NotContains(t, readString(t, wf), "active_states:", "a project or local save moves the key out of WORKFLOW.md")
			}

			active, _, completion := a.orch.TrackerStatesCfg()
			assert.Equal(t, "Review", completion, "applied through the cfgMu setter")
			assert.Equal(t, layer, explainKey(t, wf, "tracker.completion_state").Layer, "the chosen layer is in effect")
			if layer == config.LayerUser {
				assert.Equal(t, config.LayerWorkflow, explainKey(t, wf, "tracker.active_states").Layer)
				assert.Equal(t, []string{"Todo", "In Progress"}, active, "the front matter's list stays in effect")
				return
			}
			assert.Equal(t, layer, explainKey(t, wf, "tracker.active_states").Layer)
			assert.Equal(t, []string{"Todo", "Doing"}, active)
			got, err := mt.FetchCandidateIssues(t.Context())
			require.NoError(t, err)
			assert.Len(t, got, 1, "the tracker client sees the new active_states without a reload")
		})
	}

	t.Run("dedicated endpoint writes the layer that holds the keys", func(t *testing.T) {
		a, wf := newPersistTestAdapter(t, persistFixture, "")
		local := config.LocalSettingsPath(wf)
		require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
		require.NoError(t, os.WriteFile(local, []byte("// mine\n{\"tracker\": {\"active_states\": [\"Todo\"]}}\n"), 0o644))

		require.NoError(t, a.UpdateTrackerStates([]string{"Todo", "Doing"}, []string{"Done"}, ""))
		data, err := config.ReadSettingsFile(local)
		require.NoError(t, err)
		assert.Equal(t, []any{"Todo", "Doing"}, data["tracker"].(map[string]any)["active_states"], "the local file the operator chose")
		assert.Contains(t, readString(t, local), "// mine")
		_, err = os.Stat(config.ProjectSettingsPath(wf))
		assert.ErrorIs(t, err, os.ErrNotExist, "a project-file write would be shadowed by the local one")
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.Equal(t, []string{"Todo", "Doing"}, cfg.Tracker.ActiveStates)
	})

	t.Run("an inline secret is refused", func(t *testing.T) {
		a, wf := newPersistTestAdapter(t, persistFixture, "")
		srv := settingsLayersServer(t, a)
		view := getSettingsLayers(t, srv)
		rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/project", map[string]any{
			"baseVersion": layerVersion(view, config.LayerProject),
			"values":      map[string]any{"tracker.api_key": "inline-fixture-value"},
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotContains(t, rec.Body.String(), "inline-fixture-value")
		_, err := os.Stat(config.ProjectSettingsPath(wf))
		assert.ErrorIs(t, err, os.ErrNotExist)
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
