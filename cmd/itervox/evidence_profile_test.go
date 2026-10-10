package main

import (
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

// TestUpsertProfileKeepsRequireEvidenceAndPermissionMode (#80): a dashboard
// profile save does not edit require_evidence or permission_mode, so it must
// carry them over instead of stripping them from WORKFLOW.md (permission_mode
// was being stripped before). Names YAML would otherwise read as a number or
// a boolean ("123", "true") are written back quoted, so the saved workflow
// still loads (#80 review).
func TestUpsertProfileKeepsRequireEvidenceAndPermissionMode(t *testing.T) {
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
    qa:
      command: claude
      permission_mode: sandbox
      require_evidence:
        - test
        - ci
        - "123"
        - "true"
` + testProfileFileFields(t, dir, "qa") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	require.Equal(t, []string{"test", "ci", "123", "true"}, cfg.Agent.Profiles["qa"].RequireEvidence)

	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("qa", server.ProfileDef{
		Command: "claude --model claude-sonnet-4-6",
		Enabled: true,
	}, "qa"))

	reloaded, err := config.Load(workflowPath)
	require.NoError(t, err)
	qa := reloaded.Agent.Profiles["qa"]
	assert.Equal(t, "claude --model claude-sonnet-4-6", qa.Command, "the save applied")
	assert.Equal(t, []string{"test", "ci", "123", "true"}, qa.RequireEvidence, "require_evidence survives a dashboard save")
	assert.Equal(t, "sandbox", qa.PermissionMode, "permission_mode survives a dashboard save")
	assert.Equal(t, []string{"test", "ci", "123", "true"}, orch.ProfilesCfg()["qa"].RequireEvidence)
}
