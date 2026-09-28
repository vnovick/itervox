package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// switchRuleAutomations is a minimal enabled rate_limited rule that switches
// to profile "fallback" with switch_to_backend: codex.
func switchRuleAutomations() []config.AutomationConfig {
	return []config.AutomationConfig{{
		ID:      "rl-switch",
		Enabled: true,
		Profile: "primary",
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRateLimited},
		Policy: config.AutomationPolicyConfig{
			AutoResume:      true,
			SwitchToProfile: "fallback",
			SwitchToBackend: "codex",
		},
	}}
}

// CORE-010 — switch_to_backend must agree with the backend derived from the
// switch profile's EFFECTIVE command (profile.command, else agent.command),
// mirroring resolveBackendForIssue in internal/orchestrator/dispatch_resolve.go.
func TestValidate_SwitchToBackendMismatchesProfileCommand(t *testing.T) {
	tests := []struct {
		name            string
		fallbackCommand string
		defaultCommand  string
		wantErr         bool
	}{
		{"direct mismatch", "claude --model opus", "claude", true},
		{"inherited mismatch", "", "claude --model opus", true},
		{"opaque wrapper", "./run-agent.sh", "claude", false},
		{"direct match", "codex --model gpt-5", "claude", false},
		{"inherited match", "", "codex", false},
		{"hinted mismatch", "@@itervox-backend=claude ./run-agent.sh", "codex", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profiles := map[string]config.AgentProfile{
				"primary":  {Command: "claude"},
				"fallback": {Command: tc.fallbackCommand},
			}
			err := config.ValidateAutomationsWithDefaults(switchRuleAutomations(), profiles, tc.defaultCommand)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "switch_to_backend")
				assert.Contains(t, err.Error(), "fallback")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// The dashboard save path (internal/server handleSetAutomations) calls the
// two-argument ValidateAutomations, which has no default command. It must
// still reject a direct mismatch, and must not reject the inherited case it
// cannot see (that one is rejected by the adapter before persisting).
func TestValidateAutomations_DirectSwitchBackendMismatchWithoutDefault(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"primary":  {Command: "claude"},
		"fallback": {Command: "claude --model opus"},
	}
	require.Error(t, config.ValidateAutomations(switchRuleAutomations(), profiles))

	profiles["fallback"] = config.AgentProfile{}
	require.NoError(t, config.ValidateAutomations(switchRuleAutomations(), profiles))
}

func TestBackendFromCommand(t *testing.T) {
	for cmd, want := range map[string]string{
		"claude -p x":                          "claude",
		"/usr/local/bin/codex exec":            "codex",
		"FOO=1 env -i codex exec":              "codex",
		"./run-agent.sh":                       "",
		"":                                     "",
		"@@itervox-backend=codex ./wrapper.sh": "codex",
	} {
		assert.Equal(t, want, config.BackendFromCommand(cmd), cmd)
	}
}

func TestIsEnvAssignment(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"FOO=bar", true},
		{"_VAR=123", true},
		{"A=", true},
		{"var123=val", true},
		{"=nope", false},
		{"nope", false},
		{"123=bad", false},
		{"-flag", false},
		{"", false},
		{"a.b=c", false},
	}
	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			assert.Equal(t, tc.want, config.IsEnvAssignment(tc.token))
		})
	}
}
