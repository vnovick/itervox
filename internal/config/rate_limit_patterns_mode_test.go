package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRateLimitPatterns_InvalidModeRejectedAtLoad (CORE-100): an unknown
// agent.rate_limit_error_patterns_mode fails the load naming the field;
// omitted means "replace" (the pre-CORE-100 behaviour).
func TestRateLimitPatterns_InvalidModeRejectedAtLoad(t *testing.T) {
	_, err := Load(writeWorkflowAt(t, t.TempDir(), "agent:\n  rate_limit_error_patterns_mode: merge\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit_error_patterns_mode")

	for _, mode := range []string{"extend", "replace"} {
		cfg, err := Load(writeWorkflowAt(t, t.TempDir(), "agent:\n  rate_limit_error_patterns_mode: "+mode+"\n"))
		require.NoError(t, err, mode)
		assert.Equal(t, mode, cfg.Agent.RateLimitErrorPatternsMode)
	}
	cfg, err := Load(writeWorkflowAt(t, t.TempDir(), ""))
	require.NoError(t, err)
	assert.Equal(t, RateLimitPatternsModeReplace, cfg.Agent.RateLimitErrorPatternsMode)
}
