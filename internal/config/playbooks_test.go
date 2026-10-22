package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestPlaybooksConfig (#218): agent.playbooks absent → on, the default
// directory, the classifier on and the documented label map; a set labels
// map replaces the defaults (keys lower-cased).
func TestPlaybooksConfig(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	pc := cfg.Agent.Playbooks
	assert.True(t, pc.Enabled)
	assert.True(t, pc.Classify)
	assert.Equal(t, ".itervox/playbooks", pc.Dir)
	assert.Equal(t, config.DefaultPlaybookLabels, pc.Labels)
	assert.Equal(t, "bug-fix", pc.Labels["bug"])

	cfg, err = config.Load(workflowWithContent(t, minimal("agent:\n  playbooks:\n    enabled: false\n    classify: false\n    dir: docs/playbooks\n    labels:\n      Defect: bug-fix\n      \"\": feature\n")))
	require.NoError(t, err)
	pc = cfg.Agent.Playbooks
	assert.False(t, pc.Enabled)
	assert.False(t, pc.Classify)
	assert.Equal(t, "docs/playbooks", pc.Dir)
	assert.Equal(t, map[string]string{"defect": "bug-fix"}, pc.Labels)
}
