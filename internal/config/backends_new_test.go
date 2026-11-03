package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// TestNewBackendsAreSupported: opencode, cursor and custom are backends
// everywhere config names one; the binary map knows each CLI (custom has
// none); anything else, or a near-miss spelling, is not a backend.
func TestNewBackendsAreSupported(t *testing.T) {
	assert.Equal(t, []string{"claude", "codex", "opencode", "cursor", "custom"}, config.SupportedBackends)
	for _, b := range config.SupportedBackends {
		assert.True(t, config.IsSupportedBackend(b), b)
	}
	for _, b := range []string{"", "Cursor", " opencode", "cursor-agent", "agent", "gemini"} {
		assert.False(t, config.IsSupportedBackend(b), b)
	}
	assert.Equal(t, "claude, codex, opencode, cursor or custom", config.SupportedBackendsText())
	assert.Equal(t, "cursor-agent", config.BackendBinary("cursor"))
	assert.Equal(t, "opencode", config.BackendBinary("opencode"))
	assert.Equal(t, "", config.BackendBinary("custom"))
	for cmd, want := range map[string]string{
		"opencode --model anthropic/x":          "opencode",
		"/opt/bin/cursor-agent --model gpt-5":   "cursor",
		"FOO=1 env cursor-agent":                "cursor",
		"agent -p":                              "", // Cursor's generic alias is not claimed
		"cursor":                                "", // the editor launcher is not the CLI
		"custom":                                "", // custom has no binary
		"@@itervox-backend=custom ./wrapper.sh": "custom",
	} {
		assert.Equal(t, want, config.BackendFromCommand(cmd), cmd)
	}
}

// TestNewBackendsConfigRoundTrip: agent.backend, a profile's backend and
// the fallback chain / profile_map accept the new backends through Load and
// validation, and the settings enum lists them.
func TestNewBackendsConfigRoundTrip(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal(
		"agent:\n"+
			"  command: ./my-agent.sh\n"+
			"  backend: custom\n"+
			"  profiles:\n"+
			"    coder:\n"+
			"      command: opencode --model anthropic/claude-sonnet-4-5\n"+
			"    coder-cursor:\n"+
			"      command: cursor-agent --model gpt-5\n"+
			"    wrapped:\n"+
			"      command: ./cursor-wrapper.sh\n"+
			"      backend: cursor\n"+
			"  backend_fallback:\n"+
			"    chain: [opencode, cursor]\n"+
			"    profile_map:\n"+
			"      coder:\n"+
			"        cursor: coder-cursor\n")))
	require.NoError(t, err)
	assert.Equal(t, "custom", cfg.Agent.Backend)
	assert.Equal(t, "cursor", cfg.Agent.Profiles["wrapped"].Backend)
	assert.Equal(t, []string{"opencode", "cursor"}, cfg.Agent.BackendFallback.Chain)
	assert.Equal(t, "coder-cursor", cfg.Agent.BackendFallback.Lookup("coder", "cursor"))
	assert.Equal(t, "opencode", config.ProfileRunsBackend(cfg.Agent.Profiles["coder"], cfg.Agent.Command))
	assert.Equal(t, "cursor", config.ProfileRunsBackend(cfg.Agent.Profiles["wrapped"], cfg.Agent.Command))

	fields := config.SettingFields
	var enum []string
	for _, f := range fields {
		if f.Key == "agent.backend" {
			enum = f.Enum
		}
	}
	assert.Equal(t, []string{"", "claude", "codex", "opencode", "cursor", "custom"}, enum)

	// A chain naming an unknown backend is still rejected, with the list.
	require.NoError(t, config.ValidateBackendFallback(cfg.Agent.BackendFallback, cfg.Agent.Profiles, cfg.Agent.Command))
	bad := cfg.Agent.BackendFallback
	bad.Chain = []string{"claude", "gemini"}
	err = config.ValidateBackendFallback(bad, cfg.Agent.Profiles, cfg.Agent.Command)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude, codex, opencode, cursor or custom")
}

// TestSwitchToBackendAcceptsNewBackends: a rate_limited rule may switch to
// opencode, cursor or custom; an unknown backend is still refused.
func TestSwitchToBackendAcceptsNewBackends(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"primary":  {Command: "claude"},
		"fallback": {Command: "./wrapper.sh"},
	}
	for _, b := range []string{"opencode", "cursor", "custom"} {
		rules := switchRuleAutomations()
		rules[0].Policy.SwitchToBackend = b
		require.NoError(t, config.ValidateAutomations(rules, profiles), b)
	}
	rules := switchRuleAutomations()
	rules[0].Policy.SwitchToBackend = "gemini"
	err := config.ValidateAutomations(rules, profiles)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "switch_to_backend must be empty or one of")

	// The binary still wins: an opencode command cannot switch to cursor.
	profiles["fallback"] = config.AgentProfile{Command: "opencode"}
	rules[0].Policy.SwitchToBackend = "cursor"
	require.Error(t, config.ValidateAutomations(rules, profiles))
}
