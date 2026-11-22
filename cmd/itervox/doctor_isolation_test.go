package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// TestDoctorIsolationSection (#258): doctor's Isolation section lists what
// this machine has and which profiles ask for process isolation, prints
// the policy lists, and fails doctor when a profile asks for a sandbox
// that no mechanism here provides.
func TestDoctorIsolationSection(t *testing.T) {
	cfg := &config.Config{WorkflowPath: "/p/WORKFLOW.md"}
	cfg.Workspace.Root = "/ws"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"boxed": {Command: "claude", Isolation: config.IsolationProcess},
		"open":  {Command: "claude"},
	}
	prev := isolationMechanismReport
	t.Cleanup(func() { isolationMechanismReport = prev })

	isolationMechanismReport = func() []agent.IsolationMechanismStatus {
		return []agent.IsolationMechanismStatus{
			{Name: "bubblewrap", Err: errors.New("bwrap is not installed")},
			{Name: "landlock"},
		}
	}
	c := checkIsolation(cfg)
	var b strings.Builder
	renderIsolationCheck(&b, c)
	out := b.String()
	assert.Contains(t, out, "\nIsolation\n")
	assert.Contains(t, out, "bubblewrap: unavailable — bwrap is not installed")
	assert.Contains(t, out, "landlock: available")
	assert.Contains(t, out, "profiles with isolation: process: boxed")
	assert.Contains(t, out, "sandbox: landlock")
	assert.Contains(t, out, "~/.claude")
	assert.Contains(t, out, "~/.ssh")
	assert.Contains(t, out, "/p/.itervox/.env")
	assert.Contains(t, out, "every other workspace under /ws")
	assert.False(t, c.Failing())
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Isolation: c}))

	isolationMechanismReport = func() []agent.IsolationMechanismStatus {
		return []agent.IsolationMechanismStatus{{Name: "bubblewrap", Err: errors.New("bwrap is not installed")}}
	}
	c = checkIsolation(cfg)
	b.Reset()
	renderIsolationCheck(&b, c)
	assert.True(t, c.Failing())
	assert.Contains(t, b.String(), "ERROR: boxed ask for process isolation but no sandbox works on this machine")
	assert.Equal(t, 1, doctorExitCode(DoctorReport{SchemaPassed: true, Isolation: c}))

	cfg.Agent.Profiles = map[string]config.AgentProfile{"open": {Command: "claude"}}
	c = checkIsolation(cfg)
	assert.False(t, c.Failing(), "nothing asks for a sandbox: no failure")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Isolation: c}))
}

// TestDoctorEgressSection: doctor reports whether this machine can enforce
// egress (a sandbox that closes the network), lists the profiles with
// egress: allowlist, and fails when they ask for it where it cannot hold.
func TestDoctorEgressSection(t *testing.T) {
	cfg := &config.Config{WorkflowPath: "/p/WORKFLOW.md"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"closed": {Command: "claude", Isolation: config.IsolationProcess, Egress: config.EgressAllowlist},
	}
	prevM, prevE := isolationMechanismReport, egressAvailability
	t.Cleanup(func() { isolationMechanismReport, egressAvailability = prevM, prevE })
	isolationMechanismReport = func() []agent.IsolationMechanismStatus {
		return []agent.IsolationMechanismStatus{{Name: "bubblewrap"}, {Name: "landlock"}}
	}
	egressAvailability = func() (string, error) { return "bubblewrap", nil }
	c := checkIsolation(cfg)
	var b strings.Builder
	renderIsolationCheck(&b, c)
	out := b.String()
	assert.Contains(t, out, "egress enforcement: available via bubblewrap")
	assert.Contains(t, out, "profiles with egress: allowlist: closed")
	assert.Contains(t, out, "network: closed for them but the allowlist, through a per-run proxy (bubblewrap)")
	assert.Contains(t, out, "~/.git-credentials")
	assert.Contains(t, out, "api.anthropic.com")
	assert.False(t, c.Failing())

	egressAvailability = func() (string, error) {
		return "", errors.New("agent: process isolation unavailable: bubblewrap: bwrap cannot create network and pid namespaces here; landlock: cannot restrict network by host")
	}
	c = checkIsolation(cfg)
	b.Reset()
	renderIsolationCheck(&b, c)
	out = b.String()
	assert.Contains(t, out, "egress enforcement: unavailable — bubblewrap: bwrap cannot create network and pid namespaces here; landlock: cannot restrict network by host")
	assert.Contains(t, out, "ERROR: closed ask for egress: allowlist but no sandbox here can close the network")
	assert.True(t, c.Failing(), "landlock alone keeps isolation but not egress: the egress runs are refused")
	assert.Equal(t, 1, doctorExitCode(DoctorReport{SchemaPassed: true, Isolation: c}))

	cfg.Agent.Profiles = map[string]config.AgentProfile{"boxed": {Command: "claude", Isolation: config.IsolationProcess}}
	c = checkIsolation(cfg)
	b.Reset()
	renderIsolationCheck(&b, c)
	assert.False(t, c.Failing(), "no profile asks for egress: no failure")
	assert.Contains(t, b.String(), "network: allowed (no profile sets egress: allowlist)")
}
