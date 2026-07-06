package orchestrator

import "strings"

// IsTransportFailure reports whether errMsg matches any configured
// transport-error pattern (substring, case-insensitive). Used by the
// orchestrator to classify retryable transport hiccups (codex "stream
// disconnected", network resets) so the issue pauses with reason
// "transport_error" instead of being marked failed. todolist4 A.4.
func IsTransportFailure(errMsg string, patterns []string) bool {
	if errMsg == "" || len(patterns) == 0 {
		return false
	}
	lower := strings.ToLower(errMsg)
	for _, pattern := range patterns {
		p := strings.TrimSpace(strings.ToLower(pattern))
		if p == "" {
			continue
		}
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// PausedReasonTransportError is the marker the orchestrator sets on
// state.PausedReason so the dashboard surfaces "paused_transport" tiles.
const PausedReasonTransportError = "transport_error"
