package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestModelServersAdapterListsProfilesPoolsAndRemote drives
// GET /api/v1/model-servers through the REAL orchestratorAdapter and a REAL
// orchestrator: the rows are doctor's grouping with the profiles' pools,
// remote follows agent.ssh_hosts, and runtime profile and SSH-host edits
// (cfgMu setters) show on the next read.
func TestModelServersAdapterListsProfilesPoolsAndRemote(t *testing.T) {
	local := "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"
	cfg := &config.Config{Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}}}
	cfg.Agent.Command = local
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"coder-local": {Command: local + " --model glm", Pool: "local"},
		"from-var":    {Command: "OPENAI_BASE_URL=$LOCAL_URL opencode"},
		"vendor":      {Command: "claude"},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, notify: func() {}}
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		Client:       adapter,
		AllowedHosts: []string{"example.com"},
	})
	get := func() string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/model-servers", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}

	assert.JSONEq(t, `{"servers":[
		{"var":"OPENAI_BASE_URL","url":"$LOCAL_URL","profiles":["from-var"],"pools":[],"remote":false,"unchecked":true},
		{"var":"ANTHROPIC_BASE_URL","url":"http://127.0.0.1:8000","profiles":["(default)","coder-local"],"pools":["local"],"remote":false}
	]}`, get())

	// Runtime edits through the cfgMu setters are read on the next request.
	orch.SetProfilesCfg(map[string]config.AgentProfile{"box": {Command: local, Pool: "box"}})
	orch.AddSSHHostCfg("worker-1", "")
	var got struct {
		Servers []server.ModelServerRow `json:"servers"`
	}
	require.NoError(t, json.Unmarshal([]byte(get()), &got))
	require.Len(t, got.Servers, 1)
	assert.Equal(t, server.ModelServerRow{
		Var: "ANTHROPIC_BASE_URL", URL: "http://127.0.0.1:8000",
		Profiles: []string{"(default)", "box"}, Pools: []string{"box"}, Remote: true,
	}, got.Servers[0])
}
