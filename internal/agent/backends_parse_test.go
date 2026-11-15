package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readFixture runs a synthetic stream fixture (testdata/backends, see its
// README) through readLines with parse, the way a runner reads stdout.
func readFixture(t *testing.T, name string, parse func([]byte) (StreamEvent, error)) TurnResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "backends", name))
	require.NoError(t, err)
	res, err := readLines(context.Background(), slog.Default(), nil, strings.NewReader(string(data)), 5000, "fixture", parse)
	require.NoError(t, err)
	return res
}

// TestOpenCodeFixtureMapsOntoTurnResult: the OpenCode JSON stream maps onto
// TurnResult — session id, text blocks, tool calls (with their targets),
// tokens summed over the steps (reasoning counted as output, cache reads as
// cached input) and the summed step cost.
func TestOpenCodeFixtureMapsOntoTurnResult(t *testing.T) {
	stream := &openCodeStream{}
	res := FinalizeResult(readFixture(t, "opencode.jsonl", stream.parse))

	assert.Equal(t, "ses_fixture01", res.SessionID)
	assert.Equal(t, []string{"I'll read the README first.", "Done: the tests now pass."}, res.AllTextBlocks)
	assert.Equal(t, "Done: the tests now pass.", res.LastText)
	assert.Equal(t, 2, res.ToolCalls)
	assert.Equal(t, "bash", res.LastToolName)
	assert.Equal(t, "go test ./...", res.LastToolTarget)
	assert.Equal(t, 2700, res.InputTokens)
	assert.Equal(t, 140, res.OutputTokens)
	assert.Equal(t, 1500, res.CachedInputTokens)
	assert.False(t, res.Failed)
	assert.Nil(t, res.CostUSD, "cost is read by the stream, not ApplyEvent")
	assert.True(t, stream.costKnown)
	assert.InDelta(t, 0.02, stream.cost, 1e-9)
	assert.Equal(t, "ses_fixture01", stream.sessionID)
}

// TestOpenCodeToolTargetReadsFilePath: OpenCode's read/edit/write tools
// name their file filePath; the run context shows it.
func TestOpenCodeToolTargetReadsFilePath(t *testing.T) {
	ev, err := ParseOpenCodeLine([]byte(`{"type":"tool_use","sessionID":"s","part":{"tool":"read","state":{"status":"completed","input":{"filePath":"/ws/a.go"},"output":"secret body"}}}`))
	require.NoError(t, err)
	require.Len(t, ev.ToolCalls, 1)
	assert.Equal(t, "/ws/a.go", ToolTarget(ev.ToolCalls[0].Name, ev.ToolCalls[0].Input))
}

// TestOpenCodeErrorLines: an error line fails the turn with its message,
// a usage-limit message is a quota limit, a 429 a throttle, and the v1
// {name, data{message}} shape is read too.
func TestOpenCodeErrorLines(t *testing.T) {
	cases := []struct {
		name, line, text string
		kind             LimitKind
		status           int
	}{
		{"plain", `{"type":"error","sessionID":"s","error":{"type":"unknown","message":"model not found"}}`, "model not found", "", 0},
		{"quota", `{"type":"error","sessionID":"s","error":{"name":"APIError","data":{"message":"You've hit your usage limit. Try again later.","statusCode":429}}}`,
			"You've hit your usage limit. Try again later.", LimitKindQuota, 429},
		{"throttle_by_status", `{"type":"error","sessionID":"s","error":{"name":"APIError","data":{"message":"slow down","statusCode":429}}}`,
			"slow down", LimitKindThrottle, 429},
		{"no_message", `{"type":"error","sessionID":"s"}`, "opencode reported an error", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseOpenCodeLine([]byte(tc.line))
			require.NoError(t, err)
			assert.Equal(t, EventResult, ev.Type)
			assert.True(t, ev.IsError)
			assert.Equal(t, tc.text, ev.ResultText)
			if tc.kind == "" {
				assert.Nil(t, ev.Limit)
				return
			}
			require.NotNil(t, ev.Limit)
			assert.Equal(t, tc.kind, ev.Limit.Kind)
			assert.Equal(t, tc.status, ev.Limit.HTTPStatus)
		})
	}
	res := ApplyEvent(TurnResult{}, mustParse(t, ParseOpenCodeLine, cases[1].line))
	assert.True(t, res.Failed)
	assert.True(t, res.LastLimit.Terminal(), "a quota error must reach the turn's LastLimit")
}

// TestOpenCodeSkipsUnknownLines: reasoning and unknown types are skipped.
func TestOpenCodeSkipsUnknownLines(t *testing.T) {
	for _, line := range []string{`{"type":"reasoning","sessionID":"s","part":{"text":"hmm"}}`, `{"type":"future_thing"}`, `not json`} {
		_, err := ParseOpenCodeLine([]byte(line))
		assert.Error(t, err, line)
	}
}

// TestCursorFixtureMapsOntoTurnResult: the Cursor stream-json maps onto
// TurnResult through the Claude parser plus tool_call lines.
func TestCursorFixtureMapsOntoTurnResult(t *testing.T) {
	res := FinalizeResult(readFixture(t, "cursor.jsonl", ParseCursorLine))

	assert.Equal(t, "c6b62c6f-0000-4000-8000-00000000f1x7", res.SessionID)
	assert.Equal(t, []string{"I'll read the test file.", "The test passes now."}, res.AllTextBlocks)
	assert.Equal(t, 2, res.ToolCalls, "only completed tool calls count")
	assert.Equal(t, "shell", res.LastToolName)
	assert.Equal(t, "go test ./...", res.LastToolTarget)
	assert.Equal(t, "I'll read the test file.The test passes now.", res.ResultText)
	assert.Equal(t, 900, res.InputTokens)
	assert.Equal(t, 60, res.OutputTokens)
	assert.Equal(t, 100, res.CachedInputTokens)
	assert.False(t, res.Failed)
	assert.Nil(t, res.CostUSD)
}

// TestCursorToolCallLines: started is in progress, completed counts, the
// path argument is the target, an error result marks the status, and an
// unmapped kind keeps its name.
func TestCursorToolCallLines(t *testing.T) {
	started := mustParse(t, ParseCursorLine, `{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"editToolCall":{"args":{"path":"a.go"}}},"session_id":"s"}`)
	assert.True(t, started.InProgress)
	assert.Equal(t, "edit", started.ToolCalls[0].Name)
	assert.Equal(t, "a.go", ToolTarget("edit", started.ToolCalls[0].Input))

	failed := mustParse(t, ParseCursorLine, `{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"editToolCall":{"args":{"path":"a.go"},"result":{"error":{"message":"denied"}}}},"session_id":"s"}`)
	assert.False(t, failed.InProgress)
	assert.Contains(t, string(failed.ToolCalls[0].Input), `"status":"error"`)

	other := mustParse(t, ParseCursorLine, `{"type":"tool_call","subtype":"completed","tool_call":{"mcpToolCall":{"args":{"x":"y"}}}}`)
	assert.Equal(t, "mcp", other.ToolCalls[0].Name)

	_, err := ParseCursorLine([]byte(`{"type":"tool_call","subtype":"started","tool_call":{}}`))
	assert.Error(t, err)
}

// TestCursorErrorResult: an error result fails the turn and a usage-limit
// text is a quota limit (ParseLine's text fallback).
func TestCursorErrorResult(t *testing.T) {
	ev := mustParse(t, ParseCursorLine, `{"type":"result","subtype":"error","is_error":true,"result":"You've hit your usage limit for this month.","session_id":"s"}`)
	res := ApplyEvent(TurnResult{}, ev)
	assert.True(t, res.Failed)
	assert.Equal(t, "You've hit your usage limit for this month.", res.FailureText)
	require.NotNil(t, res.LastLimit)
	assert.True(t, res.LastLimit.Terminal())
}

// TestCustomFixtureMapsOntoTurnResult: the custom protocol maps onto
// TurnResult: session, text, tool call/result, additive usage and cost,
// done.
func TestCustomFixtureMapsOntoTurnResult(t *testing.T) {
	stream := &customStream{}
	res := FinalizeResult(readFixture(t, "custom.jsonl", stream.parse))

	assert.Equal(t, "custom-sess-1", res.SessionID)
	assert.Equal(t, []string{"Looking at the issue.", "All green."}, res.AllTextBlocks)
	assert.Equal(t, 1, res.ToolCalls, "a tool_call starts, its tool_result completes")
	assert.Equal(t, "bash", res.LastToolName)
	assert.Equal(t, "make test", res.LastToolTarget)
	assert.Equal(t, 1000, res.InputTokens)
	assert.Equal(t, 50, res.OutputTokens)
	assert.Equal(t, 50, res.CachedInputTokens)
	assert.Equal(t, "Fixed the flaky test.", res.ResultText)
	assert.False(t, res.Failed)
	assert.False(t, stream.inputRequired)
	assert.InDelta(t, 0.006, stream.cost, 1e-9)
}

// TestCustomProtocolLines: input_required, rate_limit (explicit kind,
// inferred kind, reset and retry-after) and failed lines.
func TestCustomProtocolLines(t *testing.T) {
	stream := &customStream{}
	q, err := stream.parse([]byte(`{"type":"input_required","question":"Which database should I use?"}`))
	require.NoError(t, err)
	assert.True(t, stream.inputRequired)
	assert.Equal(t, []string{"Which database should I use?"}, q.TextBlocks)

	rl := mustParse(t, ParseCustomLine, `{"type":"rate_limit","kind":"quota","message":"monthly cap","resets_at":"2030-01-02T03:04:05Z","retry_after_seconds":"90"}`)
	require.NotNil(t, rl.Limit)
	assert.Equal(t, LimitKindQuota, rl.Limit.Kind)
	assert.Equal(t, "2030-01-02T03:04:05Z", rl.Limit.ResetsAt.UTC().Format("2006-01-02T15:04:05Z"))
	assert.Equal(t, "1m30s", rl.Limit.RetryAfter.String())

	inferred := mustParse(t, ParseCustomLine, `{"type":"rate_limit","message":"429 Too Many Requests"}`)
	assert.Equal(t, LimitKindThrottle, inferred.Limit.Kind)

	explicit := mustParse(t, ParseCustomLine, `{"type":"rate_limit","kind":"throttle","message":"You've hit your usage limit"}`)
	assert.Equal(t, LimitKindThrottle, explicit.Limit.Kind, "an explicit kind wins over the message")

	unnamed := mustParse(t, ParseCustomLine, `{"type":"rate_limit","message":"stop for now"}`)
	assert.Equal(t, LimitKindQuota, unnamed.Limit.Kind, "a limit the message does not name is a quota")

	failed := mustParse(t, ParseCustomLine, `{"type":"failed","reason":"compiler crashed"}`)
	res := ApplyEvent(TurnResult{}, failed)
	assert.True(t, res.Failed)
	assert.Equal(t, "compiler crashed", res.FailureText)

	empty := mustParse(t, ParseCustomLine, `{"type":"failed"}`)
	assert.Equal(t, "custom agent reported failure", empty.ResultText)

	_, err = ParseCustomLine([]byte(`{"type":"heartbeat"}`))
	assert.Error(t, err, "unknown types are skipped")
}

// TestSessionCostLedgerIsCumulative: per-turn costs become the session's
// running total (CORE-091's cumulative semantics); no session returns the
// turn's own cost.
func TestSessionCostLedgerIsCumulative(t *testing.T) {
	var l sessionCostLedger
	assert.InDelta(t, 0.5, l.add("a", 0.5), 1e-9)
	assert.InDelta(t, 0.75, l.add("a", 0.25), 1e-9)
	assert.InDelta(t, 0.1, l.add("b", 0.1), 1e-9)
	assert.InDelta(t, 0.3, l.add("", 0.3), 1e-9)
	for i := range maxTrackedSessionCosts {
		l.add(strings.Repeat("x", i+1), 1)
	}
	assert.InDelta(t, 0.25, l.add("a", 0.25), 1e-9, "an evicted session restarts its total")
	assert.LessOrEqual(t, len(l.total), maxTrackedSessionCosts)
}

func mustParse(t *testing.T, parse func([]byte) (StreamEvent, error), line string) StreamEvent {
	t.Helper()
	ev, err := parse([]byte(line))
	require.NoError(t, err)
	return ev
}
