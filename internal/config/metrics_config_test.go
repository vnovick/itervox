package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// CORE-045: server.metrics.enabled opts in to GET /metrics. Off by default.
func TestServerMetricsEnabledRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  bool
	}{
		{"absent defaults off", "", false},
		{"server block without metrics", "server:\n  host: 127.0.0.1\n", false},
		{"enabled", "server:\n  metrics:\n    enabled: true\n", true},
		{"explicitly disabled", "server:\n  metrics:\n    enabled: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Server.Metrics.Enabled)
		})
	}
}
