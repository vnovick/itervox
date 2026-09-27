package agent_test

// CORE-002 — the shared stdout reader (readLines) must survive stream-json
// lines larger than the old 1 MiB bufio.Scanner cap. Design (claude.go,
// binding acceptance as amended 2026-09-25):
//   - a line of at most maxNonTerminalLineBytes (1 MiB) is always decoded in
//     full, with no byte-level type detection (the baseline behaviour);
//   - only a line over 1 MiB has its top-level "type" detected, within the
//     first streamDetectBudgetBytes (64 KiB);
//   - terminal type (Claude "result"; Codex "turn.completed"/"turn.failed"):
//     the line is decoded in full, up to the 16 MiB hard cap, over which the
//     turn fails closed ("agent: stream line exceeds 16 MiB cap");
//   - any other line over 1 MiB (non-terminal type, or "type" not within the
//     detection budget) is skipped without buffering past 1 MiB and counted
//     in TurnResult.OversizeLines.
// Every RunTurn row drives the real runner of BOTH parsers (ClaudeRunner →
// ParseLine, CodexRunner → ParseCodexLine) against a fake agent that `cat`s
// a payload pre-generated in Go. The peak-allocation rows additionally drive
// readLines directly (agent.ReadLinesForTest) over a synthetic reader that
// never materialises the 2 MB line, and bound runtime.MemStats.TotalAlloc —
// cumulative bytes allocated, an upper bound on the peak — so a reader that
// buffers the whole line (≥ 2 MiB) fails the assertion.
//
// Spec note: the acceptance's key-order dimension ({type first} vs {large key
// first, type last}) cannot be taken literally for a 2 MB TERMINAL line —
// "type" last after a 2 MB key is exactly the fourth row (type beyond the
// detection budget → skipped). The terminal "large key first" rows therefore
// put a large key (48 KiB, inside the budget) before "type" and the 2 MB
// payload after it; the non-terminal rows exercise both readings.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// oversizedLineBytes is well past the pre-CORE-002 1 MiB Scanner cap and
// well under the 16 MiB hard cap.
const oversizedLineBytes = 2 << 20

// overCapLineBytes is just over readLines' documented 16 MiB hard cap.
const overCapLineBytes = 16<<20 + 1024

// largeKeyBytes is a key value large enough to push "type" deep into the
// line while staying inside the 64 KiB detection budget.
const largeKeyBytes = 48 << 10

func bigString(n int) string { return strings.Repeat("a", n) }

// runFakeStream writes lines (joined by "\n", trailing "\n") to a payload
// file, points a fake agent at it, and runs one turn.
func runFakeStream(t *testing.T, runner agent.Runner, lines []string) (agent.TurnResult, time.Duration, error) {
	t.Helper()
	dir := t.TempDir()
	payload := filepath.Join(dir, "stream.jsonl")
	require.NoError(t, os.WriteFile(payload, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	exe := filepath.Join(dir, "fake-agent")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\ncat "+shellLiteral(payload)+"\n"), 0o755))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	res, err := runner.RunTurn(ctx, slog.Default(), nil, nil, "hello", dir, exe, "", "", 30000, 60000, agent.PermissionBypass)
	return res, time.Since(start), err
}

type streamRunner struct {
	name  string
	new   func() agent.Runner
	parse func([]byte) (agent.StreamEvent, error)
}

var streamRunners = []streamRunner{
	{"claude", func() agent.Runner { return agent.NewClaudeRunner() }, agent.ParseLine},
	{"codex", func() agent.Runner { return agent.NewCodexRunner() }, agent.ParseCodexLine},
}

// repeatReader yields n copies of b without materialising them.
type repeatReader struct {
	n int
	b byte
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	k := min(len(p), r.n)
	for i := range p[:k] {
		p[i] = r.b
	}
	r.n -= k
	return k, nil
}

// measureReadLinesAlloc runs readLines over prefix + n×'a' + suffix and
// returns the minimum TotalAlloc delta over three attempts (unrelated
// allocations from other goroutines can only ADD to a sample, so the minimum
// is the tightest honest measurement) plus the last result.
func measureReadLinesAlloc(t *testing.T, parse func([]byte) (agent.StreamEvent, error), prefix string, n int, suffix string) (uint64, agent.TurnResult) {
	t.Helper()
	var best uint64
	var res agent.TurnResult
	for attempt := range 3 {
		r := io.MultiReader(strings.NewReader(prefix), &repeatReader{n: n, b: 'a'}, strings.NewReader(suffix))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := agent.ReadLinesForTest(r, parse)
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		res = got
		if d := after.TotalAlloc - before.TotalAlloc; attempt == 0 || d < best {
			best = d
		}
	}
	return best, res
}

// oversizeRow is one parser's fixture set for TestRunTurnSurvivesOversizedLine.
type oversizeRow struct {
	runner streamRunner
	// nonTerminalOpen/Close wrap a big string into a non-terminal event with
	// "type" first; typeLastOpen/Close wrap it with "type" as the LAST key.
	nonTerminalOpen, nonTerminalClose string
	typeLastOpen, typeLastClose       string
	// afterKeyOpen/Close: a 48 KiB key first, then "type", then the big string.
	afterKeyOpen, afterKeyClose string
	tail                        []string // valid lines ending the turn
	check                       func(t *testing.T, r agent.TurnResult)
}

var oversizeRows = []oversizeRow{
	{
		runner:           streamRunners[0],
		nonTerminalOpen:  `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"`,
		nonTerminalClose: `"}]}}`,
		typeLastOpen:     " \t {\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"content\":\"",
		typeLastClose:    "\"}]} , \"type\" : \"user\"}",
		afterKeyOpen:     " \t {\"meta\":\"" + bigString(largeKeyBytes) + "\", \"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"content\":\"",
		afterKeyClose:    "\"}]}}",
		tail:             []string{`{"type":"result","subtype":"success","is_error":false,"result":"ok-after-big","session_id":"s1"}`},
		check:            func(t *testing.T, r agent.TurnResult) { assert.Equal(t, "ok-after-big", r.ResultText) },
	},
	{
		runner:           streamRunners[1],
		nonTerminalOpen:  `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"`,
		nonTerminalClose: `"}}`,
		typeLastOpen:     "  {\"item\":{\"id\":\"i1\",\"type\":\"agent_message\",\"text\":\"",
		typeLastClose:    "\"}, \"type\":\"item.completed\"}",
		afterKeyOpen:     "  {\"meta\":\"" + bigString(largeKeyBytes) + "\",\"type\":\"item.completed\",\"item\":{\"id\":\"i1\",\"type\":\"agent_message\",\"text\":\"",
		afterKeyClose:    "\"}}",
		tail: []string{
			`{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"after-big"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`,
		},
		check: func(t *testing.T, r agent.TurnResult) {
			assert.Equal(t, "after-big", r.LastText)
			assert.Equal(t, 10, r.InputTokens)
		},
	},
}

func TestRunTurnSurvivesOversizedLine(t *testing.T) {
	big := bigString(oversizedLineBytes)
	for _, row := range oversizeRows {
		shapes := []struct {
			name        string
			open, close string
		}{
			{"type_first", row.nonTerminalOpen, row.nonTerminalClose},
			{"large_key_first_type_after_leading_whitespace", row.afterKeyOpen, row.afterKeyClose},
			{"large_key_first_type_last_leading_whitespace", row.typeLastOpen, row.typeLastClose},
		}
		for _, shape := range shapes {
			t.Run(row.runner.name+"/"+shape.name, func(t *testing.T) {
				lines := append([]string{shape.open + big + shape.close}, row.tail...)
				res, _, err := runFakeStream(t, row.runner.new(), lines)
				require.NoError(t, err, "an oversized non-terminal line must not end the turn")
				assert.False(t, res.Failed, "turn must succeed; FailureText=%q", truncateForMsg(res.FailureText))
				assert.Equal(t, 1, res.OversizeLines, "the skipped oversized line is counted exactly once")
				row.check(t, res)
			})
		}

		// Third row: a 2 MB non-terminal line (type found, not terminal) is
		// skipped and counted, and peak allocation stays under the documented
		// non-terminal cap (maxNonTerminalLineBytes = 1 MiB) plus reader
		// overhead — far below the 16 MiB hard cap and below the line itself.
		t.Run(row.runner.name+"/nonterminal_peak_alloc_under_cap", func(t *testing.T) {
			suffix := row.nonTerminalClose + "\n" + strings.Join(row.tail, "\n") + "\n"
			alloc, res := measureReadLinesAlloc(t, row.runner.parse, row.nonTerminalOpen, oversizedLineBytes, suffix)
			bound := uint64(agent.MaxNonTerminalLineBytes + agent.StreamReaderOverheadBytes)
			t.Logf("peak-allocation bound (TotalAlloc delta): %d bytes; measured %d bytes (line %d bytes)", bound, alloc, oversizedLineBytes)
			assert.Less(t, alloc, bound, "reader must not buffer a non-terminal line past its cap")
			assert.Equal(t, 1, res.OversizeLines)
			assert.False(t, res.Failed)
			row.check(t, res)
		})

		// Fourth row: "type" beyond the detection budget → skipped, counted.
		// Bound (M0-close re-check G6): the amended acceptance requires every
		// line of at most 1 MiB to be decoded in full, so the reader cannot
		// know a line is over 1 MiB — and only then consult the detection
		// budget — without first holding its first 1 MiB. The pre-amendment
		// bound (detection budget + reader overhead, 320 KiB) is therefore
		// unattainable by construction; the attainable, asserted bound is
		// the non-terminal cap + reader overhead, still well under the 2 MiB
		// line itself, so a reader that buffers the whole line still fails.
		t.Run(row.runner.name+"/type_beyond_detection_budget_peak_alloc", func(t *testing.T) {
			suffix := row.typeLastClose + "\n" + strings.Join(row.tail, "\n") + "\n"
			alloc, res := measureReadLinesAlloc(t, row.runner.parse, row.typeLastOpen, oversizedLineBytes, suffix)
			bound := uint64(agent.MaxNonTerminalLineBytes + agent.StreamReaderOverheadBytes)
			t.Logf("peak-allocation bound (TotalAlloc delta): %d bytes; measured %d bytes (line %d bytes)", bound, alloc, oversizedLineBytes)
			assert.Less(t, alloc, bound, "reader must not buffer an unknown-type line past the 1 MiB decode cap")
			assert.Equal(t, 1, res.OversizeLines)
			assert.False(t, res.Failed)
			row.check(t, res)
		})
	}
}

func TestRunTurnParsesOversizedResultLine(t *testing.T) {
	big := bigString(oversizedLineBytes)
	key := bigString(largeKeyBytes)
	claudeOK := func(t *testing.T, r agent.TurnResult, err error) {
		require.NoError(t, err)
		assert.False(t, r.Failed)
		assert.Len(t, r.ResultText, oversizedLineBytes)
	}
	codexCompleted := func(t *testing.T, r agent.TurnResult, err error) {
		require.NoError(t, err)
		assert.False(t, r.Failed)
		assert.Equal(t, 7, r.InputTokens)
		assert.Equal(t, 3, r.OutputTokens)
	}
	codexFailed := func(t *testing.T, r agent.TurnResult, err error) {
		require.NoError(t, err, "a decoded turn.failed is a failed turn, not a read error")
		assert.True(t, r.Failed)
		assert.Contains(t, r.FailureText, "quota exhausted")
	}
	rows := []struct {
		name   string
		runner func() agent.Runner
		line   string
		check  func(t *testing.T, r agent.TurnResult, err error)
	}{
		{"claude/result_type_first", streamRunners[0].new,
			`{"type":"result","subtype":"success","is_error":false,"result":"` + big + `","session_id":"s1"}`, claudeOK},
		{"claude/result_large_key_first_leading_whitespace", streamRunners[0].new,
			"\t  {\"meta\":\"" + key + "\",  \"type\" : \"result\",\"result\":\"" + big + "\",\"is_error\":false,\"session_id\":\"s1\"}", claudeOK},
		{"codex/turn.completed_type_first", streamRunners[1].new,
			`{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3},"padding":"` + big + `"}`, codexCompleted},
		{"codex/turn.completed_large_key_first_leading_whitespace", streamRunners[1].new,
			"   {\"meta\":\"" + key + "\",\"type\":\"turn.completed\",\"padding\":\"" + big + "\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}", codexCompleted},
		{"codex/turn.failed_type_first", streamRunners[1].new,
			`{"type":"turn.failed","error":{"message":"quota exhausted ` + big + `"}}`, codexFailed},
		{"codex/turn.failed_large_key_first_leading_whitespace", streamRunners[1].new,
			" \t{\"meta\":\"" + key + "\" , \"type\":\"turn.failed\",\"error\":{\"message\":\"quota exhausted " + big + "\"}}", codexFailed},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			res, _, err := runFakeStream(t, row.runner(), []string{row.line})
			row.check(t, res, err)
			assert.Equal(t, 0, res.OversizeLines, "a terminal line under the hard cap is decoded, not skipped")
		})
	}
}

func TestRunTurnFailsClosedOnOverCapLine(t *testing.T) {
	pad := bigString(overCapLineBytes)
	rows := []struct {
		name   string
		runner func() agent.Runner
		lines  []string
	}{
		// A terminal-shaped over-cap line followed by a perfectly valid
		// terminal line: the reader must NOT skip past the over-cap line and
		// report the later result — it fails closed.
		{"claude", streamRunners[0].new, []string{
			`{"type":"result","is_error":false,"result":"` + pad + `"}`,
			`{"type":"result","subtype":"success","is_error":false,"result":"must-not-be-reached"}`,
		}},
		{"codex", streamRunners[1].new, []string{
			`{"type":"turn.completed","padding":"` + pad + `","usage":{"input_tokens":1,"output_tokens":1}}`,
			`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			res, elapsed, err := runFakeStream(t, row.runner(), row.lines)
			require.Error(t, err, "a line over the 16 MiB cap is a read error")
			assert.Contains(t, err.Error(), "agent: stream line exceeds 16 MiB cap")
			assert.True(t, res.Failed)
			assert.Contains(t, res.FailureText, "stream line exceeds 16 MiB cap")
			assert.NotContains(t, res.ResultText, "must-not-be-reached")
			assert.Less(t, elapsed, 20*time.Second, "fail-closed is immediate, not an idle timeout")
		})
	}
}

func truncateForMsg(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// M0-close re-check G6/N3 (CORE-002 amendment of 2026-09-25): the detection
// budget applies ONLY to lines longer than 1 MiB. A line of at most 1 MiB is
// always fully JSON-decoded, as the pre-CORE-002 reader did, so a terminal
// event whose "type" key is JSON-escaped, or whose "type" comes after 64 KiB
// of other keys, still ends the turn. Codex's re-check counterexample is the
// first row, verbatim in shape: {"type":"result",...,"result":"<128 KiB>"}
// (backslash-u escape of 'p' in the key; built with jsonEsc below so the
// source cannot be normalised into a plain "type").
func TestRunTurnDecodesSubMiBTerminalLineWithoutTypeDetection(t *testing.T) {
	const payloadBytes = 128 << 10
	body := bigString(payloadBytes)
	const jsonEsc = "\\u" // a JSON \uXXXX escape introducer
	claudeOK := func(t *testing.T, r agent.TurnResult, err error) {
		require.NoError(t, err)
		assert.False(t, r.Failed, "FailureText=%q", truncateForMsg(r.FailureText))
		assert.Len(t, r.ResultText, payloadBytes, "the terminal result must be decoded, not skipped")
	}
	codexCompleted := func(t *testing.T, r agent.TurnResult, err error) {
		require.NoError(t, err)
		assert.False(t, r.Failed)
		assert.Equal(t, 7, r.InputTokens, "turn.completed must be decoded, not skipped")
	}
	rows := []struct {
		name   string
		runner func() agent.Runner
		line   string
		check  func(t *testing.T, r agent.TurnResult, err error)
	}{
		{"claude/escaped_type_key", streamRunners[0].new,
			`{"ty` + jsonEsc + `0070e":"result","is_error":false,"result":"` + body + `","session_id":"s1"}`, claudeOK},
		{"claude/escaped_type_value", streamRunners[0].new,
			`{"type":"r` + jsonEsc + `0065sult","is_error":false,"result":"` + body + `","session_id":"s1"}`, claudeOK},
		{"claude/type_last_key", streamRunners[0].new,
			`{"is_error":false,"result":"` + body + `","session_id":"s1","type":"result"}`, claudeOK},
		{"codex/turn.completed_type_last_key", streamRunners[1].new,
			`{"padding":"` + body + `","usage":{"input_tokens":7,"output_tokens":3},"type":"turn.completed"}`, codexCompleted},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			res, _, err := runFakeStream(t, row.runner(), []string{row.line})
			row.check(t, res, err)
			assert.Equal(t, 0, res.OversizeLines, "a line of at most 1 MiB is never skipped")
		})
	}
}
