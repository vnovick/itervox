package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// modelServerWorkflow writes a workflow whose `local` profile has a
// model_server and its own stall timeout, and returns its path and the
// adapter over a real orchestrator.
func modelServerWorkflow(t *testing.T, modelServer string) (string, *orchestratorAdapter) {
	t.Helper()
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    local:
      command: claude
      model_server: "` + modelServer + `"
      stall_timeout_ms: 1200000
` + testProfileFileFields(t, dir, "local") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	return workflowPath, &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}
}

// TestUpsertProfileModelServer: the dashboard save path. A save without
// modelServer keeps it (and the profile's timeouts); a save with one sets
// it, re-derives the pools of the whole set and never writes the derived
// pool; "" clears it;
// a value the profile's backend cannot use is refused.
func TestUpsertProfileModelServer(t *testing.T) {
	t.Setenv("ITERVOX_TEST_LOCAL_URL", "http://127.0.0.1:8100")
	workflowPath, adapter := modelServerWorkflow(t, "$ITERVOX_TEST_LOCAL_URL")
	reload := func() config.AgentProfile {
		t.Helper()
		cfg, err := config.Load(workflowPath)
		require.NoError(t, err)
		return cfg.Agent.Profiles["local"]
	}

	require.NoError(t, adapter.UpsertProfile("local", server.ProfileDef{Command: "claude --model m", Enabled: true}, "local"))
	p := reload()
	assert.Equal(t, "$ITERVOX_TEST_LOCAL_URL", p.ModelServer, "kept as written when the save does not carry it")
	assert.Equal(t, "http://127.0.0.1:8100", p.ModelServerURL)
	assert.Equal(t, 1200000, p.StallTimeoutMs, "per-profile timeouts are carried over")
	raw, _ := os.ReadFile(workflowPath)
	assert.NotContains(t, string(raw), "pool:", "the derived pool is never written")

	require.NoError(t, adapter.UpsertProfile("local", server.ProfileDef{
		Command: "claude --model m", Enabled: true, ModelServer: "http://127.0.0.1:9000", ModelServerSet: true,
	}, "local"))
	live := adapter.orch.ProfilesCfg()["local"]
	assert.Equal(t, "http://127.0.0.1:9000", live.ModelServerURL, "the live profile is resolved too")
	assert.Equal(t, "model-local", live.Pool, "the pool is named after the profile, not the port")
	assert.True(t, live.PoolAuto)
	assert.Equal(t, "model-local", reload().Pool)

	// A second profile on the same server joins its pool, which is named
	// after the first of them by name; deleting it gives the name back.
	require.NoError(t, adapter.UpsertProfile("another", server.ProfileDef{
		Command: "claude", Enabled: true, ModelServer: "http://localhost:9000", ModelServerSet: true,
	}, ""))
	profiles := adapter.orch.ProfilesCfg()
	assert.Equal(t, "model-another", profiles["another"].Pool)
	assert.Equal(t, "model-another", profiles["local"].Pool, "one server, one pool")
	require.NoError(t, adapter.DeleteProfile("another"))
	assert.Equal(t, "model-local", adapter.orch.ProfilesCfg()["local"].Pool)
	assert.Equal(t, "model-local", reload().Pool)
	raw, _ = os.ReadFile(workflowPath)
	assert.NotContains(t, string(raw), "pool:", "the derived pool is never written")

	require.NoError(t, adapter.UpsertProfile("local", server.ProfileDef{
		Command: "claude --model m", Enabled: true, ModelServer: "", ModelServerSet: true,
	}, "local"))
	p = reload()
	assert.Empty(t, p.ModelServer)
	assert.Empty(t, p.Pool, "a derived pool goes with its server")

	err := adapter.UpsertProfile("local", server.ProfileDef{
		Command: "opencode", Enabled: true, ModelServer: "http://127.0.0.1:9000", ModelServerSet: true,
	}, "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model_server works with claude and codex profiles")
}

// TestUpsertProfileKeepsModelServerName: model_server_name is load-time
// (the dashboard does not edit it): a save carries it over, writes it back
// and keeps naming the pool.
func TestUpsertProfileKeepsModelServerName(t *testing.T) {
	workflowPath, adapter := modelServerWorkflow(t, "http://127.0.0.1:9000\"\n      model_server_name: \"workstation")
	require.Equal(t, "workstation", adapter.orch.ProfilesCfg()["local"].Pool)
	require.NoError(t, adapter.UpsertProfile("local", server.ProfileDef{Command: "claude --model m", Enabled: true}, "local"))
	raw, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "model_server_name: \"workstation\"")
	assert.NotContains(t, string(raw), "pool:")
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "workstation", cfg.Agent.Profiles["local"].Pool)
	assert.Equal(t, "workstation", adapter.orch.ProfilesCfg()["local"].Pool)
}

// TestDoctorAndModelServersListModelServerProfiles: doctor probes a
// profile's model_server (httptest, never a real server) and marks it, and
// GET /api/v1/model-servers lists it with its derived pool.
func TestDoctorAndModelServersListModelServerProfiles(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()
	_, adapter := modelServerWorkflow(t, up.URL)

	c := checkModelServers(adapter.cfg)
	require.Len(t, c.Servers, 1)
	require.NoError(t, c.Servers[0].Err)
	var b strings.Builder
	renderModelServerCheck(&b, c)
	assert.Contains(t, b.String(), "ANTHROPIC_BASE_URL="+up.URL+" (model_server), used by local: answering")

	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		Client:       adapter,
		AllowedHosts: []string{"example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/model-servers", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	pool := "model-local" // the server's pool, named after its one profile
	assert.JSONEq(t, `{"servers":[{"var":"ANTHROPIC_BASE_URL","url":"`+up.URL+`","profiles":["local"],"pools":["`+pool+`"],"remote":false,"modelServer":true}]}`,
		rec.Body.String())

	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	_, stopped := modelServerWorkflow(t, downURL)
	b.Reset()
	renderModelServerCheck(&b, checkModelServers(stopped.cfg))
	assert.Contains(t, b.String(), "(model_server), used by local: not answering")
	assert.Contains(t, b.String(), "or fix their model_server")
}
