package agent

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/logging"
)

// toolOutputKeys are the input fields that carry what a tool produced
// rather than what it was asked to do. Codex folds a command's output into
// its command_execution item ("output"); other shapes use the rest. They
// are dropped before a call is described for the run context (#215).
var toolOutputKeys = []string{
	"output", "aggregated_output", "stdout", "stderr", "result", "results",
	"content", "response", "agents_states", "exit_code", "status",
}

// ToolTarget describes a tool call's target — the file path, command,
// pattern or URL it acts on — for the run context (#215). It is the same
// description the log stream shows (toolDescription), computed from the
// call's input with every output field removed first, so no part of what
// the tool returned can reach it, and with every string field redacted
// (logging.RedactString) before it is cut. Empty when the input is not an
// object.
func ToolTarget(name string, input json.RawMessage) string {
	var m map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &m) != nil {
		return ""
	}
	for k, v := range m {
		if slices.Contains(toolOutputKeys, strings.ToLower(k)) {
			delete(m, k)
			continue
		}
		// Redact before toolDescription truncates: a secret cut in half
		// by the bound would no longer match its pattern.
		var str string
		if json.Unmarshal(v, &str) == nil {
			if red, err := json.Marshal(logging.RedactString(str)); err == nil {
				m[k] = red
			}
		}
	}
	stripped, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return toolDescription(name, stripped)
}
