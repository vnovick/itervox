package orchestrator

import "strings"

// normalizePauseStates lowercases, trims, and dedups the configured pause
// states for case-insensitive comparison at dispatch time. Empty inputs return
// nil so the dispatch guard can short-circuit cheaply.
func normalizePauseStates(states []string) []string {
	if len(states) == 0 {
		return nil
	}
	out := make([]string, 0, len(states))
	seen := make(map[string]struct{}, len(states))
	for _, s := range states {
		normalized := strings.ToLower(strings.TrimSpace(s))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pausedByAnyInState reports whether any currently-tracked issue (running OR
// the broader PrevIssueStates snapshot of last poll) sits in one of the
// configured pause states. The boolean second return indicates "guard fired";
// the string is the matching state name so the dispatch caller can include it
// in the ineligibility reason ("paused_by_state:<state>"). The check uses the
// State snapshot — not cfg — so the dispatch path stays lock-free per the
// orchestrator's single-goroutine invariant.
func pausedByAnyInState(state State) (string, bool) {
	if len(state.PauseDispatchWhenAnyInState) == 0 {
		return "", false
	}
	pauseSet := make(map[string]struct{}, len(state.PauseDispatchWhenAnyInState))
	for _, s := range state.PauseDispatchWhenAnyInState {
		pauseSet[s] = struct{}{}
	}
	// Running issues are the authoritative current-state source for issues the
	// daemon owns. PrevIssueStates extends coverage to issues that were last
	// observed at poll time but are not running (the canonical "merge in
	// progress, no agent assigned yet" case).
	for _, entry := range state.Running {
		if entry == nil {
			continue
		}
		if _, ok := pauseSet[strings.ToLower(entry.Issue.State)]; ok {
			return entry.Issue.State, true
		}
	}
	for _, s := range state.PrevIssueStates {
		if _, ok := pauseSet[strings.ToLower(s)]; ok {
			return s, true
		}
	}
	return "", false
}
