package domain

import "strings"

// IsTerminalState is the one terminal-state predicate (CORE-111), shared by
// the orchestrator's dispatch eligibility and cmd/itervox's snapshot rows,
// which used to disagree on whitespace. The contract: both sides are
// trimmed, the comparison is case-insensitive, and an empty (or all-blank)
// state is never terminal — even when the terminal list holds a blank entry.
func IsTerminalState(state string, terminal []string) bool {
	s := strings.TrimSpace(state)
	if s == "" {
		return false
	}
	for _, t := range terminal {
		if strings.EqualFold(s, strings.TrimSpace(t)) {
			return true
		}
	}
	return false
}
