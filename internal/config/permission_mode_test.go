package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestProfilePermissionModeParses is issue #66's config acceptance: the field
// must parse per profile and default to the pre-#66 behaviour when unset.
func TestProfilePermissionModeParses(t *testing.T) {
	path := workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n"+
			"  profiles:\n"+
			"    sandboxed:\n"+
			"      command: claude\n"+
			"      permission_mode: sandbox\n"+
			"    legacy:\n"+
			"      command: claude\n"))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "sandbox", cfg.Agent.Profiles["sandboxed"].PermissionMode,
		"an explicit permission_mode must reach the profile")
	assert.Empty(t, cfg.Agent.Profiles["legacy"].PermissionMode,
		"an unset field stays empty so agent.ParsePermissionMode applies the default")
}
