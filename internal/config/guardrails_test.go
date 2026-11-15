package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// #204: the guardrails: block. Absent, guardrails are on with no force
// pushes and the default approval commands; a list replaces the defaults
// (an empty one turns approvals off).
func TestGuardrailsConfigRoundTrip(t *testing.T) {
	defaults := config.DefaultGuardrailApprovalCommands
	cases := []struct {
		name  string
		extra string
		want  config.GuardrailsConfig
	}{
		{"absent uses defaults", "", config.GuardrailsConfig{Enabled: true, ApprovalCommands: defaults}},
		{"null list uses defaults", "guardrails:\n  approval_commands:\n", config.GuardrailsConfig{Enabled: true, ApprovalCommands: defaults}},
		{"all set", "guardrails:\n  enabled: false\n  allow_force_push: true\n  approval_commands:\n    - \"helm  upgrade *-prod\"\n    - \"helm upgrade *-prod\"\n    - \"\"\n",
			config.GuardrailsConfig{Enabled: false, AllowForcePush: true, ApprovalCommands: []string{"helm upgrade *-prod"}}},
		{"empty list turns approvals off", "guardrails:\n  approval_commands: []\n", config.GuardrailsConfig{Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Guardrails)
		})
	}
	assert.Equal(t, []string{"vercel --prod", "fly deploy", "kubectl apply", "terraform apply", "gcloud deploy", "npm publish", "cargo publish"}, defaults)
}
