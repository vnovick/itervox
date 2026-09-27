package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

const vendorDefaultOnly = "Error: rate_limit_exceeded"
const customOnly = "upstream returned ANTHROPIC-OVERLOAD-503"

// TestRateLimitPatterns_ExtendModeKeepsDefaults (CORE-100).
func TestRateLimitPatterns_ExtendModeKeepsDefaults(t *testing.T) {
	custom := []string{"anthropic-overload"}
	assert.True(t, IsRateLimitFailureWithPatternsMode(vendorDefaultOnly, custom, config.RateLimitPatternsModeExtend),
		"extend keeps every built-in default")
	assert.True(t, IsRateLimitFailureWithPatternsMode(customOnly, custom, config.RateLimitPatternsModeExtend),
		"extend adds the operator's patterns")
	assert.True(t, IsRateLimitFailureWithPatternsMode("API Error: 429 {", custom, config.RateLimitPatternsModeExtend),
		"extend keeps the agent-side standalone 429 rule")
	assert.False(t, IsRateLimitFailureWithPatternsMode("compile error", custom, config.RateLimitPatternsModeExtend))
}

// TestRateLimitPatterns_ReplaceModeDropsDefaults (CORE-100).
func TestRateLimitPatterns_ReplaceModeDropsDefaults(t *testing.T) {
	custom := []string{"anthropic-overload"}
	assert.False(t, IsRateLimitFailureWithPatternsMode(vendorDefaultOnly, custom, config.RateLimitPatternsModeReplace))
	assert.True(t, IsRateLimitFailureWithPatternsMode(customOnly, custom, config.RateLimitPatternsModeReplace))
	// "" (unset) is replace — the pre-CORE-100 behaviour.
	assert.False(t, IsRateLimitFailureWithPatternsMode(vendorDefaultOnly, custom, ""))
}

// TestRateLimitPatterns_EmptyListFallsBackToDefaults (CORE-100): with no
// custom patterns the defaults apply whatever the mode.
func TestRateLimitPatterns_EmptyListFallsBackToDefaults(t *testing.T) {
	for _, mode := range []string{"", config.RateLimitPatternsModeReplace, config.RateLimitPatternsModeExtend} {
		assert.True(t, IsRateLimitFailureWithPatternsMode(vendorDefaultOnly, nil, mode), mode)
		assert.True(t, IsRateLimitFailureWithPatternsMode(vendorDefaultOnly, []string{""}, mode), mode)
		assert.False(t, IsRateLimitFailureWithPatternsMode(customOnly, nil, mode), mode)
	}
}

// TestRateLimitPatterns_ConfigReachesClassifier (CORE-100): mode and
// patterns loaded from WORKFLOW.md reach the event loop's classifier
// (o.isRateLimitFailureCfg, read at the retry-exhaustion site) and the
// failure classifier as one consistent pair.
func TestRateLimitPatterns_ConfigReachesClassifier(t *testing.T) {
	load := func(mode string) *Orchestrator {
		dir := t.TempDir()
		path := filepath.Join(dir, "WORKFLOW.md")
		body := "---\nitervox_schema_version: 2\ntracker:\n  kind: github\nagent:\n" +
			"  rate_limit_error_patterns: [\"anthropic-overload\"]\n" +
			"  rate_limit_error_patterns_mode: " + mode + "\n---\n# p\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		cfg, err := config.Load(path)
		require.NoError(t, err)
		return New(cfg, nil, nil, nil)
	}
	ext := load("extend")
	assert.True(t, ext.isRateLimitFailureCfg(vendorDefaultOnly))
	assert.True(t, ext.isRateLimitFailureCfg(customOnly))
	assert.Equal(t, "rate limited", ext.classifyWorkerFailure(errors.New(vendorDefaultOnly)))

	rep := load("replace")
	assert.False(t, rep.isRateLimitFailureCfg(vendorDefaultOnly))
	assert.True(t, rep.isRateLimitFailureCfg(customOnly))
	assert.Equal(t, "agent error", rep.classifyWorkerFailure(errors.New(vendorDefaultOnly)))
}
