package agent

import (
	"encoding/json"
	"fmt"
	"strings"
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
}

type rawEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	Message   json.RawMessage `json:"message"`
	Usage     *UsageSnapshot  `json:"usage"`
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
		SessionID: raw.SessionID,
	}

	switch raw.Type {
	case "system":
		// session_id populated above

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
		if ev.IsError {
			ev.IsInputRequired = isInputRequiredMsg(raw.Result)
		}
	}

	return ev, nil
}

// isInputRequiredMsg returns true when an error message indicates the agent
// is blocked waiting for human input. Shared by all backend parsers.
func isInputRequiredMsg(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "human turn") ||
		strings.Contains(lower, "approval") ||
		strings.Contains(lower, "waiting for input") ||
		strings.Contains(lower, "requires approval") ||
		strings.Contains(lower, "pending approval") ||
		strings.Contains(lower, "interactive") ||
		strings.Contains(lower, "user input") ||
		strings.Contains(lower, "confirmation required")
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
