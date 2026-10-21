package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestMemoryConfigRoundTrip (#202): the memory: block parses its keys;
// absent, memory is on with a 2000-token budget inside an 8000-token
// rules-plus-memory share, and non-positive budgets fall back to those.
func TestMemoryConfigRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  config.MemoryConfig
	}{
		{"absent uses defaults", "", config.MemoryConfig{Enabled: true, TokenBudget: 2000, PromptShareTokens: 8000}},
		{"all set", "memory:\n  enabled: false\n  token_budget: 500\n  prompt_share_tokens: 3000\n",
			config.MemoryConfig{Enabled: false, TokenBudget: 500, PromptShareTokens: 3000}},
		{"non-positive falls back", "memory:\n  token_budget: 0\n  prompt_share_tokens: -1\n",
			config.MemoryConfig{Enabled: true, TokenBudget: 2000, PromptShareTokens: 8000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Memory)
		})
	}
	m := config.MemoryConfig{TokenBudget: 2000, PromptShareTokens: 8000}
	assert.Equal(t, 2000, m.MemoryBudget(100))
	assert.Equal(t, 500, m.MemoryBudget(7500), "rules take their share first")
	assert.Equal(t, 0, m.MemoryBudget(9000))
}
