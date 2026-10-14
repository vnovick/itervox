package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

// layerComments is a hand-written settings layer file whose comments a
// Settings page save must keep.
const layerComments = `// %s settings — this header comment must survive.
{
  // Agent defaults.
  "agent": {
    "max_retries": 3, // trailing comment
  },
}
`

func settingsLayersServer(t *testing.T, a *orchestratorAdapter) *server.Server {
	t.Helper()
	return server.New(server.Config{
		Snapshot:       func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:    make(chan struct{}, 1),
		SettingsLayers: a,
		AllowedHosts:   []string{"example.com"},
	})
}

func getSettingsLayers(t *testing.T, srv *server.Server) server.SettingsLayersView {
	t.Helper()
	rec := rulesRequest(t, srv, http.MethodGet, "/api/v1/settings/layers", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var view server.SettingsLayersView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	return view
}

func layerVersion(view server.SettingsLayersView, name string) string {
	for _, l := range view.Layers {
		if l.Name == name {
			return l.Version
		}
	}
	return ""
}

// TestSettingsPageSaveWritesChosenLayerAndKeepsComments (#158): a Settings
// page save names its layer and writes exactly that file through the generic
// writer, keeping its comments. A project or local save also moves the key out
// of WORKFLOW.md (which would otherwise win); a user save leaves the project's
// files alone and reports the layer that still wins over it.
func TestSettingsPageSaveWritesChosenLayerAndKeepsComments(t *testing.T) {
	for _, layer := range config.SettingsLayerNames {
		t.Run(layer, func(t *testing.T) {
			t.Setenv(config.UserSettingsEnv, filepath.Join(t.TempDir(), "home", "settings.jsonc"))
			a, wf := newPersistTestAdapter(t, persistFixture, "")
			paths := map[string]string{}
			for _, name := range config.SettingsLayerNames {
				paths[name] = config.SettingsLayerPath(wf, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(paths[name]), 0o755))
				require.NoError(t, os.WriteFile(paths[name], []byte(fmtLayer(name)), 0o644))
			}
			srv := settingsLayersServer(t, a)
			view := getSettingsLayers(t, srv)
			require.Len(t, view.Layers, 3)

			rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/"+layer, map[string]any{
				"baseVersion": layerVersion(view, layer),
				"values":      map[string]any{"agent.max_turns": 7, "agent.max_retries": 4},
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var res server.SettingsLayerSaveResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
			assert.Equal(t, layer, res.Layer)

			got := readString(t, paths[layer])
			assert.Contains(t, got, "// "+layer+" settings — this header comment must survive.")
			assert.Contains(t, got, "// Agent defaults.")
			assert.Contains(t, got, "// trailing comment")
			assert.Regexp(t, `"max_turns":\s+7`, got)
			assert.Regexp(t, `"max_retries":\s+4, // trailing comment`, got)
			for _, other := range config.SettingsLayerNames {
				if other != layer {
					assert.Equal(t, fmtLayer(other), readString(t, paths[other]), "the %s layer is untouched", other)
				}
			}

			wfText := readString(t, wf)
			if layer == config.LayerUser {
				assert.Contains(t, wfText, "max_turns: 12", "a user save never edits the project's WORKFLOW.md")
				assert.Equal(t, config.LayerWorkflow, res.Shadowed["agent.max_turns"])
				assert.Equal(t, config.LayerLocal, res.Shadowed["agent.max_retries"])
			} else {
				assert.NotContains(t, wfText, "max_turns:")
				assert.Contains(t, wfText, "# Concurrency: how many agents run at once.", "WORKFLOW.md keeps its comments")
				assert.Equal(t, 7, explainKey(t, wf, "agent.max_turns").Value)
				assert.Equal(t, layer, explainKey(t, wf, "agent.max_turns").Layer)
			}
		})
	}
}

func fmtLayer(name string) string {
	return "// " + name + layerComments[len("// %s"):]
}

// TestSettingsLayerSaveAppliesLiveSettings (#158): a changed Live setting is
// applied through its cfgMu setter at once; any other changed setting is
// reported as needing a restart.
func TestSettingsLayerSaveAppliesLiveSettings(t *testing.T) {
	a, wf := newPersistTestAdapter(t, persistFixture, "")
	srv := settingsLayersServer(t, a)
	view := getSettingsLayers(t, srv)
	rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/local", map[string]any{
		"baseVersion": layerVersion(view, config.LayerLocal),
		"values": map[string]any{
			"agent.max_concurrent_agents": 4, "agent.max_turns": 30, "agent.inline_input": true,
			"agent.dispatch_strategy": "least-loaded", "agent.ssh_hosts": []string{"build-1"},
		},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res server.SettingsLayerSaveResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.ElementsMatch(t, []string{"agent.max_concurrent_agents", "agent.inline_input", "agent.dispatch_strategy", "agent.ssh_hosts"}, res.Applied)
	assert.Equal(t, []string{"agent.max_turns"}, res.RestartRequired)
	assert.Equal(t, 4, a.orch.MaxWorkers())
	assert.True(t, a.orch.InlineInputCfg())
	assert.Equal(t, "least-loaded", a.orch.DispatchStrategyCfg())
	hosts, _ := a.orch.SSHHostsCfg()
	assert.Equal(t, []string{"build-1"}, hosts)
	assert.NotContains(t, readString(t, wf), "max_concurrent_agents: 3", "the key moved out of WORKFLOW.md")
	ignore := readString(t, filepath.Join(filepath.Dir(wf), ".itervox", ".gitignore"))
	assert.Contains(t, ignore, "/settings.local.jsonc", "a local save makes sure the file is gitignored")
}

// TestSettingsLayerSaveRefusesStaleOrInvalid (#158): a stale base version, a
// value of the wrong type, a reviewer profile that does not exist, or JSON
// view text that does not parse is refused and nothing is written.
func TestSettingsLayerSaveRefusesStaleOrInvalid(t *testing.T) {
	a, wf := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)
	srv := settingsLayersServer(t, a)
	project := config.ProjectSettingsPath(wf)
	version := layerVersion(getSettingsLayers(t, srv), config.LayerProject)
	wfBefore := readString(t, wf)

	for name, tc := range map[string]struct {
		body map[string]any
		code int
	}{
		"stale":             {map[string]any{"baseVersion": "nope", "values": map[string]any{"agent.max_turns": 2}}, http.StatusConflict},
		"wrong type":        {map[string]any{"baseVersion": version, "values": map[string]any{"agent.max_turns": "lots"}}, http.StatusBadRequest},
		"unknown key":       {map[string]any{"baseVersion": version, "values": map[string]any{"agent.max_turnz": 2}}, http.StatusBadRequest},
		"enum":              {map[string]any{"baseVersion": version, "values": map[string]any{"agent.dispatch_strategy": "fastest"}}, http.StatusBadRequest},
		"missing reviewer":  {map[string]any{"baseVersion": version, "values": map[string]any{"agent.reviewer_profile": "ghost"}}, http.StatusBadRequest},
		"profiles page":     {map[string]any{"baseVersion": version, "values": map[string]any{"agent.profiles": map[string]any{}}}, http.StatusBadRequest},
		"raw not jsonc":     {map[string]any{"baseVersion": version, "raw": "{ nope"}, http.StatusBadRequest},
		"raw wrong section": {map[string]any{"baseVersion": version, "raw": `{"tracker": {"kind": "github"}}`}, http.StatusBadRequest},
		"both":              {map[string]any{"baseVersion": version, "raw": "{}", "values": map[string]any{}}, http.StatusBadRequest},
		"unknown layer":     {map[string]any{"baseVersion": "", "values": map[string]any{"agent.max_turns": 2}}, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			layer := "project"
			if name == "unknown layer" {
				layer = "org"
			}
			rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/"+layer, tc.body)
			assert.Equal(t, tc.code, rec.Code, rec.Body.String())
			assert.Equal(t, persistSettingsFixture, readString(t, project))
			assert.Equal(t, wfBefore, readString(t, wf))
		})
	}
}

// TestSettingsLayerRawSaveWritesTextAsTyped (#158): the JSON view's text is
// written as typed, comments included, and the keys it sets move out of
// WORKFLOW.md.
func TestSettingsLayerRawSaveWritesTextAsTyped(t *testing.T) {
	a, wf := newPersistTestAdapter(t, persistFixture, persistSettingsFixture)
	srv := settingsLayersServer(t, a)
	view := getSettingsLayers(t, srv)
	raw := "// typed in the JSON view\n{\"$schema\": \"" + config.SettingsSchemaURL + "\", \"agent\": {\"max_concurrent_agents\": 5 /* five */}}\n"
	rec := rulesRequest(t, srv, http.MethodPut, "/api/v1/settings/layers/project", map[string]any{
		"baseVersion": layerVersion(view, config.LayerProject), "raw": raw,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, raw, readString(t, config.ProjectSettingsPath(wf)))
	assert.Equal(t, 5, a.orch.MaxWorkers())
	assert.NotContains(t, readString(t, wf), "max_concurrent_agents: 3")
	var res server.SettingsLayerSaveResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	for _, l := range res.Layers {
		if l.Name == config.LayerProject {
			assert.Equal(t, map[string]any{"agent.max_concurrent_agents": float64(5)}, l.Values)
		}
	}
}

// TestLiveSettingsHaveAppliers (#158): every setting the registry marks Live
// has an applier that reaches its cfgMu setter, and nothing else has one.
func TestLiveSettingsHaveAppliers(t *testing.T) {
	for _, f := range config.SettingFields {
		_, has := liveSettingAppliers[f.Key]
		assert.Equal(t, f.Live, has, "%s: Live=%v but applier present=%v", f.Key, f.Live, has)
	}
	for key := range liveSettingAppliers {
		_, ok := config.SettingFieldFor(key)
		assert.True(t, ok, "applier for unregistered key %s", key)
	}
}
