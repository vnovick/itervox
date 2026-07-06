package agent

import "strings"

// formatFailureText assembles the FailureText emitted by both Claude and
// Codex runners when the agent process fails. Combines the agent-reported
// failure message, the trimmed stderr buffer, and the wait error into a
// single pipe-separated string so the dashboard / TUI can show all
// available diagnostic context. T-53 (gaps_280426 04.G-07).
//
// Rules (preserve the prior runner-specific behavior):
//   - The agent-reported failure (parsed from stream-json) goes first.
//   - Trimmed stderr is included next when non-empty.
//   - The exit-status error from cmd.Wait is appended ONLY when no
//     higher-quality diagnostic is available (no parsed failure, no stderr).
func formatFailureText(agentFailure, rawStderr string, waitErr error) string {
	stderr := strings.TrimSpace(rawStderr)
	parts := make([]string, 0, 3)
	if agentFailure != "" {
		parts = append(parts, agentFailure)
	}
	if stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if waitErr != nil && agentFailure == "" && stderr == "" {
		parts = append(parts, "exit: "+waitErr.Error())
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}
