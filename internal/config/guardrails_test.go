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
	lanes := config.GuardrailLanesConfig{Human: config.DefaultGuardrailHumanLane}
	cases := []struct {
		name  string
		extra string
		want  config.GuardrailsConfig
	}{
		{"absent uses defaults", "", config.GuardrailsConfig{Enabled: true, ApprovalCommands: defaults, Lanes: lanes}},
		{"null list uses defaults", "guardrails:\n  approval_commands:\n", config.GuardrailsConfig{Enabled: true, ApprovalCommands: defaults, Lanes: lanes}},
		{"all set", "guardrails:\n  enabled: false\n  allow_force_push: true\n  approval_commands:\n    - \"helm  upgrade *-prod\"\n    - \"helm upgrade *-prod\"\n    - \"\"\n",
			config.GuardrailsConfig{Enabled: false, AllowForcePush: true, ApprovalCommands: []string{"helm upgrade *-prod"}, Lanes: lanes}},
		{"empty list turns approvals off", "guardrails:\n  approval_commands: []\n", config.GuardrailsConfig{Enabled: true, Lanes: lanes}},
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

// guardrails.lanes: absent, the human lane is the documented default and
// second_opinion is empty; a user's list replaces the default (an empty
// list turns the lane off); patterns are trimmed and de-duplicated; a
// pattern that can never match is refused at validation.
func TestLanesConfigRoundtrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  config.GuardrailLanesConfig
	}{
		{"absent uses defaults", "", config.GuardrailLanesConfig{Human: config.DefaultGuardrailHumanLane}},
		{"empty lanes block uses defaults", "guardrails:\n  lanes: {}\n", config.GuardrailLanesConfig{Human: config.DefaultGuardrailHumanLane}},
		{"null human uses defaults", "guardrails:\n  lanes:\n    human:\n    second_opinion: [\"internal/auth/**\"]\n",
			config.GuardrailLanesConfig{Human: config.DefaultGuardrailHumanLane, SecondOpinion: []string{"internal/auth/**"}}},
		{"set: a user list replaces the default", "guardrails:\n  lanes:\n    human:\n      - \" .github/workflows/** \"\n      - \".github/workflows/**\"\n      - \"\"\n      - \"deploy/**\"\n    second_opinion:\n      - \"go.mod\"\n      - \"migrations/**\"\n",
			config.GuardrailLanesConfig{Human: []string{".github/workflows/**", "deploy/**"}, SecondOpinion: []string{"go.mod", "migrations/**"}}},
		{"empty human list turns the lane off", "guardrails:\n  lanes:\n    human: []\n", config.GuardrailLanesConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Guardrails.Lanes)
			require.NoError(t, config.ValidateGuardrailLanes(cfg.Guardrails.Lanes))
		})
	}
	assert.Equal(t, []string{".github/**", ".gitlab-ci.yml", ".circleci/**", ".itervox/**", "!.itervox/handoff/**",
		"WORKFLOW.md", "CODEOWNERS", ".github/CODEOWNERS", "jest.config.*", "vitest.config.*", "pytest.ini",
		".coveragerc", "codecov.yml", ".golangci.yml", "Makefile"}, config.DefaultGuardrailHumanLane)

	for _, bad := range []config.GuardrailLanesConfig{{Human: []string{".github/[ci"}}, {SecondOpinion: []string{"!"}}, {Human: []string{"/"}}} {
		assert.Error(t, config.ValidateGuardrailLanes(bad), "%+v", bad)
	}
}
