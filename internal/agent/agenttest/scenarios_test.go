package agenttest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vnovick/itervox/internal/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuccessRunner_EmitsResult(t *testing.T) {
	// CORE-146: res.Failed is a bool zero-value, so asserting it's false
	// proved nothing — an empty, never-touched TurnResult also satisfies it.
	// Assert the actual emitted content: the session ID stamped by both the
	// scripted "system" and "result" events.
	r := SuccessRunner("s1")
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0, agent.PermissionBypass)
	require.NoError(t, err)
	assert.False(t, res.Failed)
	assert.Equal(t, "s1", res.SessionID, "SuccessRunner must stamp the given sessionID onto the emitted result")
	assert.Equal(t, 1, r.CallCount)
}

func TestFailRunner_RecordsFailureAtomically(t *testing.T) {
	r := FailRunner("disk full")
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0, agent.PermissionBypass)
			require.NoError(t, err)
			assert.True(t, res.Failed)
			assert.Contains(t, res.FailureText, "disk full")
		})
	}
	wg.Wait()
	assert.Equal(t, int64(10), r.CallCount())
}

func TestRateLimitedFailRunner_ClassifiedAsRateLimit(t *testing.T) {
	r := RateLimitedFailRunner()
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0, agent.PermissionBypass)
	require.NoError(t, err)
	require.True(t, res.Failed)
	// The orchestrator's IsRateLimitFailure classifier matches on
	// "rate_limit_exceeded" or "429" — both present in the failure text.
	assert.True(t,
		strings.Contains(strings.ToLower(res.FailureText), "rate_limit_exceeded") ||
			strings.Contains(res.FailureText, "429"),
		"failure text must trip the rate-limit classifier",
	)
}

func TestInputRequiredRunner_FlagsAreSet(t *testing.T) {
	// CORE-138/141/142: the double must emit what the real parsers emit for a
	// vendor input request — an assistant message carrying the agent's
	// question, then an error result event flagged IsInputRequired (Claude
	// result is_error / Codex turn.failed) — so ApplyEvent sets InputRequired
	// exactly as it does for a real stream.
	const question = "Should I rebase before merge?"
	r := InputRequiredRunner("s1", question)
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0, agent.PermissionBypass)
	require.NoError(t, err)
	assert.True(t, res.InputRequired, "the scenario's whole point: InputRequired must be set")
	assert.True(t, res.Failed, "a vendor input request arrives on an error result event, as in the real parsers")
	assert.Equal(t, question, res.LastText, "the agent-written question is the last assistant text")
	assert.Equal(t, []string{question}, res.AllTextBlocks)
	assert.Equal(t, "s1", res.SessionID)
	assert.Positive(t, res.InputTokens+res.OutputTokens, "a real input-request turn produced tokens")
	assert.Equal(t, 1, r.CallCount)
}
