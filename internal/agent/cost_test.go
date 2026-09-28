package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// TestParseClaudeResultCost (CORE-091): Claude's stream-json `result` event
// carries a top-level `total_cost_usd` (Claude Code 2.1.283 bundle:
// `type:"result",subtype:…,session_id:r,total_cost_usd:0,usage:…`) — a
// client-side, session-cumulative estimate. It is parsed onto the event and
// carried to the TurnResult; absent means unknown (nil), never 0.
func TestParseClaudeResultCost(t *testing.T) {
	ev, err := agent.ParseLine([]byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"done","total_cost_usd":0.0421,"usage":{"input_tokens":10,"output_tokens":5}}`))
	require.NoError(t, err)
	require.NotNil(t, ev.CostUSD)
	assert.InDelta(t, 0.0421, *ev.CostUSD, 1e-12)

	// An error result carries it too (error_during_execution ships total_cost_usd:0).
	ev, err = agent.ParseLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s1","total_cost_usd":0}`))
	require.NoError(t, err)
	require.NotNil(t, ev.CostUSD)
	assert.Zero(t, *ev.CostUSD)

	// Absent → unknown; a string value parses; other events never carry a cost.
	ev, err = agent.ParseLine([]byte(`{"type":"result","subtype":"success","session_id":"s1","result":"ok"}`))
	require.NoError(t, err)
	assert.Nil(t, ev.CostUSD)
	ev, err = agent.ParseLine([]byte(`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":"1.5"}`))
	require.NoError(t, err)
	require.NotNil(t, ev.CostUSD)
	assert.InDelta(t, 1.5, *ev.CostUSD, 1e-12)
	ev, err = agent.ParseLine([]byte(`{"type":"assistant","session_id":"s1","total_cost_usd":9,"message":{"content":[]}}`))
	require.NoError(t, err)
	assert.Nil(t, ev.CostUSD)

	// ApplyEvent carries the result's cost to the TurnResult.
	r := agent.ApplyEvent(agent.TurnResult{}, agent.StreamEvent{Type: agent.EventResult, SessionID: "s1", CostUSD: ptr(0.25)})
	require.NotNil(t, r.CostUSD)
	assert.InDelta(t, 0.25, *r.CostUSD, 1e-12)

	// Codex events have no cost field.
	cev, err := agent.ParseCodexLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}`))
	require.NoError(t, err)
	assert.Nil(t, cev.CostUSD)
}

func ptr(f float64) *float64 { return &f }
