package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The itervox agent protocol, version 1: how `backend: custom` runs any
// command as an agent (site/src/content/docs/guides/backends.mdx is the user-facing spec;
// docs/examples/custom-agent.sh is a working wrapper).
//
// Input. The command runs in the issue's workspace (over SSH: cd'd there)
// with:
//   - the whole prompt on stdin, then EOF;
//   - ITERVOX_AGENT_PROTOCOL=1;
//   - ITERVOX_PERMISSION_MODE=bypass|sandbox (the profile's mode);
//   - ITERVOX_SESSION_ID=<id> on a continuation turn: the id the agent
//     reported in its session event, to resume. Unset on a fresh turn.
//   - ITERVOX_AGENT=1, as for every agent itervox starts.
//
// Output. One JSON object per stdout line, each with a "type":
//
//	{"type":"session","session_id":"abc"}                   the session id (resume key)
//	{"type":"text","text":"…"}                               assistant text
//	{"type":"tool_call","name":"bash","input":{…}}           a tool call started
//	{"type":"tool_result","name":"bash","input":{…},"output":"…","is_error":false}
//	                                                         a tool call finished
//	{"type":"usage","input_tokens":1,"cached_input_tokens":0,"output_tokens":2,"cost_usd":0.01}
//	                                                         tokens/cost since the last usage line
//	{"type":"input_required","question":"…"}                 the agent needs a human answer
//	{"type":"rate_limit","kind":"quota"|"throttle","message":"…","resets_at":"RFC3339","retry_after_seconds":60}
//	{"type":"done","result":"summary"}                       the turn succeeded
//	{"type":"failed","reason":"…"}                           the turn failed
//
// Lines that are not JSON, and unknown types, are ignored. Usage and cost
// are additive within a turn; itervox keeps the session total.
//
// Exit status. 0 ends the turn: a success unless a failed line was printed.
// Any other status fails the turn (stderr's tail becomes the failure text);
// 75 (EX_TEMPFAIL) without a rate_limit line means "usage limit reached".

// CustomProtocolVersion is the protocol version itervox speaks.
const CustomProtocolVersion = 1

// Environment names of the custom protocol.
const (
	CustomEnvProtocol       = "ITERVOX_AGENT_PROTOCOL"
	CustomEnvSessionID      = "ITERVOX_SESSION_ID"
	CustomEnvPermissionMode = "ITERVOX_PERMISSION_MODE"
)

// customExitUsageLimit is the exit status a custom agent uses for a usage
// limit it did not describe with a rate_limit line.
const customExitUsageLimit = 75

// errCustomNoCommand fails a custom turn with no command to run.
var errCustomNoCommand = errors.New("custom: backend custom needs a command (agent.command or the profile's command)")

// CustomRunner runs a command that speaks the itervox agent protocol.
type CustomRunner struct {
	costs sessionCostLedger
}

// NewCustomRunner constructs a CustomRunner.
func NewCustomRunner() *CustomRunner {
	return &CustomRunner{}
}

// ValidateCustomCommand checks that a custom backend's command names a
// program that can be found. It does not run it: a wrapper need not
// support --version.
func ValidateCustomCommand(command string, lookPath func(string) (string, error)) error {
	prog := programOf(command, "")
	if prog == "" {
		return errCustomNoCommand
	}
	if _, err := lookPath(prog); err != nil {
		return fmt.Errorf("custom: %q not found: %w", prog, err)
	}
	return nil
}

// customEnv is the protocol environment of one turn.
func customEnv(sessionID *string, mode PermissionMode) []string {
	if mode == "" {
		mode = DefaultPermissionMode
	}
	env := []string{
		fmt.Sprintf("%s=%d", CustomEnvProtocol, CustomProtocolVersion),
		CustomEnvPermissionMode + "=" + string(mode),
	}
	if sessionID != nil && *sessionID != "" {
		env = append(env, CustomEnvSessionID+"="+*sessionID)
	}
	return env
}

// customTurn is the cliTurn for one custom-protocol turn.
func customTurn(command string, sessionID *string, mode PermissionMode, stream *customStream) cliTurn {
	return cliTurn{name: "custom", command: command, env: customEnv(sessionID, mode), parse: stream.parse}
}

// RunTurn runs a single custom-protocol turn.
func (r *CustomRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, _ string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	if strings.TrimSpace(command) == "" {
		return TurnResult{Failed: true, FailureText: errCustomNoCommand.Error()}, errCustomNoCommand
	}
	stream := &customStream{}
	result, out, err := runCLITurn(ctx, log, onProgress, customTurn(command, sessionID, permissionMode, stream),
		prompt, workspacePath, workerHost, readTimeoutMs, turnTimeoutMs)
	if result.SessionID == "" && sessionID != nil {
		result.SessionID = *sessionID
	}
	if stream.costKnown {
		c := r.costs.add(result.SessionID, stream.cost)
		result.CostUSD = &c
	}
	if stream.inputRequired && !result.Failed {
		result.InputRequired = true
	}
	if out.exitCode == customExitUsageLimit && result.LastLimit == nil {
		result.LastLimit = &LimitSignal{Kind: LimitKindQuota, Source: LimitSourceText,
			Message: "custom agent exited 75: usage limit reached"}
	}
	return result, err
}

// customRawEvent is one protocol line.
type customRawEvent struct {
	Type              string          `json:"type"`
	SessionID         string          `json:"session_id"`
	Text              string          `json:"text"`
	Name              string          `json:"name"`
	Input             json.RawMessage `json:"input"`
	Output            string          `json:"output"`
	IsError           bool            `json:"is_error"`
	InputTokens       int             `json:"input_tokens"`
	CachedInputTokens int             `json:"cached_input_tokens"`
	OutputTokens      int             `json:"output_tokens"`
	CostUSD           flexNumber      `json:"cost_usd"`
	Question          string          `json:"question"`
	Kind              string          `json:"kind"`
	Message           string          `json:"message"`
	ResetsAt          string          `json:"resets_at"`
	RetryAfterSeconds flexNumber      `json:"retry_after_seconds"`
	Result            string          `json:"result"`
	Reason            string          `json:"reason"`
}

// ParseCustomLine maps one custom-protocol line to a StreamEvent. Unknown
// types return an error so readLines skips them.
func ParseCustomLine(line []byte) (StreamEvent, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return StreamEvent{}, fmt.Errorf("custom: empty line")
	}
	var raw customRawEvent
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return StreamEvent{}, fmt.Errorf("custom: parse line: %w", err)
	}
	ev := StreamEvent{SessionID: raw.SessionID}
	switch raw.Type {
	case "session":
		ev.Type, ev.Subtype = EventSystem, "init"
	case "text":
		ev.Type = EventAssistant
		if raw.Text != "" {
			ev.TextBlocks, ev.Message = []string{raw.Text}, raw.Text
		}
	case "tool_call", "tool_result":
		ev.Type = EventAssistant
		ev.InProgress = raw.Type == "tool_call"
		ev.ToolCalls = []ToolCall{{Name: raw.Name, Input: customToolInput(raw)}}
	case "usage":
		ev.Type = EventAssistant
		ev.Usage = UsageSnapshot{InputTokens: raw.InputTokens, CachedInputTokens: raw.CachedInputTokens, OutputTokens: raw.OutputTokens}
		ev.CostUSD = raw.CostUSD.ptr()
	case "input_required":
		ev.Type, ev.IsInputRequired = EventAssistant, true
		if q := strings.TrimSpace(raw.Question); q != "" {
			ev.TextBlocks, ev.Message = []string{q}, q
		}
	case "rate_limit":
		ev.Type, ev.Message = EventError, raw.Message
		ev.Limit = customLimit(raw)
	case "done":
		ev.Type, ev.ResultText = EventResult, raw.Result
	case "failed":
		ev.Type, ev.IsError = EventResult, true
		ev.ResultText = cmp.Or(strings.TrimSpace(raw.Reason), "custom agent reported failure")
		ev.Limit = ClassifyLimitText(ev.ResultText, time.Now(), time.Local)
	default:
		return StreamEvent{}, fmt.Errorf("custom: skip event type %q", raw.Type)
	}
	return ev, nil
}

// customToolInput is the call's input object, with the result's output and
// error status added for a tool_result.
func customToolInput(raw customRawEvent) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(raw.Input) > 0 {
		_ = json.Unmarshal(raw.Input, &m)
	}
	if raw.Type == "tool_result" {
		status := "completed"
		if raw.IsError {
			status = "error"
		}
		m["status"], _ = json.Marshal(status)
		if raw.Output != "" {
			m["output"], _ = json.Marshal(raw.Output)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// customLimit builds the LimitSignal of a rate_limit line. kind defaults to
// what the message says, else quota (an agent reports a limit it hit).
func customLimit(raw customRawEvent) *LimitSignal {
	sig := &LimitSignal{Kind: LimitKindQuota, Source: LimitSourceRateLimitEvent, Message: boundLimitMessage(raw.Message)}
	switch LimitKind(raw.Kind) {
	case LimitKindQuota, LimitKindThrottle:
		sig.Kind = LimitKind(raw.Kind)
	default:
		if k := limitKindOfText(normalizeLimitText(raw.Message)); k != "" {
			sig.Kind = k
		}
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw.ResetsAt)); err == nil {
		sig.ResetsAt = t
	}
	if s, ok := raw.RetryAfterSeconds.int(); ok && s > 0 {
		sig.RetryAfter = time.Duration(min(s, 24*60*60)) * time.Second
	}
	return sig
}

// customStream is one turn's parse state: the summed cost and whether the
// agent asked for input (ApplyEvent reads both from result events only).
type customStream struct {
	cost          float64
	costKnown     bool
	inputRequired bool
}

func (s *customStream) parse(line []byte) (StreamEvent, error) {
	ev, err := ParseCustomLine(line)
	if err != nil {
		return ev, err
	}
	if ev.CostUSD != nil {
		s.cost += *ev.CostUSD
		s.costKnown = true
		ev.CostUSD = nil
	}
	if ev.IsInputRequired {
		s.inputRequired = true
	}
	return ev, nil
}
