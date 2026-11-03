package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"
)

// OpenCode `opencode run --format json` event stream.
//
// Ground truth: packages/cli/src/run/noninteractive.ts of sst/opencode at
// @opencode/cli 2.2.5 (commit d9184f2, 2026-11-02); the same line types
// were printed by the 1.x `run --format json`. Every line is
//
//	{"type": T, "timestamp": ms, "sessionID": "ses_…", …}
//
// with T one of:
//
//   - step_start  {part: {type: "step-start", messageID, snapshot}}
//   - text        {part: {type: "text", text, time}}
//   - reasoning   {part: {type: "reasoning", text}} — only with --thinking
//   - tool_use    {part: {type: "tool", tool, state: {status:
//     "completed"|"error", input, output, error, title}}}
//   - step_finish {part: {type: "step-finish", reason, cost, tokens:
//     {input, output, reasoning, cache: {read, write}}}}
//   - error       {error: {type|name, message, data?: {message,
//     statusCode}}} — the run then exits 1
//
// There is no end-of-turn result line: the process exits when the session
// goes idle. A question or permission prompt the model raises is answered
// by the CLI itself in a non-interactive run (a question is cancelled with
// feedback; a permission is rejected, or approved with --auto), so the
// stream carries no input-required event — the sentinel is the only path.

type openCodeRawEvent struct {
	Type      string           `json:"type"`
	SessionID string           `json:"sessionID"`
	Part      *openCodeRawPart `json:"part"`
	Error     *openCodeError   `json:"error"`
}

type openCodeRawPart struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	Tool   string          `json:"tool"`
	State  *openCodeState  `json:"state"`
	Cost   *float64        `json:"cost"`
	Tokens *openCodeTokens `json:"tokens"`
	Reason string          `json:"reason"`
}

type openCodeState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
	Title  string          `json:"title"`
}

type openCodeTokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

type openCodeError struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Data    *struct {
		Message    string     `json:"message"`
		StatusCode flexNumber `json:"statusCode"`
	} `json:"data"`
	StatusCode flexNumber `json:"statusCode"`
}

// message is the error's text: message, else data.message, else its name.
func (e *openCodeError) message() string {
	switch {
	case e.Message != "":
		return e.Message
	case e.Data != nil && e.Data.Message != "":
		return e.Data.Message
	case e.Name != "":
		return e.Name
	}
	return e.Type
}

func (e *openCodeError) status() int {
	if s, ok := e.StatusCode.int(); ok {
		return s
	}
	if e.Data != nil {
		if s, ok := e.Data.StatusCode.int(); ok {
			return s
		}
	}
	return 0
}

// OpenCode StreamEvent subtypes (system events).
const (
	openCodeSubtypeStepStart = "step_start"
)

// ParseOpenCodeLine maps one `opencode run --format json` line to a
// StreamEvent. A step_start is a system event carrying the session id
// (subtype step_start; openCodeStream marks the first one "init"), text a
// text block, tool_use a completed tool call, step_finish the step's token
// usage (cost is read by openCodeStream), error a failed result. Other
// line types return an error so readLines skips them.
func ParseOpenCodeLine(line []byte) (StreamEvent, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return StreamEvent{}, fmt.Errorf("opencode: empty line")
	}
	var raw openCodeRawEvent
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return StreamEvent{}, fmt.Errorf("opencode: parse line: %w", err)
	}
	ev := StreamEvent{SessionID: raw.SessionID}
	switch raw.Type {
	case "step_start":
		ev.Type, ev.Subtype = EventSystem, openCodeSubtypeStepStart
	case "text":
		ev.Type = EventAssistant
		if raw.Part != nil && strings.TrimSpace(raw.Part.Text) != "" {
			ev.TextBlocks = []string{raw.Part.Text}
			ev.Message = raw.Part.Text
		}
	case "tool_use":
		if raw.Part == nil {
			return StreamEvent{}, fmt.Errorf("opencode: tool_use missing part")
		}
		ev.Type = EventAssistant
		ev.ToolCalls = []ToolCall{{Name: raw.Part.Tool, Input: openCodeToolInput(raw.Part.State)}}
	case "step_finish":
		ev.Type = EventAssistant
		if raw.Part != nil && raw.Part.Tokens != nil {
			tk := raw.Part.Tokens
			ev.Usage = UsageSnapshot{
				InputTokens:       tk.Input,
				CachedInputTokens: tk.Cache.Read,
				OutputTokens:      tk.Output + tk.Reasoning,
			}
		}
		if raw.Part != nil && raw.Part.Cost != nil {
			c := *raw.Part.Cost
			ev.CostUSD = &c
		}
	case "error":
		ev.Type, ev.IsError = EventResult, true
		if raw.Error != nil {
			msg := raw.Error.message()
			ev.ResultText = msg
			ev.Limit = openCodeLimit(msg, raw.Error.status())
			if !ev.Limit.Terminal() {
				ev.IsInputRequired = isInputRequiredMsg(msg)
			}
		}
		if ev.ResultText == "" {
			ev.ResultText = "opencode reported an error"
		}
	default:
		return StreamEvent{}, fmt.Errorf("opencode: skip event type %q", raw.Type)
	}
	return ev, nil
}

// openCodeToolInput is the tool call's input plus its status, output and
// error, in the shape toolDescription and ToolTarget read (ToolTarget drops
// the output fields again).
func openCodeToolInput(st *openCodeState) json.RawMessage {
	if st == nil {
		return nil
	}
	m := map[string]any{}
	if len(st.Input) > 0 {
		var in map[string]any
		if json.Unmarshal(st.Input, &in) == nil {
			maps.Copy(m, in)
		}
	}
	m["status"] = st.Status
	if st.Output != "" {
		m["output"] = st.Output
	}
	if st.Error != "" {
		m["error"] = st.Error
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// openCodeLimit classifies an OpenCode error: an HTTP 429 from the provider
// is a throttle unless its text names a usage quota; otherwise the shared
// text classifier decides.
func openCodeLimit(msg string, status int) *LimitSignal {
	sig := ClassifyLimitText(msg, time.Now(), time.Local)
	if sig == nil && status == 429 {
		sig = &LimitSignal{Kind: LimitKindThrottle, Source: LimitSourceText, Message: boundLimitMessage(msg)}
	}
	if sig != nil && status != 0 {
		sig.HTTPStatus = status
	}
	return sig
}

// openCodeStream is one turn's parse state: it names the first step the
// session start, and remembers the session id and the summed step cost,
// which the stream has no end-of-turn line to carry.
type openCodeStream struct {
	sessionID string
	cost      float64
	costKnown bool
}

func (s *openCodeStream) parse(line []byte) (StreamEvent, error) {
	ev, err := ParseOpenCodeLine(line)
	if err != nil {
		return ev, err
	}
	if ev.Type == EventSystem && ev.Subtype == openCodeSubtypeStepStart && s.sessionID == "" {
		ev.Subtype = "init" // logged once as "session started"
	}
	if s.sessionID == "" && ev.SessionID != "" {
		s.sessionID = ev.SessionID
	}
	if ev.CostUSD != nil {
		s.cost += *ev.CostUSD
		s.costKnown = true
		ev.CostUSD = nil // ApplyEvent reads cost from result events only
	}
	return ev, nil
}
