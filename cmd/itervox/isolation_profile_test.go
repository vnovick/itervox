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

// TestUpsertProfileKeepsIsolation (#258): the dashboard does not edit a
// profile's sandbox settings, so a profile save writes isolation and both
// path lists back to WORKFLOW.md unchanged, and the result loads again.
func TestUpsertProfileKeepsIsolation(t *testing.T) {
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
    boxed:
      command: claude
      isolation: process
      isolation_extra_write:
        - /opt/cache dir
      isolation_extra_deny:
        - /srv/secrets
` + testProfileFileFields(t, dir, "boxed") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("boxed", server.ProfileDef{Command: "claude --model m", Enabled: true}, "boxed"))

	reloaded, err := config.Load(workflowPath)
	require.NoError(t, err)
	boxed := reloaded.Agent.Profiles["boxed"]
	assert.Equal(t, "claude --model m", boxed.Command, "the save applied")
	assert.Equal(t, config.IsolationProcess, boxed.Isolation, "isolation survives a dashboard save")
	assert.Equal(t, []string{"/opt/cache dir"}, boxed.IsolationExtraWrite)
	assert.Equal(t, []string{"/srv/secrets"}, boxed.IsolationExtraDeny)
}

// TestUpsertProfileKeepsPush (push: daemon): the dashboard does not edit a
// profile's push mode, so a profile save writes push back to WORKFLOW.md
// unchanged, and the result loads again as patches only.
func TestUpsertProfileKeepsPush(t *testing.T) {
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
    patches:
      command: claude
      push: daemon
` + testProfileFileFields(t, dir, "patches") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("patches", server.ProfileDef{Command: "claude --model m", Enabled: true}, "patches"))

	reloaded, err := config.Load(workflowPath)
	require.NoError(t, err)
	p := reloaded.Agent.Profiles["patches"]
	assert.Equal(t, "claude --model m", p.Command, "the save applied")
	assert.Equal(t, config.PushDaemon, p.Push, "push: daemon survives a dashboard save")
}

// TestUpsertProfileKeepsEgress: the dashboard does not edit a profile's
// egress, so a profile save writes egress, egress_allow and
// egress_defaults back to WORKFLOW.md unchanged, and the result loads
// again with the network still closed.
func TestUpsertProfileKeepsEgress(t *testing.T) {
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
    closed:
      command: claude
      isolation: process
      egress: allowlist
      egress_allow:
        - mirror.example.org
        - "*.corp.example:8443"
      egress_defaults: false
` + testProfileFileFields(t, dir, "closed") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("closed", server.ProfileDef{Command: "claude --model m", Enabled: true}, "closed"))

	reloaded, err := config.Load(workflowPath)
	require.NoError(t, err)
	closed := reloaded.Agent.Profiles["closed"]
	assert.Equal(t, "claude --model m", closed.Command, "the save applied")
	assert.Equal(t, config.EgressAllowlist, closed.Egress, "egress survives a dashboard save")
	assert.Equal(t, []string{"mirror.example.org", "*.corp.example:8443"}, closed.EgressAllow)
	require.NotNil(t, closed.EgressDefaults)
	assert.False(t, *closed.EgressDefaults)
}
