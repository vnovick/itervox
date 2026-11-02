package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CursorRunner runs a Cursor CLI turn in print mode with stream-json
// output.
//
// Fresh turn (sessionID == nil):
//
//	<command> -p --output-format stream-json --force --trust -- <prompt>
//
// Continuation:
//
//	<command> -p --output-format stream-json --resume <id> --force --trust -- <prompt>
//
// The Cursor CLI takes its prompt as an argument only, so the prompt is the
// last argument: locally the direct path passes it in argv and the login
// shell expands it from the prompt file on fd 3; over SSH it is expanded
// from the script's prompt variable. A prompt is therefore bounded by the
// OS argument limit (128 KiB per argument on Linux), unlike the stdin
// backends. A model is chosen with --model in the configured command.
//
// Ground truth: Cursor's headless CLI docs (cursor.com/docs/cli/headless,
// cursor.com/docs/cli/reference/output-format). The stream-json lines are
// Claude's (system/init, user, assistant, result with session_id) plus
// tool_call started/completed lines, so ParseCursorLine reuses ParseLine
// for every line but tool_call.
type CursorRunner struct{}

// NewCursorRunner constructs a CursorRunner.
func NewCursorRunner() *CursorRunner {
	return &CursorRunner{}
}

// ValidateCursorCLICommand checks that the program the command names (or
// `cursor-agent` for an empty command) runs `--version`.
func ValidateCursorCLICommand(command string) error {
	return validateCLI(programOf(command, "cursor-agent"), "ensure the Cursor CLI ('cursor-agent') is installed and on PATH")
}

// cursorPermissionFlags maps a permission mode onto the Cursor CLI.
//
// In print mode the Cursor CLI only PROPOSES file changes unless --force is
// given, and stops on an untrusted workspace unless --trust is. Both
// itervox modes let the agent edit its workspace (sandbox is Claude's
// acceptEdits), so both pass --force and --trust; the CLI has no
// non-interactive mode that edits but confines commands, so sandbox mode is
// not a confinement on Cursor — use isolation: process for that.
func cursorPermissionFlags(PermissionMode) []string {
	return []string{"--force", "--trust"}
}

// cursorArgs are the turn's arguments after the command (the prompt is
// added by cliTurn).
func cursorArgs(sessionID *string, mode PermissionMode) []string {
	args := []string{"-p", "--output-format", "stream-json"}
	if sessionID != nil && *sessionID != "" {
		args = append(args, "--resume", *sessionID)
	}
	return append(args, cursorPermissionFlags(mode)...)
}

// cursorTurn is the cliTurn for one Cursor turn.
func cursorTurn(command string, sessionID *string, mode PermissionMode) cliTurn {
	return cliTurn{
		name: "cursor", fallback: "cursor-agent", command: command,
		args: cursorArgs(sessionID, mode), promptArg: true, parse: ParseCursorLine,
	}
}

// RunTurn runs a single Cursor turn (see the type comment).
func (c *CursorRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, _ string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	result, _, err := runCLITurn(ctx, log, onProgress, cursorTurn(command, sessionID, permissionMode),
		prompt, workspacePath, workerHost, readTimeoutMs, turnTimeoutMs)
	if result.SessionID == "" && sessionID != nil {
		result.SessionID = *sessionID // the resumed session, when the stream did not repeat it
	}
	return result, err
}

// cursorRawToolCall is a Cursor tool_call line.
type cursorRawToolCall struct {
	Subtype   string                     `json:"subtype"`
	CallID    string                     `json:"call_id"`
	SessionID string                     `json:"session_id"`
	ToolCall  map[string]json.RawMessage `json:"tool_call"`
}

// cursorUsage is the token usage a Cursor result line may carry; both
// camelCase and snake_case keys are read.
type cursorUsage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	CacheReadTokens   int `json:"cacheReadTokens"`
	InputTokensSnake  int `json:"input_tokens"`
	OutputTokensSnake int `json:"output_tokens"`
	CacheReadSnake    int `json:"cache_read_input_tokens"`
}

// cursorToolNames maps Cursor's tool_call keys onto the tool names the log
// stream describes (toolDescription): readToolCall → read, …
var cursorToolNames = map[string]string{
	"readToolCall":        "read",
	"writeToolCall":       "write",
	"editToolCall":        "edit",
	"deleteToolCall":      "delete",
	"shellToolCall":       "shell",
	"grepToolCall":        "grep",
	"globToolCall":        "glob",
	"lsToolCall":          "ls",
	"todoToolCall":        "todowrite",
	"updateTodosToolCall": "todowrite",
	"webFetchToolCall":    "webfetch",
	"webSearchToolCall":   "websearch",
}

// ParseCursorLine maps one Cursor stream-json line to a StreamEvent:
// tool_call started/completed lines become an in-progress/completed tool
// call, every other line is parsed by ParseLine (the Claude stream-json
// parser), plus the token usage a result line may carry.
func ParseCursorLine(line []byte) (StreamEvent, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return StreamEvent{}, fmt.Errorf("cursor: empty line")
	}
	var head struct {
		Type  string       `json:"type"`
		Usage *cursorUsage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(trimmed), &head); err != nil {
		return StreamEvent{}, fmt.Errorf("cursor: parse line: %w", err)
	}
	if head.Type == "tool_call" {
		return parseCursorToolCall([]byte(trimmed))
	}
	ev, err := ParseLine([]byte(trimmed))
	if err != nil {
		return ev, err
	}
	if head.Type == EventResult && head.Usage != nil {
		u := head.Usage
		ev.Usage = UsageSnapshot{
			InputTokens:       max(u.InputTokens, u.InputTokensSnake),
			OutputTokens:      max(u.OutputTokens, u.OutputTokensSnake),
			CachedInputTokens: max(u.CacheReadTokens, u.CacheReadSnake),
		}
	}
	return ev, nil
}

func parseCursorToolCall(line []byte) (StreamEvent, error) {
	var raw cursorRawToolCall
	if err := json.Unmarshal(line, &raw); err != nil {
		return StreamEvent{}, fmt.Errorf("cursor: parse tool_call: %w", err)
	}
	if len(raw.ToolCall) == 0 {
		return StreamEvent{}, fmt.Errorf("cursor: tool_call without a tool")
	}
	// One key per line ({"readToolCall": {...}}); sorted for determinism.
	key := slices.Sorted(maps.Keys(raw.ToolCall))[0]
	var body struct {
		Args   map[string]any  `json:"args"`
		Result json.RawMessage `json:"result"`
		Name   string          `json:"name"` // function tool calls
	}
	_ = json.Unmarshal(raw.ToolCall[key], &body)
	name, ok := cursorToolNames[key]
	if !ok {
		name = strings.TrimSuffix(key, "ToolCall")
		if body.Name != "" {
			name = body.Name
		}
	}
	input := map[string]any{}
	maps.Copy(input, body.Args)
	input["call_id"] = raw.CallID
	if raw.Subtype == "completed" {
		input["status"] = "completed"
		if len(body.Result) > 0 {
			var res struct {
				Success json.RawMessage `json:"success"`
				Error   json.RawMessage `json:"error"`
			}
			if json.Unmarshal(body.Result, &res) == nil && len(res.Error) > 0 && string(res.Error) != "null" {
				input["status"] = "error"
			}
			input["result"] = body.Result
		}
	} else {
		input["status"] = "in_progress"
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return StreamEvent{}, fmt.Errorf("cursor: marshal tool_call: %w", err)
	}
	return StreamEvent{
		Type:       EventAssistant,
		SessionID:  raw.SessionID,
		InProgress: raw.Subtype == "started",
		ToolCalls:  []ToolCall{{Name: name, Input: encoded}},
	}, nil
}
