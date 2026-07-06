package agent

// White-box fixture-based tests for ParseLine, ParseCodexLine, ApplyEvent,
// toolDescription, and shellSemanticDesc. These complement the existing
// black-box tests in events_test.go, codex_test.go, and runner_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestParseLine — fixture-based with realistic Claude SSE JSON lines
// ---------------------------------------------------------------------------

func TestParseLine(t *testing.T) {
	tests := []struct {
		name            string
		line            string
		wantType        string
		wantSessionID   string
		wantMessage     string
		wantTextBlocks  []string
		wantToolCalls   int
		wantToolName    string
		wantIsError     bool
		wantInputReq    bool
		wantResultText  string
		wantInputTokens int
		wantOutputToks  int
		wantErr         bool
	}{
		{
			name:          "system event with session_id",
			line:          `{"type":"system","session_id":"sess-xyz-123"}`,
			wantType:      "system",
			wantSessionID: "sess-xyz-123",
		},
		{
			name:            "assistant with single text block",
			line:            `{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix this bug."}]},"usage":{"input_tokens":150,"output_tokens":30}}`,
			wantType:        "assistant",
			wantMessage:     "I'll fix this bug.",
			wantTextBlocks:  []string{"I'll fix this bug."},
			wantInputTokens: 150,
			wantOutputToks:  30,
		},
		{
			name:           "assistant with multiple text blocks",
			line:           `{"type":"assistant","message":{"content":[{"type":"text","text":"First thought."},{"type":"text","text":"Second thought."}]}}`,
			wantType:       "assistant",
			wantMessage:    "First thought.",
			wantTextBlocks: []string{"First thought.", "Second thought."},
		},
		{
			name:          "assistant with tool_use call",
			line:          `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
			wantType:      "assistant",
			wantToolCalls: 1,
			wantToolName:  "Bash",
		},
		{
			name:           "assistant with text and tool_use mixed",
			line:           `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check."},{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}`,
			wantType:       "assistant",
			wantMessage:    "Let me check.",
			wantTextBlocks: []string{"Let me check."},
			wantToolCalls:  1,
			wantToolName:   "Read",
		},
		{
			name:            "assistant with usage inside message object",
			line:            `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":200,"cached_input_tokens":50,"output_tokens":75}}}`,
			wantType:        "assistant",
			wantMessage:     "hi",
			wantTextBlocks:  []string{"hi"},
			wantInputTokens: 200,
			wantOutputToks:  75,
		},
		{
			name:           "result success",
			line:           `{"type":"result","subtype":"success","session_id":"sess-ok","result":"Task completed."}`,
			wantType:       "result",
			wantSessionID:  "sess-ok",
			wantIsError:    false,
			wantResultText: "Task completed.",
		},
		{
			name:           "result error (subtype)",
			line:           `{"type":"result","subtype":"error","session_id":"sess-err","result":"Something failed"}`,
			wantType:       "result",
			wantSessionID:  "sess-err",
			wantIsError:    true,
			wantResultText: "Something failed",
		},
		{
			name:           "result error (is_error flag)",
			line:           `{"type":"result","is_error":true,"session_id":"sess-err2","result":"Bad request"}`,
			wantType:       "result",
			wantIsError:    true,
			wantResultText: "Bad request",
		},
		{
			name:           "result error with input required (human turn)",
			line:           `{"type":"result","subtype":"error","is_error":true,"result":"Human turn required"}`,
			wantType:       "result",
			wantIsError:    true,
			wantInputReq:   true,
			wantResultText: "Human turn required",
		},
		{
			name:           "result error with input required (approval)",
			line:           `{"type":"result","subtype":"error","is_error":true,"result":"Pending approval needed"}`,
			wantType:       "result",
			wantIsError:    true,
			wantInputReq:   true,
			wantResultText: "Pending approval needed",
		},
		{
			name:    "malformed JSON returns error",
			line:    `{not valid json at all`,
			wantErr: true,
		},
		{
			name:    "empty line returns error",
			line:    ``,
			wantErr: true,
		},
		{
			name:    "whitespace-only line returns error",
			line:    `   `,
			wantErr: true,
		},
		{
			name:           "assistant with empty text block is skipped",
			line:           `{"type":"assistant","message":{"content":[{"type":"text","text":""}]}}`,
			wantType:       "assistant",
			wantTextBlocks: nil,
		},
		{
			name:          "unknown type still parses without error",
			line:          `{"type":"unknown_event","session_id":"s1"}`,
			wantType:      "unknown_event",
			wantSessionID: "s1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseLine([]byte(tc.line))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, ev.Type)
			if tc.wantSessionID != "" {
				assert.Equal(t, tc.wantSessionID, ev.SessionID)
			}
			if tc.wantMessage != "" {
				assert.Equal(t, tc.wantMessage, ev.Message)
			}
			if tc.wantTextBlocks != nil {
				assert.Equal(t, tc.wantTextBlocks, ev.TextBlocks)
			} else if tc.wantType == "assistant" && tc.wantMessage == "" && tc.wantToolCalls == 0 {
				// When we explicitly expect nil text blocks
				assert.Nil(t, ev.TextBlocks)
			}
			if tc.wantToolCalls > 0 {
				assert.Len(t, ev.ToolCalls, tc.wantToolCalls)
				if tc.wantToolName != "" {
					assert.Equal(t, tc.wantToolName, ev.ToolCalls[0].Name)
				}
			}
			assert.Equal(t, tc.wantIsError, ev.IsError)
			assert.Equal(t, tc.wantInputReq, ev.IsInputRequired)
			if tc.wantResultText != "" {
				assert.Equal(t, tc.wantResultText, ev.ResultText)
			}
			if tc.wantInputTokens > 0 {
				assert.Equal(t, tc.wantInputTokens, ev.Usage.InputTokens)
			}
			if tc.wantOutputToks > 0 {
				assert.Equal(t, tc.wantOutputToks, ev.Usage.OutputTokens)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestParseCodexLine — fixture-based for Codex JSON events
// ---------------------------------------------------------------------------

func TestParseCodexLineFixtures(t *testing.T) {
	tests := []struct {
		name           string
		line           string
		wantType       string
		wantSessionID  string
		wantMessage    string
		wantToolCalls  int
		wantToolName   string
		wantIsError    bool
		wantInputReq   bool
		wantInProgress bool
		wantErr        bool
	}{
		{
			name:          "thread.started",
			line:          `{"type":"thread.started","thread_id":"thr-456"}`,
			wantType:      EventSystem,
			wantSessionID: "thr-456",
		},
		{
			name:        "item.completed agent_message",
			line:        `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"Analysis complete."}}`,
			wantType:    EventAssistant,
			wantMessage: "Analysis complete.",
		},
		{
			name:          "item.completed command_execution",
			line:          `{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"go test ./...","aggregated_output":"PASS","exit_code":0,"status":"completed"}}`,
			wantType:      EventAssistant,
			wantToolCalls: 1,
			wantToolName:  "shell",
		},
		{
			name:           "item.started command_execution sets InProgress",
			line:           `{"type":"item.started","item":{"id":"i3","type":"command_execution","command":"make build","status":"in_progress"}}`,
			wantType:       EventAssistant,
			wantInProgress: true,
			wantToolCalls:  1,
			wantToolName:   "shell",
		},
		{
			name:           "item.started collab_tool_call",
			line:           `{"type":"item.started","item":{"id":"i4","type":"collab_tool_call","tool":"wait","sender_thread_id":"t1","receiver_thread_ids":["t2"],"status":"in_progress"}}`,
			wantType:       EventAssistant,
			wantInProgress: true,
			wantToolCalls:  1,
			wantToolName:   "wait",
		},
		{
			name:          "item.completed collab_tool_call with tool name",
			line:          `{"type":"item.completed","item":{"id":"i5","type":"collab_tool_call","tool":"send_input","sender_thread_id":"t1","prompt":"continue"}}`,
			wantType:      EventAssistant,
			wantToolCalls: 1,
			wantToolName:  "send_input",
		},
		{
			name:          "item.completed collab_tool_call without tool name falls back to type",
			line:          `{"type":"item.completed","item":{"id":"i6","type":"collab_tool_call","tool":""}}`,
			wantType:      EventAssistant,
			wantToolCalls: 1,
			wantToolName:  "collab_tool_call",
		},
		{
			name:     "turn.completed with usage",
			line:     `{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":200}}`,
			wantType: EventResult,
		},
		{
			name:        "turn.failed with error",
			line:        `{"type":"turn.failed","error":{"message":"rate limit hit","type":"rate_limit","code":"429"}}`,
			wantType:    EventResult,
			wantIsError: true,
		},
		{
			name:         "turn.failed with input required (approval)",
			line:         `{"type":"turn.failed","error":{"message":"Requires approval from admin"}}`,
			wantType:     EventResult,
			wantIsError:  true,
			wantInputReq: true,
		},
		{
			name:    "empty line returns error",
			line:    ``,
			wantErr: true,
		},
		{
			name:    "invalid JSON returns error",
			line:    `{broken`,
			wantErr: true,
		},
		{
			name:    "item.started with nil item returns error",
			line:    `{"type":"item.started"}`,
			wantErr: true,
		},
		{
			name:    "item.completed with nil item returns error",
			line:    `{"type":"item.completed"}`,
			wantErr: true,
		},
		{
			name:    "item.completed with unknown item type returns error",
			line:    `{"type":"item.completed","item":{"id":"i7","type":"unknown_thing"}}`,
			wantErr: true,
		},
		{
			name:    "unknown top-level event type is skipped",
			line:    `{"type":"some.other.event"}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseCodexLine([]byte(tc.line))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, ev.Type)
			if tc.wantSessionID != "" {
				assert.Equal(t, tc.wantSessionID, ev.SessionID)
			}
			if tc.wantMessage != "" {
				assert.Equal(t, tc.wantMessage, ev.Message)
			}
			if tc.wantToolCalls > 0 {
				require.Len(t, ev.ToolCalls, tc.wantToolCalls)
				assert.Equal(t, tc.wantToolName, ev.ToolCalls[0].Name)
			}
			assert.Equal(t, tc.wantIsError, ev.IsError)
			assert.Equal(t, tc.wantInputReq, ev.IsInputRequired)
			assert.Equal(t, tc.wantInProgress, ev.InProgress)
		})
	}
}

func TestParseCodexLineTurnCompletedUsage(t *testing.T) {
	line := []byte(`{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":200}}`)
	ev, err := ParseCodexLine(line)
	require.NoError(t, err)
	assert.Equal(t, 500, ev.Usage.InputTokens)
	assert.Equal(t, 100, ev.Usage.CachedInputTokens)
	assert.Equal(t, 200, ev.Usage.OutputTokens)
}

func TestParseCodexLineTurnFailedUsage(t *testing.T) {
	line := []byte(`{"type":"turn.failed","error":{"message":"context too long"},"usage":{"input_tokens":8000,"output_tokens":400}}`)
	ev, err := ParseCodexLine(line)
	require.NoError(t, err)
	assert.Equal(t, 8000, ev.Usage.InputTokens)
	assert.Equal(t, 400, ev.Usage.OutputTokens)
	assert.Equal(t, "context too long", ev.ResultText)
}

func TestParseCodexLineTurnCompletedNoUsage(t *testing.T) {
	ev, err := ParseCodexLine([]byte(`{"type":"turn.completed"}`))
	require.NoError(t, err)
	assert.Equal(t, EventResult, ev.Type)
	assert.Equal(t, 0, ev.Usage.InputTokens)
}

func TestParseCodexLineTurnFailedNoError(t *testing.T) {
	ev, err := ParseCodexLine([]byte(`{"type":"turn.failed"}`))
	require.NoError(t, err)
	assert.Equal(t, EventResult, ev.Type)
	assert.True(t, ev.IsError)
	assert.Equal(t, "", ev.ResultText)
}

// ---------------------------------------------------------------------------
// TestApplyEventSequence — test that applying system, assistant, result events
// accumulates tokens, text, and session ID correctly.
// ---------------------------------------------------------------------------

func TestApplyEventSequence(t *testing.T) {
	events := []StreamEvent{
		{Type: EventSystem, SessionID: "sess-integration"},
		{
			Type:       EventAssistant,
			TextBlocks: []string{"Analyzing the codebase..."},
			Usage:      UsageSnapshot{InputTokens: 200, CachedInputTokens: 50, OutputTokens: 80},
		},
		{
			Type:      EventAssistant,
			ToolCalls: []ToolCall{{Name: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}},
			Usage:     UsageSnapshot{InputTokens: 300, OutputTokens: 40},
		},
		{
			Type:       EventAssistant,
			TextBlocks: []string{"Tests pass.", "Moving on to implementation."},
			Usage:      UsageSnapshot{InputTokens: 100, OutputTokens: 60},
		},
		{
			Type:       EventResult,
			ResultText: "Implementation complete",
			Usage:      UsageSnapshot{InputTokens: 50, OutputTokens: 10},
		},
	}

	var r TurnResult
	for _, ev := range events {
		r = ApplyEvent(r, ev)
	}

	assert.Equal(t, "sess-integration", r.SessionID)
	assert.Equal(t, 650, r.InputTokens)      // 200+300+100+50
	assert.Equal(t, 50, r.CachedInputTokens) // only first assistant has cached
	assert.Equal(t, 190, r.OutputTokens)     // 80+40+60+10
	assert.Equal(t, 840, r.TotalTokens)      // 650+190
	assert.Equal(t, "Moving on to implementation.", r.LastText)
	assert.Equal(t, []string{"Analyzing the codebase...", "Tests pass.", "Moving on to implementation."}, r.AllTextBlocks)
	assert.Equal(t, "Implementation complete", r.ResultText)
	assert.False(t, r.Failed)
	assert.False(t, r.InputRequired)
}

func TestApplyEventInProgressSkipsAccumulation(t *testing.T) {
	r := TurnResult{SessionID: "sess-1"}
	r = ApplyEvent(r, StreamEvent{
		Type:       EventAssistant,
		InProgress: true,
		TextBlocks: []string{"should be ignored"},
		Usage:      UsageSnapshot{InputTokens: 999, OutputTokens: 999},
	})
	assert.Equal(t, 0, r.InputTokens)
	assert.Equal(t, 0, r.OutputTokens)
	assert.Nil(t, r.AllTextBlocks)
	assert.Equal(t, "", r.LastText)
}

func TestApplyEventResultOverwritesSessionID(t *testing.T) {
	r := TurnResult{SessionID: "old-sess"}
	r = ApplyEvent(r, StreamEvent{
		Type:      EventResult,
		SessionID: "new-sess",
	})
	assert.Equal(t, "new-sess", r.SessionID)
}

func TestApplyEventResultErrorSetsFailedAndText(t *testing.T) {
	var r TurnResult
	r = ApplyEvent(r, StreamEvent{
		Type:            EventResult,
		IsError:         true,
		IsInputRequired: true,
		ResultText:      "Waiting for input",
	})
	assert.True(t, r.Failed)
	assert.True(t, r.InputRequired)
	assert.Equal(t, "Waiting for input", r.FailureText)
}

// ---------------------------------------------------------------------------
// TestToolDescription — test cases for each tool type
// ---------------------------------------------------------------------------

func TestToolDescription(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		want     string
	}{
		// Bash/Shell
		{
			name:     "bash simple command",
			toolName: "Bash",
			input:    `{"command":"ls -la /tmp"}`,
			want:     "/tmp",
		},
		{
			name:     "bash command with pipe returns raw (no semantic)",
			toolName: "Bash",
			input:    `{"command":"cat file.go | grep TODO"}`,
			want:     "cat file.go | grep TODO",
		},
		{
			name:     "bash non-zero exit code",
			toolName: "Bash",
			input:    `{"command":"make build","exit_code":2}`,
			want:     "make build (exit:2)",
		},
		{
			name:     "bash zero exit code no suffix",
			toolName: "Bash",
			input:    `{"command":"echo hi","exit_code":0}`,
			want:     "echo hi",
		},
		{
			name:     "shell alias",
			toolName: "shell",
			input:    `{"command":"cat main.go"}`,
			want:     "main.go",
		},
		// Read
		{
			name:     "Read tool",
			toolName: "Read",
			input:    `{"file_path":"/Users/dev/main.go"}`,
			want:     "/Users/dev/main.go",
		},
		// Write
		{
			name:     "Write tool",
			toolName: "Write",
			input:    `{"file_path":"/Users/dev/output.txt","content":"data"}`,
			want:     "/Users/dev/output.txt",
		},
		// Edit
		{
			name:     "Edit tool",
			toolName: "Edit",
			input:    `{"file_path":"/src/handler.go"}`,
			want:     "/src/handler.go",
		},
		// MultiEdit
		{
			name:     "MultiEdit tool",
			toolName: "MultiEdit",
			input:    `{"file_path":"/src/model.go"}`,
			want:     "/src/model.go",
		},
		// Glob
		{
			name:     "Glob with pattern only",
			toolName: "Glob",
			input:    `{"pattern":"**/*.go"}`,
			want:     "**/*.go",
		},
		{
			name:     "Glob with pattern and path",
			toolName: "Glob",
			input:    `{"pattern":"*.ts","path":"src/"}`,
			want:     "*.ts in src/",
		},
		// Grep
		{
			name:     "Grep with pattern only",
			toolName: "Grep",
			input:    `{"pattern":"func main"}`,
			want:     "func main",
		},
		{
			name:     "Grep with pattern and path",
			toolName: "Grep",
			input:    `{"pattern":"TODO","path":"internal/"}`,
			want:     "TODO in internal/",
		},
		// Agent/Task
		{
			name:     "Agent with description",
			toolName: "Agent",
			input:    `{"description":"Investigate the failing test suite"}`,
			want:     "Investigate the failing test suite",
		},
		{
			name:     "Agent with prompt fallback",
			toolName: "Agent",
			input:    `{"prompt":"Fix the CI pipeline"}`,
			want:     "Fix the CI pipeline",
		},
		{
			name:     "Task with description",
			toolName: "Task",
			input:    `{"description":"Refactor database layer"}`,
			want:     "Refactor database layer",
		},
		// spawn_agent
		{
			name:     "spawn_agent with description",
			toolName: "spawn_agent",
			input:    `{"description":"Deploy to staging"}`,
			want:     "Deploy to staging",
		},
		{
			name:     "spawn_agent with prompt fallback",
			toolName: "spawn_agent",
			input:    `{"prompt":"Build the Docker image"}`,
			want:     "Build the Docker image",
		},
		// send_input / resume_agent
		{
			name:     "send_input",
			toolName: "send_input",
			input:    `{"prompt":"Yes, proceed with the merge"}`,
			want:     "Yes, proceed with the merge",
		},
		{
			name:     "resume_agent",
			toolName: "resume_agent",
			input:    `{"prompt":"Continue from where you left off"}`,
			want:     "Continue from where you left off",
		},
		// wait
		{
			name:     "wait with receiver threads",
			toolName: "wait",
			input:    `{"receiver_thread_ids":["t1","t2","t3"]}`,
			want:     "waiting on 3 sub-agent(s)",
		},
		{
			name:     "wait with no receiver threads",
			toolName: "wait",
			input:    `{}`,
			want:     "",
		},
		// WebFetch
		{
			name:     "WebFetch",
			toolName: "WebFetch",
			input:    `{"url":"https://example.com/api/docs"}`,
			want:     "https://example.com/api/docs",
		},
		// WebSearch
		{
			name:     "WebSearch",
			toolName: "WebSearch",
			input:    `{"query":"golang context best practices"}`,
			want:     "golang context best practices",
		},
		// TodoWrite
		{
			name:     "TodoWrite single task",
			toolName: "TodoWrite",
			input:    `{"todos":[{"content":"Fix the login bug","status":"pending"}]}`,
			want:     "Fix the login bug",
		},
		{
			name:     "TodoWrite multiple tasks",
			toolName: "TodoWrite",
			input:    `{"todos":[{"content":"Fix bug A","status":"pending"},{"content":"Fix bug B","status":"pending"},{"content":"Fix bug C","status":"pending"}]}`,
			want:     "3 tasks: Fix bug A",
		},
		{
			name:     "TodoWrite empty todos array",
			toolName: "TodoWrite",
			input:    `{"todos":[]}`,
			want:     "",
		},
		// TodoRead
		{
			name:     "TodoRead returns empty",
			toolName: "TodoRead",
			input:    `{}`,
			want:     "",
		},
		// Unknown tool falls back to first string field
		{
			name:     "unknown tool returns first string field",
			toolName: "SomeCustomTool",
			input:    `{"query":"search for this"}`,
			want:     "search for this",
		},
		// Empty / nil input
		{
			name:     "empty input returns empty",
			toolName: "Bash",
			input:    ``,
			want:     "",
		},
		{
			name:     "invalid JSON input returns empty",
			toolName: "Bash",
			input:    `{broken`,
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolDescription(tc.toolName, json.RawMessage(tc.input))
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestShellSemanticDesc — test cases for semantic descriptions of shell commands
// ---------------------------------------------------------------------------

func TestShellSemanticDesc(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		// cat family
		{"cat single file", "cat main.go", "main.go"},
		{"head single file", "head main.go", "main.go"},
		{"head with flag and value", "head -n 20 main.go", ""},
		{"tail single file", "tail -f output.log", "output.log"},
		{"less single file", "less README.md", "README.md"},
		{"bat single file", "bat config.yaml", "config.yaml"},

		// ls / find
		{"ls directory", "ls src/", "src/"},
		{"find directory", "find /tmp", "/tmp"},

		// mkdir / rm / cp / mv / touch
		{"mkdir", "mkdir -p /tmp/newdir", "mkdir /tmp/newdir"},
		{"rmdir", "rmdir /tmp/olddir", "rmdir /tmp/olddir"},
		{"rm file", "rm -f /tmp/junk.txt", "rm /tmp/junk.txt"},
		{"touch file", "touch /tmp/new.txt", "touch /tmp/new.txt"},

		// cat with flags
		{"cat -n file", "cat -n file.go", "file.go"},

		// Commands with pipes return empty (too complex)
		{"pipe command", "cat file.go | grep TODO", ""},
		{"semicolon", "echo hi; ls", ""},
		{"backtick", "echo `date`", ""},
		{"dollar expansion", "echo $(pwd)", ""},

		// No operands
		{"bare command no operand", "ls", ""},

		// Multiple operands
		{"multiple files", "cat a.go b.go", ""},

		// env var prefix stripping
		{"env var prefix", "FOO=bar cat file.txt", "file.txt"},
		{"multiple env prefixes", "A=1 B=2 cat file.txt", "file.txt"},

		// Unknown verb
		{"unknown verb", "terraform apply plan.tf", ""},

		// Single word (no args)
		{"single word", "pwd", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shellSemanticDesc(tc.cmd)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestToolDescriptionTruncation — verify long inputs are truncated
// ---------------------------------------------------------------------------

func TestToolDescriptionTruncation(t *testing.T) {
	// Build a command longer than 560 chars
	longCmd := ""
	for i := 0; i < 600; i++ {
		longCmd += "x"
	}
	input, _ := json.Marshal(map[string]any{"command": longCmd})
	desc := toolDescription("Bash", input)
	// Should be truncated to 560 runes + ellipsis
	assert.LessOrEqual(t, len([]rune(desc)), 561)
	assert.Contains(t, desc, "…")
}

func TestToolDescriptionAgentTruncation(t *testing.T) {
	longDesc := ""
	for i := 0; i < 400; i++ {
		longDesc += "y"
	}
	input, _ := json.Marshal(map[string]any{"description": longDesc})
	desc := toolDescription("Agent", input)
	assert.LessOrEqual(t, len([]rune(desc)), 301)
	assert.Contains(t, desc, "…")
}

// ---------------------------------------------------------------------------
// ValidateClaudeCLICommand / ValidateCodexCLICommand
// ---------------------------------------------------------------------------

func TestValidateClaudeCLICommandEmpty(t *testing.T) {
	// Empty command falls back to ValidateClaudeCLI, which will fail in test env.
	err := ValidateClaudeCLICommand("")
	// We just verify it returns an error (claude not on PATH in CI).
	// If claude IS on PATH, it returns nil which is also fine.
	_ = err
}

func TestValidateClaudeCLICommandClaude(t *testing.T) {
	err := ValidateClaudeCLICommand("claude")
	_ = err
}

func TestValidateCodexCLICommandEmpty(t *testing.T) {
	err := ValidateCodexCLICommand("")
	_ = err
}

func TestValidateCodexCLICommandCodex(t *testing.T) {
	err := ValidateCodexCLICommand("codex")
	_ = err
}

func TestValidateCodexCLICommandCustomPath(t *testing.T) {
	// Non-existent path should fail.
	err := ValidateCodexCLICommand("/nonexistent/path/to/codex-cli")
	assert.Error(t, err)
}

func TestValidateClaudeCLICommandCustomPath(t *testing.T) {
	err := ValidateClaudeCLICommand("/nonexistent/path/to/claude-cli")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// TestParseLineAssistantWithCachedTokens — verify CachedInputTokens is parsed
// ---------------------------------------------------------------------------

func TestParseLineAssistantWithCachedTokens(t *testing.T) {
	line := []byte(`{"type":"assistant","usage":{"input_tokens":500,"cached_input_tokens":200,"output_tokens":100}}`)
	ev, err := ParseLine(line)
	require.NoError(t, err)
	assert.Equal(t, 500, ev.Usage.InputTokens)
	assert.Equal(t, 200, ev.Usage.CachedInputTokens)
	assert.Equal(t, 100, ev.Usage.OutputTokens)
}

// ---------------------------------------------------------------------------
// TestParseCodexLineItemStartedNilItem — edge case
// ---------------------------------------------------------------------------

func TestParseCodexLineItemStartedMissingItem(t *testing.T) {
	_, err := ParseCodexLine([]byte(`{"type":"item.started"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing item")
}

func TestParseCodexLineItemCompletedMissingItem(t *testing.T) {
	_, err := ParseCodexLine([]byte(`{"type":"item.completed"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing item")
}

// ---------------------------------------------------------------------------
// TestParseCodexLineItemStartedCollabNoTool — falls back to item type
// ---------------------------------------------------------------------------

func TestParseCodexLineItemStartedCollabNoTool(t *testing.T) {
	line, _ := json.Marshal(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"id":   "i10",
			"type": "collab_tool_call",
			"tool": "",
		},
	})
	ev, err := ParseCodexLine(line)
	require.NoError(t, err)
	assert.Equal(t, "collab_tool_call", ev.ToolCalls[0].Name)
	assert.True(t, ev.InProgress)
}

// ---------------------------------------------------------------------------
// isCodexLogFile
// ---------------------------------------------------------------------------

func TestIsCodexLogFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"codex-session.jsonl", true},
		{"codex-1234567890.jsonl", true},
		{"codex-.jsonl", true},
		{"session-abc.jsonl", false},
		{"codex-session.json", false},
		{"codex-session.txt", false},
		{"abc123.jsonl", false},
		{"codex-session", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isCodexLogFile(tc.name))
		})
	}
}

// ---------------------------------------------------------------------------
// newMaxScanner — verify it can handle lines > 64 KiB
// ---------------------------------------------------------------------------

func TestNewMaxScanner(t *testing.T) {
	// Create a line larger than 64 KiB (default bufio.Scanner limit)
	bigLine := strings.Repeat("x", 100_000)
	r := strings.NewReader(bigLine + "\n")
	s := newMaxScanner(r)
	assert.True(t, s.Scan())
	assert.Equal(t, 100_000, len(s.Text()))
	assert.NoError(t, s.Err())
}

// ---------------------------------------------------------------------------
// parseSessionLogsMulti — local filesystem test
// ---------------------------------------------------------------------------

func TestParseSessionLogsMulti_NonExistentDir(t *testing.T) {
	entries, err := parseSessionLogsMulti("/nonexistent/path/to/logs")
	assert.NoError(t, err)
	assert.Nil(t, entries)
}

func TestParseSessionLogsMulti_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	entries, err := parseSessionLogsMulti(dir)
	assert.NoError(t, err)
	assert.Nil(t, entries)
}

func TestParseSessionLogsMulti_ClaudeAndCodexFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a Claude session log
	claudeLines := strings.Join([]string{
		`{"type":"system","session_id":"claude-sess"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello from claude"}]}}`,
		`{"type":"result","subtype":"success","session_id":"claude-sess"}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude-sess.jsonl"), []byte(claudeLines), 0o644))

	// Write a Codex session log
	codexLines := strings.Join([]string{
		`{"type":"thread.started","thread_id":"codex-thr"}`,
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"hello from codex"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex-1234.jsonl"), []byte(codexLines), 0o644))

	// Write a non-jsonl file (should be ignored)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644))

	// Write a subdirectory (should be ignored)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))

	entries, err := parseSessionLogsMulti(dir)
	require.NoError(t, err)
	assert.True(t, len(entries) > 0, "should have parsed entries from both files")

	// Verify we got entries from both Claude and Codex logs
	var hasClaudeEntry, hasCodexEntry bool
	for _, e := range entries {
		if e.SessionID == "claude-sess" {
			hasClaudeEntry = true
		}
		if e.SessionID == "codex-1234" {
			hasCodexEntry = true
		}
	}
	assert.True(t, hasClaudeEntry, "should have Claude session entries")
	assert.True(t, hasCodexEntry, "should have Codex session entries")
}

func TestParseSessionLogsMulti_TruncatesExcessEntries(t *testing.T) {
	dir := t.TempDir()
	// Write a file with many lines
	var lines []string
	for i := 0; i < maxSubLogLines+100; i++ {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"line"}]}}`)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big-sess.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	entries, err := parseSessionLogsMulti(dir)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(entries), maxSubLogLines)
}

// ---------------------------------------------------------------------------
// readJSONLFileMultiWith
// ---------------------------------------------------------------------------

func TestReadJSONLFileMultiWith_ClaudeFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-test.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"something failed"}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	entries, err := readJSONLFileMultiWith(path, ParseLine)
	require.NoError(t, err)
	assert.Len(t, entries, 3) // text entry + tool entry + error entry

	// Check session ID is derived from filename
	for _, e := range entries {
		assert.Equal(t, "sess-test", e.SessionID)
	}
	// First entry is text
	assert.Equal(t, "text", entries[0].Event)
	assert.Equal(t, "thinking...", entries[0].Message)
	// Second entry is action
	assert.Equal(t, "action", entries[1].Event)
	assert.Contains(t, entries[1].Message, "Bash")
	// Third entry is error
	assert.Equal(t, "error", entries[2].Event)
}

func TestReadJSONLFileMultiWith_CodexFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-sess.jsonl")
	content := strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"hello codex"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"echo hi","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	entries, err := readJSONLFileMultiWith(path, ParseCodexLine)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	assert.Equal(t, "codex-sess", entries[0].SessionID)
}

func TestReadJSONLFileMultiWith_NonExistentFile(t *testing.T) {
	_, err := readJSONLFileMultiWith("/nonexistent/file.jsonl", ParseLine)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// streamLineToEntriesWith
// ---------------------------------------------------------------------------

func TestStreamLineToEntriesWith_SkipsInProgress(t *testing.T) {
	line := []byte(`{"type":"item.started","item":{"id":"i0","type":"command_execution","command":"ls"}}`)
	entries := streamLineToEntriesWith(line, ParseCodexLine, "sess-1")
	assert.Empty(t, entries)
}

func TestStreamLineToEntriesWith_SkipsInvalidJSON(t *testing.T) {
	entries := streamLineToEntriesWith([]byte(`{broken`), ParseLine, "sess-1")
	assert.Nil(t, entries)
}

func TestStreamLineToEntriesWith_SkipsSystemEvents(t *testing.T) {
	line := []byte(`{"type":"system","session_id":"sess-1"}`)
	entries := streamLineToEntriesWith(line, ParseLine, "sess-1")
	assert.Nil(t, entries)
}

func TestStreamLineToEntriesWith_EmptyTextSkipped(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"   "}]}}`)
	entries := streamLineToEntriesWith(line, ParseLine, "sess-1")
	assert.Empty(t, entries)
}

func TestStreamLineToEntriesWith_SuccessResultReturnsNil(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","session_id":"s1"}`)
	entries := streamLineToEntriesWith(line, ParseLine, "sess-1")
	assert.Nil(t, entries)
}

// ---------------------------------------------------------------------------
// multi.go helpers — firstCommandToken, isEnvAssignment, parseBackendHint
// ---------------------------------------------------------------------------

func TestFirstCommandToken(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"simple", "claude -p hello", "claude"},
		{"with env var", "FOO=bar claude -p hello", "claude"},
		{"multiple env vars", "A=1 B=2 codex exec", "codex"},
		{"env command", "env FOO=bar claude -p hello", "claude"},
		{"env with flags", "env -i FOO=bar claude -p hello", "claude"},
		{"empty", "", ""},
		{"only env vars", "FOO=bar BAZ=qux", ""},
		{"env with only env vars", "env FOO=bar", ""},
		{"absolute path", "/usr/local/bin/claude -p test", "/usr/local/bin/claude"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, firstCommandToken(tc.command))
		})
	}
}

func TestIsEnvAssignment(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"FOO=bar", true},
		{"_VAR=123", true},
		{"A=", true},
		{"var123=val", true},
		{"=nope", false},
		{"nope", false},
		{"123=bad", false},
		{"-flag", false},
		{"", false},
		{"a.b=c", false},
	}
	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			assert.Equal(t, tc.want, isEnvAssignment(tc.token))
		})
	}
}

func TestParseBackendHint(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantBackend string
		wantCleaned string
	}{
		{"no hint", "/usr/bin/claude -p test", "", "/usr/bin/claude -p test"},
		{"codex hint", "@@itervox-backend=codex /usr/bin/codex", "codex", "/usr/bin/codex"},
		{"claude hint", "@@itervox-backend=claude /usr/bin/claude", "claude", "/usr/bin/claude"},
		{"hint only backend no command", "@@itervox-backend=codex", "codex", ""},
		{"empty after prefix", "@@itervox-backend=", "", "@@itervox-backend="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend, cleaned := parseBackendHint(tc.command)
			assert.Equal(t, tc.wantBackend, backend)
			assert.Equal(t, tc.wantCleaned, cleaned)
		})
	}
}

func TestCommandWithBackendHint(t *testing.T) {
	// Empty backend returns command unchanged
	assert.Equal(t, "/usr/bin/claude", CommandWithBackendHint("/usr/bin/claude", ""))

	// Already has matching backend from command name
	assert.Equal(t, "codex", CommandWithBackendHint("codex", "codex"))

	// Add new hint
	got := CommandWithBackendHint("/usr/bin/myagent", "codex")
	assert.Contains(t, got, "@@itervox-backend=codex")
	assert.Contains(t, got, "/usr/bin/myagent")

	// Replace existing hint
	got = CommandWithBackendHint("@@itervox-backend=claude /usr/bin/myagent", "codex")
	assert.Contains(t, got, "@@itervox-backend=codex")
}

// ---------------------------------------------------------------------------
// logShellDetail — edge cases
// ---------------------------------------------------------------------------

func TestLogShellDetail_InvalidJSON(t *testing.T) {
	log := &testLogger{}
	logShellDetail(log, "test", "sess-1", json.RawMessage(`{broken`))
	assert.Empty(t, log.info, "invalid JSON should produce no log output")
}

func TestLogShellDetail_NoMeaningfulFields(t *testing.T) {
	log := &testLogger{}
	logShellDetail(log, "test", "sess-1", json.RawMessage(`{"command":"ls"}`))
	assert.Empty(t, log.info, "no exit_code/status/output should produce no log output")
}

func TestLogShellDetail_WithExitCode(t *testing.T) {
	log := &testLogger{}
	logShellDetail(log, "test", "sess-1", json.RawMessage(`{"exit_code":1,"status":"failed","output":"error msg"}`))
	assert.Len(t, log.info, 1)
}

type testLogger struct {
	info []string
}

func (l *testLogger) Info(msg string, _ ...any) { l.info = append(l.info, msg) }
func (l *testLogger) Debug(_ string, _ ...any)  {}
func (l *testLogger) Warn(_ string, _ ...any)   {}

// ---------------------------------------------------------------------------
// loginShell — test fallback
// ---------------------------------------------------------------------------

func TestLoginShellFallback(t *testing.T) {
	orig := os.Getenv("SHELL")
	defer func() { _ = os.Setenv("SHELL", orig) }()
	_ = os.Unsetenv("SHELL")
	assert.Equal(t, "bash", loginShell())
}

func TestLoginShellFromEnv(t *testing.T) {
	orig := os.Getenv("SHELL")
	defer func() { _ = os.Setenv("SHELL", orig) }()
	_ = os.Setenv("SHELL", "/bin/zsh")
	assert.Equal(t, "/bin/zsh", loginShell())
}

func TestBuildValidationShellCommand_DetachesFromTTY(t *testing.T) {
	cmd := buildValidationShellCommand(context.Background(), "/bin/zsh", "claude --version")
	require.Equal(t, []string{"/bin/zsh", "-ilc", "claude --version"}, cmd.Args)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid)
}
