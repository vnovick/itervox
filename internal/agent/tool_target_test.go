package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestToolTargetDropsOutputAndRedacts (#215): a tool call's target is its
// input's target field; output-bearing fields never contribute, and
// secrets are redacted before the description is cut.
func TestToolTargetDropsOutputAndRedacts(t *testing.T) {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	assert.Equal(t, "go test ./...", ToolTarget("shell", in(map[string]any{"command": "go test ./...", "output": "SECRET-OUT", "exit_code": 0})))
	assert.Equal(t, "", ToolTarget("mcp__x__y", in(map[string]any{"result": "SECRET-OUT", "content": "SECRET-OUT", "stdout": "SECRET-OUT"})))
	assert.Equal(t, "a.go", ToolTarget("Write", in(map[string]any{"file_path": "a.go", "content": "SECRET-OUT"})))
	tok := "ghp_" + strings.Repeat("A", 36)
	got := ToolTarget("Bash", in(map[string]any{"command": strings.Repeat("x", 540) + " " + tok}))
	assert.NotContains(t, got, "ghp_AAAA")
	assert.Equal(t, "", ToolTarget("Bash", json.RawMessage(`[1]`)))

	r := ApplyEvent(TurnResult{}, StreamEvent{Type: EventAssistant, InProgress: true, ToolCalls: []ToolCall{{Name: "shell", Input: in(map[string]any{"command": "make"})}}})
	assert.Equal(t, "shell", r.LastToolName)
	assert.Equal(t, 0, r.ToolCalls, "a started call is not counted")
	r = ApplyEvent(r, StreamEvent{Type: EventAssistant, ToolCalls: []ToolCall{{Name: "shell", Input: in(map[string]any{"command": "make"})}}})
	assert.Equal(t, 1, r.ToolCalls)
}
