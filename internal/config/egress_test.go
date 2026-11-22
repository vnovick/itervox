package config_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestEgressConfigRoundtrip: agent.egress, agent.egress_allow,
// agent.egress_defaults and the profile keys egress, egress_allow and
// egress_defaults parse, and default to off / empty / true / inherit.
func TestEgressConfigRoundtrip(t *testing.T) {
	path := workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n"+
			"  isolation: process\n"+
			"  egress: allowlist\n"+
			"  egress_allow:\n"+
			"    - Registry.Example.com\n"+
			"    - \"*.corp.example:8443\"\n"+
			"  egress_defaults: false\n"+
			"  profiles:\n"+
			"    tight:\n"+
			"      command: claude\n"+
			"      egress_allow: [\"mirror.example.org\"]\n"+
			"      egress_defaults: true\n"+
			"    open:\n"+
			"      command: claude\n"+
			"      egress: \"off\"\n"+
			"    inherits:\n"+
			"      command: codex\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.EgressAllowlist, cfg.Agent.Egress)
	assert.Equal(t, []string{"registry.example.com", "*.corp.example:8443"}, cfg.Agent.EgressAllow)
	assert.False(t, cfg.Agent.EgressDefaults)
	tight := cfg.Agent.Profiles["tight"]
	assert.Empty(t, tight.Egress, "unset inherits agent.egress")
	assert.Equal(t, []string{"mirror.example.org"}, tight.EgressAllow)
	require.NotNil(t, tight.EgressDefaults)
	assert.True(t, *tight.EgressDefaults)
	assert.Equal(t, config.EgressOff, cfg.Agent.Profiles["open"].Egress)
	assert.Nil(t, cfg.Agent.Profiles["inherits"].EgressDefaults)
	assert.Equal(t, []string{"(default)", "inherits", "tight"}, config.EgressProfiles(cfg.Agent))

	// Absent: off, nothing allowed beyond the defaults, defaults on.
	path = workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  profiles:\n    plain:\n      command: claude\n"))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.EgressOff, cfg.Agent.Egress, "the default leaves the network open")
	assert.Empty(t, cfg.Agent.EgressAllow)
	assert.True(t, cfg.Agent.EgressDefaults)
	assert.Empty(t, cfg.Agent.Profiles["plain"].Egress)
	assert.Nil(t, cfg.Agent.Profiles["plain"].EgressDefaults)
	assert.False(t, config.ResolveEgress(cfg.Agent.Profiles["plain"], cfg.Agent, "claude", "claude", nil).On)
}

// TestEgressRefusedWithoutIsolation: egress: allowlist on a profile (or
// agent.command) that does not run with isolation: process fails the load,
// naming it.
func TestEgressRefusedWithoutIsolation(t *testing.T) {
	cases := map[string]string{
		"default without isolation": "agent:\n  egress: allowlist\n",
		"profile without isolation": "agent:\n  profiles:\n    p:\n      command: claude\n      egress: allowlist\n",
		"profile opts out of isolation": "agent:\n  isolation: process\n  egress: allowlist\n" +
			"  profiles:\n    p:\n      command: claude\n      isolation: none\n",
	}
	for name, body := range cases {
		_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, body)))
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, config.ErrEgressNeedsIsolation), "%s: %v", name, err)
		assert.Contains(t, err.Error(), "isolation: process", name)
	}
	// A profile with its own sandbox may set egress while the default runs
	// unconfined.
	_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  profiles:\n    p:\n      command: claude\n      isolation: process\n      egress: allowlist\n")))
	assert.NoError(t, err)
}

func TestEgressRefusesBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"unknown mode":    "agent:\n  isolation: process\n  egress: open\n",
		"profile mode":    "agent:\n  isolation: process\n  profiles:\n    p:\n      command: claude\n      egress: maybe\n",
		"url entry":       "agent:\n  isolation: process\n  egress_allow: [\"https://example.com/x\"]\n",
		"star entry":      "agent:\n  isolation: process\n  egress_allow: [\"*\"]\n",
		"profile entry":   "agent:\n  isolation: process\n  profiles:\n    p:\n      command: claude\n      egress_allow: [\"a b\"]\n",
		"bad port number": "agent:\n  isolation: process\n  egress_allow: [\"example.com:70000\"]\n",
	} {
		_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, body)))
		assert.Error(t, err, name)
	}
}

// TestResolveEgressDefaultsAndModelServers: the allowlist is the backend's
// model hosts plus the registries (unless egress_defaults is off), the
// agent's and the profile's entries, and remote model servers; a loopback
// model server becomes a local port instead.
func TestResolveEgressDefaultsAndModelServers(t *testing.T) {
	a := config.AgentConfig{Isolation: config.IsolationProcess, Egress: config.EgressAllowlist,
		EgressAllow: []string{"agent.example"}, EgressDefaults: true}
	p := config.AgentProfile{EgressAllow: []string{"profile.example"}}

	r := config.ResolveEgress(p, a, "", "claude --model x", nil)
	require.True(t, r.On)
	assert.Contains(t, r.Allow, "api.anthropic.com")
	assert.Contains(t, r.Allow, "registry.npmjs.org")
	assert.Contains(t, r.Allow, "agent.example")
	assert.Contains(t, r.Allow, "profile.example")
	assert.NotContains(t, r.Allow, "api.openai.com")

	r = config.ResolveEgress(p, a, "codex", "codex", nil)
	assert.Contains(t, r.Allow, "api.openai.com")
	assert.NotContains(t, r.Allow, "api.anthropic.com")

	off := false
	r = config.ResolveEgress(config.AgentProfile{EgressDefaults: &off}, a, "claude", "claude", nil)
	assert.Equal(t, []string{"agent.example"}, r.Allow, "egress_defaults: false drops the defaults")

	r = config.ResolveEgress(config.AgentProfile{EgressDefaults: &off}, a, "claude",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude", []string{"https://llm.example.com/v1", "$BASE"})
	assert.Equal(t, []int{8000}, r.LocalPorts, "a loopback model server is reached outside the proxy")
	assert.Contains(t, r.Allow, "llm.example.com:443")

	assert.False(t, config.ResolveEgress(config.AgentProfile{Egress: config.EgressOff}, a, "claude", "claude", nil).On)
}
