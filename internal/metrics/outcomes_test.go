package metrics

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunTokensAndPROutcomesFamilies (cost and outcome visibility): a
// finished run's tokens are counted per kind (every kind exported, a kind
// the backend did not report at 0), and PR outcome transitions per
// outcome; both families pass the strict parser.
func TestRunTokensAndPROutcomesFamilies(t *testing.T) {
	resetForTest()
	RunTokens(100, 40, 900, 0)
	RunTokens(10, 5, 0, 0)
	PROutcome("merged")
	PROutcome("merged")
	PROutcome("reverted")

	var b strings.Builder
	require.NoError(t, WriteText(&b, sampleView()))
	fams := parseExposition(t, b.String())
	tok := fams["itervox_agent_tokens_total"].samples
	assert.Equal(t, 110.0, tok[`{kind="input"}`])
	assert.Equal(t, 45.0, tok[`{kind="output"}`])
	assert.Equal(t, 900.0, tok[`{kind="cache_read"}`])
	assert.Contains(t, tok, `{kind="cache_write"}`)
	assert.Equal(t, 0.0, tok[`{kind="cache_write"}`])
	assert.Equal(t, uint64(900), RunTokensCount(TokensCacheRead))
	pr := fams["itervox_pr_outcomes_total"].samples
	assert.Equal(t, 2.0, pr[`{outcome="merged"}`])
	assert.Equal(t, 1.0, pr[`{outcome="reverted"}`])
	assert.Equal(t, uint64(2), PROutcomeCount("merged"))
}
