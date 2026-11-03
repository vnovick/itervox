package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// #178: agent.live_steering opts in to writing operator messages into a
// running local Claude turn. Off by default.
func TestAgentLiveSteeringRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  bool
	}{
		{"absent defaults off", "", false},
		{"enabled", "agent:\n  live_steering: true\n", true},
		{"explicitly disabled", "agent:\n  live_steering: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Agent.LiveSteering)
		})
	}
}
