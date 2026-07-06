package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

func TestParseLineSystemEvent(t *testing.T) {
	line := []byte(`{"type":"system","session_id":"sess-abc"}`)
	ev, err := agent.ParseLine(line)
	require.NoError(t, err)
	assert.Equal(t, "system", ev.Type)
	assert.Equal(t, "sess-abc", ev.SessionID)
}

func TestParseLineAssistantEvent(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]},"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	ev, err := agent.ParseLine(line)
	require.NoError(t, err)
	assert.Equal(t, "assistant", ev.Type)
	assert.Equal(t, 10, ev.Usage.InputTokens)
	assert.Equal(t, 5, ev.Usage.OutputTokens)
}

func TestParseLineResultSuccess(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","session_id":"sess-abc"}`)
	ev, err := agent.ParseLine(line)
	require.NoError(t, err)
	assert.Equal(t, "result", ev.Type)
	assert.Equal(t, "sess-abc", ev.SessionID)
	assert.False(t, ev.IsError)
	assert.False(t, ev.IsInputRequired)
}

func TestParseLineResultError(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"error","session_id":"sess-abc"}`)
	ev, err := agent.ParseLine(line)
	require.NoError(t, err)
	assert.True(t, ev.IsError)
	assert.False(t, ev.IsInputRequired)
}

func TestParseLineResultInputRequired(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"error","is_error":true,"session_id":"sess-abc","result":"Human turn required"}`)
	ev, err := agent.ParseLine(line)
	require.NoError(t, err)
	assert.True(t, ev.IsError)
}

func TestIsSentinelInputRequired(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"no sentinel", "just some output with a question?", false},
		{"sentinel alone", agent.InputRequiredSentinel, true},
		{"sentinel mid stream", "before\n" + agent.InputRequiredSentinel + "\nafter", true},
		{"sentinel with trailing whitespace", agent.InputRequiredSentinel + "   \n", true},
		{"sentinel embedded in a sentence does not match", "No sentinel emitted: " + agent.InputRequiredSentinel + " is intentionally absent.", false},
		{"heuristic-only phrase does not match sentinel detector", "Questions for you:", false},
		// T-15: sentinels inside markdown code fences must not trigger.
		{"sentinel inside backtick fence does not match",
			"Example:\n```\n" + agent.InputRequiredSentinel + "\n```\nend", false},
		{"sentinel inside tilde fence does not match",
			"Example:\n~~~\n" + agent.InputRequiredSentinel + "\n~~~\nend", false},
		{"sentinel inside language-tagged fence does not match",
			"Example:\n```md\n" + agent.InputRequiredSentinel + "\n```\nend", false},
		{"sentinel after a closed backtick fence still triggers",
			"```\nignored " + agent.InputRequiredSentinel + "\n```\n" + agent.InputRequiredSentinel, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agent.IsSentinelInputRequired(tc.text), tc.name)
		})
	}
}

func TestParseLineNonJSONReturnsError(t *testing.T) {
	line := []byte(`not json at all`)
	_, err := agent.ParseLine(line)
	assert.Error(t, err)
}

func TestParseLineEmptyLineReturnsError(t *testing.T) {
	_, err := agent.ParseLine([]byte(``))
	assert.Error(t, err)
}
