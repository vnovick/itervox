package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

func TestParseCodexLine_ThreadStarted(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"thread.started","thread_id":"abc-123"}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventSystem, ev.Type)
	assert.Equal(t, "abc-123", ev.SessionID)
}

func TestParseCodexLine_AgentMessage(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"hello"}}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventAssistant, ev.Type)
	assert.Equal(t, []string{"hello"}, ev.TextBlocks)
	assert.Equal(t, "hello", ev.Message)
}

func TestParseCodexLine_CommandExecution(t *testing.T) {
	line, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"id":                "i1",
			"type":              "command_execution",
			"command":           "echo hi",
			"aggregated_output": "hi\n",
			"exit_code":         0,
			"status":            "completed",
		},
	})
	require.NoError(t, err)

	ev, err := agent.ParseCodexLine(line)
	require.NoError(t, err)
	assert.Equal(t, agent.EventAssistant, ev.Type)
	require.Len(t, ev.ToolCalls, 1)
	assert.Equal(t, "shell", ev.ToolCalls[0].Name)

	var input map[string]any
	require.NoError(t, json.Unmarshal(ev.ToolCalls[0].Input, &input))
	assert.Equal(t, "echo hi", input["command"])
	assert.Equal(t, "hi\n", input["output"])
}

func TestParseCodexLine_CollabToolCall(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"item.completed","item":{"id":"i2","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"thr-1","receiver_thread_ids":["thr-2"],"prompt":"Investigate the bug","agents_states":{"thr-2":{"status":"pending_init","message":null}},"status":"completed"}}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventAssistant, ev.Type)
	require.Len(t, ev.ToolCalls, 1)
	assert.Equal(t, "spawn_agent", ev.ToolCalls[0].Name)

	var input map[string]any
	require.NoError(t, json.Unmarshal(ev.ToolCalls[0].Input, &input))
	assert.Equal(t, "spawn_agent", input["tool"])
	assert.Equal(t, "Investigate the bug", input["prompt"])
}

func TestParseCodexLine_TurnCompleted(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":20}}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventResult, ev.Type)
	assert.Equal(t, 100, ev.Usage.InputTokens)
	assert.Equal(t, 20, ev.Usage.OutputTokens)
}

func TestParseCodexLine_IgnoredEvents(t *testing.T) {
	// turn.started has no useful payload — it should still be skipped.
	for _, line := range []string{
		`{"type":"turn.started"}`,
	} {
		_, err := agent.ParseCodexLine([]byte(line))
		assert.Error(t, err, "expected skip error for %q", line)
	}
	// item.started with a known type (command_execution) is now handled.
	line := `{"type":"item.started","item":{"id":"i0","type":"command_execution","status":"in_progress"}}`
	ev, err := agent.ParseCodexLine([]byte(line))
	assert.NoError(t, err, "item.started/command_execution should now parse successfully")
	assert.True(t, ev.InProgress)
}

func TestCodexRunnerFreshTurn(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewCodexRunner()
	result, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hello", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "tid-1", result.SessionID)
	assert.Equal(t, "done", result.LastText)
	assert.Equal(t, 10, result.InputTokens)
	assert.Equal(t, 5, result.OutputTokens)
	assert.False(t, result.Failed)
}

func TestCodexRunnerResumeTurn(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"resumed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":8}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	argFile := filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %s\nprintf '%%s' %s\n", shellLiteral(argFile), shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewCodexRunner()
	sessionID := "tid-1"
	result, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		&sessionID, "continue", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "resumed", result.LastText)
	assert.Equal(t, 20, result.InputTokens)
	assert.Equal(t, 8, result.OutputTokens)

	argsData, err := os.ReadFile(argFile)
	require.NoError(t, err)
	assert.Contains(t, string(argsData), "-C")
	assert.Contains(t, string(argsData), "resume")
	assert.Contains(t, string(argsData), "tid-1")
	assert.Contains(t, string(argsData), "continue")
}

func TestMultiRunnerDispatchesToCodex(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"mtr-1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"codex here"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeCodex := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeCodex, []byte(script), 0o755))

	multi := agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{
		"codex": agent.NewCodexRunner(),
	})
	result, err := multi.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, fakeCodex, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "mtr-1", result.SessionID)
	assert.Equal(t, "codex here", result.LastText)
	assert.Equal(t, 5, result.InputTokens)
	assert.Equal(t, 2, result.OutputTokens)
}

func TestCodexRunnerLogsSubagentEvents(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sub-1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"sub-1","receiver_thread_ids":["sub-2"],"prompt":"Investigate the failing test","agents_states":{"sub-2":{"status":"pending_init","message":null}},"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	log := &captureLogger{}
	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), log, nil,
		nil, "hi", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(log.info, "\n"), "codex: subagent")
}

func TestMultiRunnerDispatchesToHintedBackend(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"hint-1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"hinted"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeWrapper := filepath.Join(dir, "run_codex_wrapper")
	argFile := filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %s\nprintf '%%s' %s\n", shellLiteral(argFile), shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeWrapper, []byte(script), 0o755))

	multi := agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{
		"codex": agent.NewCodexRunner(),
	})
	command := agent.CommandWithBackendHint(fakeWrapper, "codex")
	result, err := multi.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, command, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "hint-1", result.SessionID)

	argsData, err := os.ReadFile(argFile)
	require.NoError(t, err)
	assert.NotContains(t, string(argsData), "@@itervox-backend=")
	assert.Contains(t, string(argsData), "exec")
}

func shellLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

type captureLogger struct {
	info []string
}

func (l *captureLogger) Info(msg string, _ ...any) {
	l.info = append(l.info, msg)
}

func (l *captureLogger) Debug(_ string, _ ...any) {}

func (l *captureLogger) Warn(_ string, _ ...any) {}

// captureLoggerFull records full log lines including key=value args for detail assertions.
type captureLoggerFull struct {
	info []string
	warn []string
}

func (l *captureLoggerFull) Info(msg string, args ...any) {
	l.info = append(l.info, formatLogLine(msg, args))
}

func (l *captureLoggerFull) Debug(_ string, _ ...any) {}

func (l *captureLoggerFull) Warn(msg string, args ...any) {
	l.warn = append(l.warn, formatLogLine(msg, args))
}

func formatLogLine(msg string, args []any) string {
	if len(args) == 0 {
		return msg
	}
	parts := []string{msg}
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", args[i], args[i+1]))
	}
	return strings.Join(parts, " ")
}

func TestParseCodexLine_TurnFailed(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"turn.failed","error":{"message":"API rate limit exceeded","type":"rate_limit_error","code":"rate_limit"}}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventResult, ev.Type)
	assert.True(t, ev.IsError)
	assert.Equal(t, "API rate limit exceeded", ev.ResultText)
}

func TestParseCodexLine_TurnFailedWithUsage(t *testing.T) {
	ev, err := agent.ParseCodexLine([]byte(`{"type":"turn.failed","error":{"message":"context length exceeded"},"usage":{"input_tokens":5000,"output_tokens":100}}`))
	require.NoError(t, err)
	assert.Equal(t, agent.EventResult, ev.Type)
	assert.True(t, ev.IsError)
	assert.Equal(t, "context length exceeded", ev.ResultText)
	assert.Equal(t, 5000, ev.Usage.InputTokens)
	assert.Equal(t, 100, ev.Usage.OutputTokens)
}

func TestMultiRunnerStripsBackendHintFromPrompt(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"strip-1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"cleaned"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeCodex := filepath.Join(dir, "codex")
	argFile := filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\ncat > %s\necho \"$@\" >> %s\nprintf '%%s' %s", shellLiteral(argFile), shellLiteral(argFile), shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeCodex, []byte(script), 0o755))

	multi := agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{
		"codex": agent.NewCodexRunner(),
	})
	result, err := multi.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "@@itervox-backend=codex actual prompt text", dir, fakeCodex, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "strip-1", result.SessionID)

	callData, err := os.ReadFile(argFile)
	require.NoError(t, err)
	callStr := string(callData)
	assert.NotContains(t, callStr, "@@itervox-backend=codex actual prompt text")
}

func TestCodexRunnerStartupFailure(t *testing.T) {
	runner := agent.NewCodexRunner()
	result, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "test", t.TempDir(), "/nonexistent/path/to/codex", "",
		"",
		30000, 60000,
	)
	require.Error(t, err)
	assert.True(t, result.Failed)
	assert.Contains(t, err.Error(), "codex:")
}

func TestCodexRunnerWithNonExeCommand(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"shell-1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeWrapper := filepath.Join(dir, "wrapper.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeWrapper, []byte(script), 0o755))

	runner := agent.NewCodexRunner()
	result, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "test", dir, fakeWrapper, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "shell-1", result.SessionID)
}

func TestParseCodexLine_UnexpectedEventType(t *testing.T) {
	// Top-level unknown event types are still skipped with "codex: skip event type".
	for _, line := range []string{
		`{"type":"turn.started"}`,
		`{"type":"unknown.event","data":"something"}`,
	} {
		_, err := agent.ParseCodexLine([]byte(line))
		assert.Error(t, err, "expected skip error for %q", line)
		assert.Contains(t, err.Error(), "codex: skip event type")
	}
	// item.started with an unknown item type is skipped with a different message.
	line := `{"type":"item.started","item":{"id":"i0","type":"completely_unknown_item","status":"in_progress"}}`
	_, err := agent.ParseCodexLine([]byte(line))
	assert.Error(t, err, "item.started with unknown item type should be skipped")
	assert.Contains(t, err.Error(), "codex: skip item.started type")
}

func TestParseCodexLine_TurnFailedWithApprovalRequired(t *testing.T) {
	for _, tc := range []struct {
		name    string
		msg     string
		wantReq bool
	}{
		{"approval required", "Approval required for this action", true},
		{"waiting for input", "Waiting for input from user", true},
		{"pending approval", "Pending approval from user", true},
		{"requires approval", "This action requires approval", true},
		{"interactive mode", "Interactive mode needed", true},
		{"user input", "Waiting for user input", true},
		{"confirmation required", "Confirmation required to proceed", true},
		{"regular error", "API rate limit exceeded", false},
		{"context exceeded", "Context length exceeded", false},
	} {
		line := fmt.Sprintf(`{"type":"turn.failed","error":{"message":"%s"},"usage":{"input_tokens":100,"output_tokens":50}}`, tc.msg)
		ev, err := agent.ParseCodexLine([]byte(line))
		require.NoError(t, err, tc.name)
		assert.True(t, ev.IsError, tc.name)
		assert.Equal(t, tc.wantReq, ev.IsInputRequired, tc.name)
	}
}

// ---------------------------------------------------------------------------
// item.started — InProgress flag and action_started log emission
// ---------------------------------------------------------------------------

func TestParseCodexLine_ItemStarted_CommandExecution(t *testing.T) {
	line, err := json.Marshal(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"id":      "i1",
			"type":    "command_execution",
			"command": "make build",
			"status":  "in_progress",
		},
	})
	require.NoError(t, err)

	ev, err := agent.ParseCodexLine(line)
	require.NoError(t, err)
	assert.Equal(t, agent.EventAssistant, ev.Type)
	assert.True(t, ev.InProgress, "item.started should set InProgress=true")
	require.Len(t, ev.ToolCalls, 1)
	assert.Equal(t, "shell", ev.ToolCalls[0].Name)

	var input map[string]any
	require.NoError(t, json.Unmarshal(ev.ToolCalls[0].Input, &input))
	assert.Equal(t, "make build", input["command"])
	assert.Equal(t, "in_progress", input["status"])
}

func TestParseCodexLine_ItemStarted_CollabToolCall(t *testing.T) {
	line, err := json.Marshal(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"id":                  "i2",
			"type":                "collab_tool_call",
			"tool":                "spawn_agent",
			"sender_thread_id":    "thr-1",
			"receiver_thread_ids": []string{"thr-2"},
			"status":              "in_progress",
		},
	})
	require.NoError(t, err)

	ev, err := agent.ParseCodexLine(line)
	require.NoError(t, err)
	assert.True(t, ev.InProgress)
	require.Len(t, ev.ToolCalls, 1)
	assert.Equal(t, "spawn_agent", ev.ToolCalls[0].Name)
}

func TestParseCodexLine_ItemStarted_UnknownTypeIsSkipped(t *testing.T) {
	line, err := json.Marshal(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"id":   "i3",
			"type": "unknown_item_type",
		},
	})
	require.NoError(t, err)

	_, err = agent.ParseCodexLine(line)
	assert.Error(t, err, "unknown item.started type should be skipped")
}

func TestParseCodexLine_InProgressFalseForCompletedEvents(t *testing.T) {
	// Completed events should not have InProgress set.
	line, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"id":      "i1",
			"type":    "command_execution",
			"command": "ls",
			"status":  "completed",
		},
	})
	require.NoError(t, err)

	ev, err := agent.ParseCodexLine(line)
	require.NoError(t, err)
	assert.False(t, ev.InProgress, "item.completed should NOT set InProgress")
}

func TestCodexRunnerLogsActionStarted(t *testing.T) {
	// item.started events should produce action_started log lines.
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-start"}`,
		`{"type":"item.started","item":{"id":"i0","type":"command_execution","command":"make build","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"make build","aggregated_output":"ok","exit_code":0,"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	log := &captureLogger{}
	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), log, nil,
		nil, "build it", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	allInfo := strings.Join(log.info, "\n")
	assert.Contains(t, allInfo, "codex: action_started", "in-progress action_started must be logged")
	assert.Contains(t, allInfo, "codex: action", "completed action must be logged")
}

// ---------------------------------------------------------------------------
// Shell payload: exit_code in description, action_detail log
// ---------------------------------------------------------------------------

func TestCodexShellNonZeroExitInDescription(t *testing.T) {
	// toolDescription("shell") should include exit code when non-zero.
	// The description is logged as a kwarg, so use captureLoggerFull to verify.
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-exit"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"bad_cmd","aggregated_output":"not found","exit_code":127,"status":"failed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	log := &captureLoggerFull{}
	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), log, nil,
		nil, "run bad cmd", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	allInfo := strings.Join(log.info, "\n")
	assert.Contains(t, allInfo, "exit:127", "non-zero exit code should appear in action log description")
}

func TestCodexShellZeroExitNoExitSuffix(t *testing.T) {
	// Zero exit should NOT add "(exit:0)" noise to the description.
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-ok"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"echo ok","aggregated_output":"ok","exit_code":0,"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	log := &captureLogger{}
	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), log, nil,
		nil, "echo ok", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	allInfo := strings.Join(log.info, "\n")
	assert.NotContains(t, allInfo, "exit:0", "zero exit should not appear in action log")
}

func TestCodexShellDetailLoggedAtInfoLevel(t *testing.T) {
	// action_detail should be emitted at INFO level so it enters the log buffer.
	fakeOutput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"tid-detail"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"make","aggregated_output":"build output here","exit_code":0,"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	log := &captureLoggerFull{}
	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), log, nil,
		nil, "build", dir, fakeExe, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	allInfo := strings.Join(log.info, "\n")
	assert.Contains(t, allInfo, "codex: action_detail", "action_detail must be logged at INFO level")
	assert.Contains(t, allInfo, "tool=shell", "action_detail must include tool=shell so parseLogLine can populate IssueLogEntry.Tool")
	assert.Contains(t, allInfo, "status=completed", "action_detail should include status")
	assert.Contains(t, allInfo, "output_size=", "action_detail should include output_size")
}

func TestMultiRunnerWarnsOnUnsupportedBackend(t *testing.T) {
	// Claude-format output (not Codex format)
	fakeOutput := strings.Join([]string{
		`{"type":"system","session_id":"warn-1"}`,
		`{"type":"result","subtype":"success","session_id":"warn-1"}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeClaude := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n", shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeClaude, []byte(script), 0o755))

	multi := agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{
		"codex": agent.NewCodexRunner(),
	})
	result, err := multi.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, "@@itervox-backend=unsupported "+fakeClaude, "",
		"",
		30000, 60000,
	)
	require.NoError(t, err)
	assert.Equal(t, "warn-1", result.SessionID)
}
