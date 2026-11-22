package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClaudeFixtureUsageIncludesPromptCache: the Claude stream-json fixture
// maps onto TurnResult with input/output summed over the assistant messages
// (as before) and the prompt-cache counts read from the result event's
// usage only — the assistant messages' own cache fields are not added, so
// the turn's cache totals are the result's, not a multiple of them.
func TestClaudeFixtureUsageIncludesPromptCache(t *testing.T) {
	res := FinalizeResult(readFixture(t, "claude.jsonl", ParseLine))

	assert.Equal(t, "sess-claude-1", res.SessionID)
	assert.Equal(t, 25, res.InputTokens)
	assert.Equal(t, 80, res.OutputTokens)
	assert.Equal(t, 105, res.TotalTokens, "cache counts stay out of TotalTokens")
	assert.Equal(t, 4148, res.CacheReadTokens)
	assert.Equal(t, 2048, res.CacheCreationTokens)
	assert.Zero(t, res.CachedInputTokens, "Claude reports cache reads as cache_read_input_tokens")
	assert.Equal(t, 1, res.ToolCalls)
	assert.False(t, res.Failed)
}

// TestCodexFixtureUsageIncludesCachedInput: the Codex JSONL fixture's
// turn.completed usage maps onto TurnResult, cached input included; Codex
// reports no cache writes.
func TestCodexFixtureUsageIncludesCachedInput(t *testing.T) {
	res := FinalizeResult(readFixture(t, "codex.jsonl", ParseCodexLine))

	assert.Equal(t, 5200, res.InputTokens)
	assert.Equal(t, 4096, res.CachedInputTokens)
	assert.Equal(t, 310, res.OutputTokens)
	assert.Zero(t, res.CacheReadTokens)
	assert.Zero(t, res.CacheCreationTokens)
	assert.Equal(t, "Fixed and verified.", res.LastText)
	assert.False(t, res.Failed)
}
