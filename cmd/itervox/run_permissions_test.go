package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// TestRunModesFlagFullPermissions: a run is "full permissions" unless an
// operating-system sandbox wraps it or a permission mode that confines its
// backend applies. agent.command always runs in bypass mode.
func TestRunModesFlagFullPermissions(t *testing.T) {
	a := config.AgentConfig{
		Command: "claude",
		Profiles: map[string]config.AgentProfile{
			"boxed":      {Command: "claude", Isolation: config.IsolationProcess},
			"restricted": {Command: "codex", PermissionMode: "sandbox"},
			"cursor":     {Command: "cursor-agent", PermissionMode: "sandbox"},
			"custom":     {Command: "./my-agent", Backend: config.BackendCustom, PermissionMode: "sandbox"},
			"typo":       {Command: "claude", PermissionMode: "sandbx"},
			"off":        {Command: "claude", Enabled: new(false)},
		},
	}
	byName := map[string]profileRunMode{}
	var order []string
	for _, m := range runModes(a) {
		byName[m.Name] = m
		order = append(order, m.Name)
	}
	assert.Equal(t, []string{defaultProfileLabel, "boxed", "cursor", "custom", "restricted", "typo"}, order,
		"agent.command first, then enabled profiles by name")

	assert.True(t, byName[defaultProfileLabel].Full)
	assert.Equal(t, agent.PermissionBypass, byName[defaultProfileLabel].Permission)
	assert.False(t, byName["boxed"].Full, "process isolation confines any permission mode")
	assert.False(t, byName["restricted"].Full, "codex sandbox mode confines")
	assert.True(t, byName["cursor"].Full, "Cursor passes --force --trust in sandbox mode too")
	assert.True(t, byName["custom"].Full, "a custom backend gets no permission flags")
	assert.True(t, byName["typo"].Full, "an unknown mode runs as bypass")
	assert.Equal(t, agent.PermissionBypass, byName["typo"].Permission)

	assert.Equal(t, []string{defaultProfileLabel, "cursor", "custom", "typo"}, fullPermissionNames(runModes(a)))

	a.Isolation = config.IsolationProcess
	assert.False(t, runModes(a)[0].Full, "agent.isolation: process confines agent.command")
}

// TestDoctorWarnsOnFullPermissions: doctor's Permissions section lists each
// run mode and warns, pointing at the safety guide, when any runs with full
// permissions; the warning never changes the exit code.
func TestDoctorWarnsOnFullPermissions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "claude"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "codex", PermissionMode: "sandbox"},
	}
	c := checkPermissions(cfg)
	var b strings.Builder
	renderPermissionsCheck(&b, c)
	out := b.String()
	assert.Contains(t, out, "\nPermissions\n")
	assert.Contains(t, out, "(default): claude, permission_mode bypass, isolation none — full permissions")
	assert.Contains(t, out, "reviewer: codex, permission_mode sandbox, isolation none — restricted permission mode")
	assert.Contains(t, out, "WARNING: (default) run with full permissions")
	assert.Contains(t, out, "on this machine")
	assert.Contains(t, out, safetyGuideURL)
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Permissions: c}))

	cfg.Agent.SSHHosts = []string{"worker-1"}
	b.Reset()
	renderPermissionsCheck(&b, checkPermissions(cfg))
	assert.Contains(t, b.String(), "on the SSH worker hosts")

	cfg.Agent.SSHHosts = nil
	cfg.Agent.Isolation = config.IsolationProcess
	b.Reset()
	renderPermissionsCheck(&b, checkPermissions(cfg))
	assert.NotContains(t, b.String(), "WARNING", "everything sandboxed: no warning")
	assert.Contains(t, b.String(), "operating-system sandbox")
}

// TestConfigShowListsHowAgentsRun: `config show` ends with how each agent
// runs and names the full-permission ones.
func TestConfigShowListsHowAgentsRun(t *testing.T) {
	out := renderRunModes(runModes(config.AgentConfig{Command: "claude"}))
	assert.Contains(t, out, "\nHow agents run\n")
	assert.Contains(t, out, "(default): claude, permission_mode bypass, isolation none — full permissions")
	assert.Contains(t, out, "(default) run with full permissions; see "+safetyGuideURL)
}
