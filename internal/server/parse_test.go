// package server (whitebox) — tests for the unexported log-line parsing functions.
package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLogLine serialises a bufLogEntry to the JSON string that parseLogLine expects.
func makeLogLine(e bufLogEntry) string {
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// skipEntry — operates on parsed BufLogEntry fields
// ---------------------------------------------------------------------------

func TestSkipEntry_SessionStarted(t *testing.T) {
	for _, prefix := range []string{"claude", "codex"} {
		assert.True(t, skipEntry(bufLogEntry{Level: "INFO", Msg: prefix + ": session started"}), prefix)
	}
}

func TestSkipEntry_TurnDone(t *testing.T) {
	for _, prefix := range []string{"claude", "codex"} {
		assert.True(t, skipEntry(bufLogEntry{Level: "INFO", Msg: prefix + ": turn done"}), prefix)
	}
}

func TestSkipEntry_DebugLines(t *testing.T) {
	assert.True(t, skipEntry(bufLogEntry{Level: "DEBUG", Msg: "claude: tool_input"}))
	assert.True(t, skipEntry(bufLogEntry{Level: "DEBUG", Msg: "codex: tool_input"}))
}

func TestSkipEntry_PassesThroughNormalLines(t *testing.T) {
	normal := []bufLogEntry{
		{Level: "INFO", Msg: "claude: text"},
		{Level: "INFO", Msg: "claude: action"},
		{Level: "INFO", Msg: "codex: action"},
		{Level: "INFO", Msg: "codex: subagent"},
		{Level: "WARN", Msg: "something went wrong"},
		{Level: "ERROR", Msg: "fatal error"},
	}
	for _, e := range normal {
		assert.False(t, skipEntry(e), "should not skip: %q %q", e.Level, e.Msg)
	}
}

// ---------------------------------------------------------------------------
// parseLogLine — JSON input
// ---------------------------------------------------------------------------

// v0.2.0 todolist5 B5 — the orchestrator's automation dispatch path writes a
// 4-line block prefixed `AUTOMATION FIRED · <id>` as a single multi-line msg.
// parseLogLine must tag it as `"automation"` so the dashboard chip filter
// can render it; previously these entries fell through to the Level-driven
// default which mapped INFO to `"info"` — a sentinel the frontend filters out.
func TestParseLogLine_AutomationFiredPrefix(t *testing.T) {
	msg := "AUTOMATION FIRED · clarify-blocked-issues\n  trigger: input_required\n  context: needs answer\n  profile: input-responder"
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: msg, Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "automation", entry.Event)
	assert.Contains(t, entry.Message, "AUTOMATION FIRED")
	assert.Contains(t, entry.Message, "clarify-blocked-issues")
}

// Negative guard: a generic INFO line that does not start with the
// AUTOMATION FIRED prefix must still land in the `"info"` fallthrough so the
// new chip doesn't over-fire on unrelated entries.
func TestParseLogLine_GenericInfoNotAutomation(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "orchestrator: tick complete", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "info", entry.Event)
}

func TestParseLogLine_NonJSONSkipped(t *testing.T) {
	_, skip := parseLogLine(`INFO claude: text session_id=s1 text="hello world"`)
	assert.True(t, skip, "legacy text-format lines must be skipped")
}

func TestParseLogLine_ClaudeText(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: text", Text: "hello world", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "text", entry.Event)
	assert.Equal(t, "hello world", entry.Message)
}

func TestParseLogLine_CodexText(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: text", Text: "codex says hi", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "text", entry.Event)
	assert.Equal(t, "codex says hi", entry.Message)
}

func TestParseLogLine_ClaudeAction(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: action", Tool: "Bash", Description: "ls -la", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Equal(t, "Bash", entry.Tool)
	assert.Contains(t, entry.Message, "Bash")
	assert.Contains(t, entry.Message, "ls -la")
}

func TestParseLogLine_CodexAction(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: action", Tool: "shell", Description: "echo hi", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Equal(t, "shell", entry.Tool)
	assert.Contains(t, entry.Message, "shell")
}

func TestParseLogLine_ClaudeSubagent(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: subagent", Tool: "Task", Description: "Investigate bug", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "subagent", entry.Event)
	assert.Equal(t, "Task", entry.Tool)
	assert.Equal(t, "Investigate bug", entry.Message)
}

func TestParseLogLine_CodexSubagent(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: subagent", Tool: "spawn_agent", Description: "Fix test", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "subagent", entry.Event)
	assert.Equal(t, "spawn_agent", entry.Tool)
	assert.Equal(t, "Fix test", entry.Message)
}

// ---------------------------------------------------------------------------
// parseLogLine — action_started
// ---------------------------------------------------------------------------

func TestParseLogLine_CodexActionStarted(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: action_started", Tool: "shell", Description: "long build", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Equal(t, "shell", entry.Tool)
	assert.Contains(t, entry.Message, "shell")
	assert.Contains(t, entry.Message, "…", "in-progress action should end with ellipsis")
}

func TestParseLogLine_ClaudeActionStarted(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: action_started", Tool: "Bash", Description: "pnpm install", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Contains(t, entry.Message, "…")
}

// ---------------------------------------------------------------------------
// parseLogLine — action_detail with Detail field
// ---------------------------------------------------------------------------

func TestParseLogLine_CodexActionDetailPopulatesDetailField(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: action_detail", Tool: "shell", Status: "completed", ExitCode: "0", OutputSize: "42", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Equal(t, "shell", entry.Tool)
	require.NotEmpty(t, entry.Detail, "Detail field should be populated for action_detail lines")

	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Detail), &detail))
	assert.Equal(t, "completed", detail["status"])
	assert.Equal(t, float64(42), detail["output_size"])
}

func TestParseLogLine_ClaudeActionDetailHandled(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: action_detail", Tool: "shell", Status: "completed", ExitCode: "0", OutputSize: "10", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "action", entry.Event)
	assert.Equal(t, "shell", entry.Tool)
	require.NotEmpty(t, entry.Detail)
}

func TestParseLogLine_CodexActionDetailNonZeroExitCode(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: action_detail", Tool: "shell", Status: "failed", ExitCode: "127", OutputSize: "10", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Contains(t, entry.Message, "exit:127", "failed shell should show exit code in message")

	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Detail), &detail))
	assert.Equal(t, float64(127), detail["exit_code"])
	assert.Equal(t, "failed", detail["status"])
}

// ---------------------------------------------------------------------------
// buildDetailJSON
// ---------------------------------------------------------------------------

func TestBuildDetailJSON_AllFields(t *testing.T) {
	result := buildDetailJSON("completed", "0", "100")
	require.NotEmpty(t, result)

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &m))
	assert.Equal(t, "completed", m["status"])
	assert.Equal(t, float64(0), m["exit_code"])
	assert.Equal(t, float64(100), m["output_size"])
}

func TestBuildDetailJSON_EmptyFieldsOmitted(t *testing.T) {
	result := buildDetailJSON("", "", "")
	assert.Empty(t, result, "all-empty fields should produce empty detail")
}

func TestBuildDetailJSON_PartialFields(t *testing.T) {
	result := buildDetailJSON("completed", "", "")
	require.NotEmpty(t, result)
	assert.Contains(t, result, "completed")
	assert.NotContains(t, result, "exit_code")
	assert.NotContains(t, result, "output_size")
}

// ---------------------------------------------------------------------------
// IssueLogEntry JSON serialization — Detail is omitempty
// ---------------------------------------------------------------------------

func TestIssueLogEntryDetailOmitEmpty(t *testing.T) {
	entry := IssueLogEntry{Level: "INFO", Event: "action", Message: "Bash — ls"}
	b, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "detail", "empty Detail should be omitted from JSON")
}

func TestIssueLogEntryDetailIncludedWhenSet(t *testing.T) {
	entry := IssueLogEntry{
		Level:   "INFO",
		Event:   "action",
		Message: "shell completed",
		Tool:    "shell",
		Detail:  `{"status":"completed","exit_code":0}`,
	}
	b, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"detail"`)
	assert.Contains(t, string(b), "completed")
}

// ---------------------------------------------------------------------------
// Both backends produce same event types (regression guard)
// ---------------------------------------------------------------------------

func TestBothBackendsProduceEqualEventShapes(t *testing.T) {
	pairs := []struct{ claude, codex string }{
		{
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: text", Text: "hello"}),
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: text", Text: "hello"}),
		},
		{
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: action", Tool: "Bash", Description: "ls"}),
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: action", Tool: "shell", Description: "ls"}),
		},
		{
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: subagent", Tool: "Task", Description: "sub"}),
			makeLogLine(bufLogEntry{Level: "INFO", Msg: "codex: subagent", Tool: "spawn_agent", Description: "sub"}),
		},
	}

	for _, p := range pairs {
		claudeEntry, claudeSkip := parseLogLine(p.claude)
		codexEntry, codexSkip := parseLogLine(p.codex)

		assert.Equal(t, claudeSkip, codexSkip, "skip parity")
		assert.Equal(t, claudeEntry.Level, codexEntry.Level, "level parity")
		assert.Equal(t, claudeEntry.Event, codexEntry.Event, "event type parity")
	}
}

// ---------------------------------------------------------------------------
// Time extraction
// ---------------------------------------------------------------------------

func TestParseLogLine_TimeExtracted(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "INFO", Msg: "claude: text", Text: "hello", Time: "12:34:56"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "12:34:56", entry.Time)
}

// ---------------------------------------------------------------------------
// Warn / Error lines
// ---------------------------------------------------------------------------

func TestParseLogLine_WarnLine(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "WARN", Msg: "rate limit hit, retrying", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "WARN", entry.Level)
	assert.Equal(t, "warn", entry.Event)
	assert.True(t, strings.Contains(entry.Message, "rate limit"))
}

func TestParseLogLine_ErrorLine(t *testing.T) {
	line := makeLogLine(bufLogEntry{Level: "ERROR", Msg: "agent subprocess failed", Time: "12:00:00"})
	entry, skip := parseLogLine(line)
	require.False(t, skip)
	assert.Equal(t, "ERROR", entry.Level)
	assert.Equal(t, "error", entry.Event)
}
