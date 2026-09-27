package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UsageSnapshot holds token counts from a stream-json usage payload.
type UsageSnapshot struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

// ToolCall represents a single tool_use content block from an assistant message.
type ToolCall struct {
	Name  string
	Input json.RawMessage
}

// StreamEvent is a normalized parsed line from a supported agent CLI stream.
type StreamEvent struct {
	Type            string
	SessionID       string
	Message         string     // first text content block, if any
	TextBlocks      []string   // all text content blocks
	ToolCalls       []ToolCall // all tool_use content blocks
	ResultText      string     // content of the "result" field on result events
	Usage           UsageSnapshot
	IsError         bool
	IsInputRequired bool
	// InProgress indicates the action is still running (e.g. from item.started).
	// Callers should log it differently from a completed action.
	InProgress bool
	// Subtype is the Claude stream-json "subtype" (system init, api_retry,
	// ...). Empty for Codex events.
	Subtype string
	// Limit is a typed vendor limit signal carried by this line, nil when
	// the line carries none (CORE-050).
	Limit *LimitSignal
	// CostUSD is Claude's `total_cost_usd` from a result event (CORE-091):
	// a client-side ESTIMATE, cumulative for the session. nil when absent
	// (and always for Codex, which reports no cost).
	CostUSD *float64
}

type rawEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	Message   json.RawMessage `json:"message"`
	Usage     *UsageSnapshot  `json:"usage"`

	// CORE-050 limit signals (see limit_signal.go for the ground truth).
	RateLimitInfo json.RawMessage `json:"rate_limit_info"` // rate_limit_event
	// flexNumber: a string or float here must never fail the whole line
	// (M3-close V2 — readLines would skip it, dropping a result event).
	RetryDelayMs   flexNumber      `json:"retry_delay_ms"`   // system/api_retry
	ErrorStatus    flexNumber      `json:"error_status"`     // system/api_retry
	Error          json.RawMessage `json:"error"`            // system/api_retry category
	APIErrorStatus flexNumber      `json:"api_error_status"` // result
	TotalCostUSD   flexNumber      `json:"total_cost_usd"`   // result (CORE-091)
}

// ParseLine parses a single newline-terminated (or bare) JSON line from
// claude --output-format stream-json stdout. Returns an error for non-JSON input.
func ParseLine(line []byte) (StreamEvent, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return StreamEvent{}, fmt.Errorf("agent: empty line")
	}

	var raw rawEvent
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return StreamEvent{}, fmt.Errorf("agent: parse line: %w", err)
	}

	ev := StreamEvent{
		Type:      raw.Type,
		Subtype:   raw.Subtype,
		SessionID: raw.SessionID,
	}

	switch raw.Type {
	case "system":
		// session_id populated above. api_retry is advisory (CORE-050): the
		// CLI emits it before retrying, so it never ends the turn itself.
		if raw.Subtype == "api_retry" {
			ev.Limit = parseAPIRetry(raw)
		}

	case "rate_limit_event":
		ev.Limit = parseRateLimitEvent([]byte(trimmed), raw.RateLimitInfo)

	case "assistant":
		if raw.Usage != nil {
			ev.Usage = *raw.Usage
		}
		if raw.Message != nil {
			var msg struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
				// Claude CLI stream-json puts usage inside the message object.
				Usage *UsageSnapshot `json:"usage"`
			}
			if err := json.Unmarshal(raw.Message, &msg); err == nil {
				if msg.Usage != nil {
					ev.Usage = *msg.Usage
				}
				for _, block := range msg.Content {
					switch block.Type {
					case "text":
						if block.Text != "" {
							ev.TextBlocks = append(ev.TextBlocks, block.Text)
						}
					case "tool_use":
						ev.ToolCalls = append(ev.ToolCalls, ToolCall{
							Name:  block.Name,
							Input: block.Input,
						})
					}
				}
				if len(ev.TextBlocks) > 0 {
					ev.Message = ev.TextBlocks[0]
				}
			}
		}

	case "result":
		ev.IsError = raw.IsError || raw.Subtype == "error"
		ev.ResultText = raw.Result
		ev.CostUSD = raw.TotalCostUSD.ptr()
		ev.Limit = parseResultLimit(raw, ev.IsError, time.Now())
		if ev.IsError && !ev.Limit.Terminal() {
			ev.IsInputRequired = isInputRequiredMsg(raw.Result)
		}
	}

	return ev, nil
}

// pendingHumanAnswerPhrases state that the agent is waiting for a human's
// answer. Neither CLI emits a structured input-request event; these are
// matched in the text of an error result event (CORE-164). Each phrase is
// addressed to a person ("your input", "human turn"); see isInputRequiredMsg.
var pendingHumanAnswerPhrases = []string{
	"human turn",
	"needs your input",
	"need your input",
	"waiting for your input",
	"waiting for your answer",
	"awaiting your input",
	"waiting for user input",
}

// cliConfigErrorMarkers name a non-interactive session, an approval or
// sandbox policy, or a permission denial. Such an error is a configuration
// problem that no reply to a tracker comment can fix, so it wins over a
// pending-answer phrase and the turn stays Failed (retried). Taken from the
// real claude/codex error strings in input_required_msg_test.go (CORE-166).
var cliConfigErrorMarkers = []string{
	"non-interactive",
	"not an interactive",
	"not a tty",
	"not a terminal",
	"raw mode",
	"approval policy",
	"approval_policy",
	"ask-for-approval",
	"not supported in exec mode",
	"cannot ask",
	"nothing on this machine",
	"permission denied",
}

// isInputRequiredMsg returns true when an error message says the agent is
// blocked waiting for a human answer. Shared by all backend parsers.
//
// CORE-166: once the worker let InputRequired win over Failed (CORE-164),
// the old broad terms ("approval", "interactive", "user input", "requires
// approval", "pending approval", "confirmation required", "waiting for
// input") went live and matched real CLI configuration errors ("MCP tool
// call requires approval, but approval policy is never", "This session is
// non-interactive, …", "confirmation required, and this session cannot
// ask"), parking the issue on a question nobody can answer instead of
// retrying it.
func isInputRequiredMsg(msg string) bool {
	lower := strings.ToLower(msg)
	if containsAny(lower, cliConfigErrorMarkers) {
		return false
	}
	// A usage/rate limit is never a question for a human either: parking it
	// as input-required would wait on a reply that cannot lift the limit
	// (CORE-050/051). Limit wording wins over a pending-answer phrase.
	if limitKindOfText(normalizeLimitText(msg)) != "" {
		return false
	}
	return containsAny(lower, pendingHumanAnswerPhrases)
}

// InputRequiredSentinel is the literal token agents are instructed to emit
// when they need human input before continuing. Chosen as an HTML comment so
// it renders invisibly in tracker comments (Linear/GitHub markdown) while
// remaining trivially detectable. The token is case-sensitive.
const InputRequiredSentinel = "<!-- itervox:needs-input -->"

// IsSentinelInputRequired returns true when the agent's output contains the
// reliable opt-in sentinel. Prefer this when an explicit signal is available —
// it has no false positives and no locale dependence.
//
// Sentinels inside fenced code blocks (``` ... ``` or ~~~ ... ~~~) are NOT
// treated as triggers: agents frequently echo example sentinels inside docs
// or example fences, and triggering on those would block runs spuriously.
// Fences toggle the in-fence flag; nested or language-tagged opens (```md)
// are handled by the prefix match.
func IsSentinelInputRequired(text string) bool {
	if len(text) == 0 {
		return false
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	inFence := false
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && trimmed == InputRequiredSentinel {
			return true
		}
	}
	return false
}
