package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestVisibilityConfigRoundTrip: agent.resource_sampling,
// agent.activity_audit, workspace.disk_warn_free_gb and
// workspace.disk_pause_free_gb parse, default when absent (sampling on,
// sampled audit, warn at 5 GB, never pause), reject an unknown mode to the
// default and clamp negative thresholds to 0 (off).
func TestVisibilityConfigRoundTrip(t *testing.T) {
	type got struct {
		sampling   bool
		mode       string
		warn, stop int
	}
	cases := []struct {
		name  string
		extra string
		want  got
	}{
		{"absent uses defaults", "", got{true, config.ActivityAuditSampled, config.DefaultDiskWarnFreeGB, 0}},
		{"all set", "agent:\n  resource_sampling: false\n  activity_audit: precise\nworkspace:\n  disk_warn_free_gb: 20\n  disk_pause_free_gb: 3\n",
			got{false, config.ActivityAuditPrecise, 20, 3}},
		{"off and zero", "agent:\n  activity_audit: OFF\nworkspace:\n  disk_warn_free_gb: 0\n", got{true, config.ActivityAuditOff, 0, 0}},
		{"unknown mode and negatives", "agent:\n  activity_audit: everything\nworkspace:\n  disk_warn_free_gb: -1\n  disk_pause_free_gb: -4\n",
			got{true, config.ActivityAuditSampled, 0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got{cfg.Agent.ResourceSampling, cfg.Agent.ActivityAudit, cfg.Workspace.DiskWarnFreeGB, cfg.Workspace.DiskPauseFreeGB})
		})
	}
}
