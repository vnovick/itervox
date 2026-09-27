package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-010 — the dashboard save path. internal/server's handleSetAutomations
// validates with the two-argument config.ValidateAutomations, which cannot see
// agent.command, so a switch profile with an EMPTY command (it inherits
// agent.command at dispatch) passes there. The adapter owns cfg and must
// re-validate with the effective default command BEFORE PatchAutomationsBlock
// writes WORKFLOW.md, so the rejected rule is never persisted.
func TestSetAutomations_RejectsInheritedSwitchBackendMismatchBeforePersist(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude --model opus
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"primary":  {Command: "claude"},
		"fallback": {}, // empty command → inherits agent.command ("claude ...")
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	rule := server.AutomationDef{
		ID:      "rl-switch",
		Enabled: true,
		Profile: "primary",
		Trigger: server.AutomationTriggerDef{Type: config.AutomationTriggerRateLimited},
		Policy: server.AutomationPolicyDef{
			AutoResume:      true,
			SwitchToProfile: "fallback",
			SwitchToBackend: "codex",
		},
	}
	err = adapter.SetAutomations([]server.AutomationDef{rule})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "inherited agent.command")
	after, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Equal(t, content, string(after), "a rejected rule must not be persisted")
	assert.Nil(t, orch.AutomationsCfg(), "a rejected rule must not reach the runtime config")
}

// CORE-010 — the same rule driven through the real dashboard route
// (PUT /api/v1/settings/automations → handleSetAutomations → adapter), without
// stubbing the client: both the direct and the inherited mismatch are
// rejected by the handler (which resolves the inherited command through the
// adapter's DefaultAgentCommand) with the same 400 invalid_policy /
// switchToBackend shape. In both rows WORKFLOW.md is byte-identical afterwards
// and no automation reaches the runtime config.
func TestDashboardSaveAutomations_RejectsSwitchBackendMismatchBeforePersist(t *testing.T) {
	tests := []struct {
		name            string
		fallbackCommand string
		wantStatus      int
	}{
		{"direct mismatch rejected by handler", "claude --model opus", http.StatusBadRequest},
		{"inherited mismatch rejected by handler", "", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			workflowPath := filepath.Join(dir, "WORKFLOW.md")
			content := "---\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\nagent:\n  command: claude --model opus\n---\n\nPrompt.\n"
			require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
			cfg, err := config.Load(workflowPath)
			require.NoError(t, err)
			cfg.Agent.Profiles = map[string]config.AgentProfile{
				"primary":  {Command: "claude"},
				"fallback": {Command: tc.fallbackCommand},
			}
			mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
			adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}
			srv := server.New(server.Config{
				Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
				RefreshChan: make(chan struct{}, 1),
				Client:      adapter,
				// httptest.NewRequest addresses example.com; no token => CORE-162 Host guard.
				AllowedHosts: []string{"example.com"},
			})

			body, err := json.Marshal(map[string]any{"automations": []server.AutomationDef{{
				ID:      "rl-switch",
				Enabled: true,
				Profile: "primary",
				Trigger: server.AutomationTriggerDef{Type: config.AutomationTriggerRateLimited},
				Policy: server.AutomationPolicyDef{
					AutoResume:      true,
					SwitchToProfile: "fallback",
					SwitchToBackend: "codex",
				},
			}}})
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/automations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "switch_to_backend")
			assert.Contains(t, w.Body.String(), `"field":"switchToBackend"`)
			after, readErr := os.ReadFile(workflowPath)
			require.NoError(t, readErr)
			assert.Equal(t, content, string(after), "a rejected rule must not be persisted")
			assert.Nil(t, orch.AutomationsCfg())
		})
	}
}
