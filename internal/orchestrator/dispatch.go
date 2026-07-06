package orchestrator

import (
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// IneligibleReason returns a short string explaining why an issue cannot be
// dispatched by the normal reconcile loop, or "" if it is eligible. Useful for
// diagnostic logging.
//
// Normal dispatch requires the issue to be in one of the configured active
// states; automation dispatch deliberately targets non-active states too (e.g.
// issue_moved_to_backlog) — callers in that path must use
// ineligibleReasonForAutomation instead.
func IneligibleReason(issue domain.Issue, state State, cfg *config.Config) string {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return "missing_fields"
	}
	if !isActiveState(issue.State, state) {
		return "not_active_state"
	}
	return ineligibleReasonShared(issue, state, cfg, false)
}

// IneligibleReasonForAutomation applies the same dispatch guards as
// IneligibleReason EXCEPT the isActiveState gate. This is the correct check
// for EventDispatchAutomation, whose trigger types include issue_moved_to_backlog
// and arbitrary issue_entered_state targets that may be outside ActiveStates.
// isTerminalState is still enforced — a terminal issue should never be the
// target of an automation worker.
//
// This is the canonical automation-dispatch eligibility check. Both the
// automation watcher pre-filter (cmd/itervox/automations.go) and the event
// loop's TOCTOU re-check (event_loop.go) call this so they cannot drift.
// Adding a new dispatch guard? Add it once here.
func IneligibleReasonForAutomation(issue domain.Issue, state State, cfg *config.Config) string {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return "missing_fields"
	}
	return ineligibleReasonShared(issue, state, cfg, true)
}

// ineligibleReasonForAutomation is the lowercase alias preserved for any
// internal-package callers that haven't migrated to the exported name yet.
// Deprecated: use IneligibleReasonForAutomation.
func ineligibleReasonForAutomation(issue domain.Issue, state State, cfg *config.Config) string {
	return IneligibleReasonForAutomation(issue, state, cfg)
}

// ineligibleReasonShared holds every guard common to reconcile dispatch and
// automation dispatch. Callers layer their own pre-conditions (active-state or
// not) on top.
func ineligibleReasonShared(issue domain.Issue, state State, cfg *config.Config, allowInputRequired bool) string {
	if isTerminalState(issue.State, state) {
		return "terminal_state"
	}
	if _, paused := state.PausedIdentifiers[issue.Identifier]; paused {
		return "paused"
	}
	if _, discarding := state.DiscardingIdentifiers[issue.Identifier]; discarding {
		return "discarding"
	}
	if _, inputReq := state.InputRequiredIssues[issue.Identifier]; inputReq && !allowInputRequired {
		return "input_required"
	}
	if _, pendingResume := state.PendingInputResumes[issue.Identifier]; pendingResume {
		return "pending_input_resume"
	}
	if _, running := state.Running[issue.ID]; running {
		return "already_running"
	}
	if _, claimed := state.Claimed[issue.ID]; claimed {
		return "claimed"
	}
	if AvailableSlots(state) <= 0 {
		return "no_slots"
	}
	if pauseState, paused := pausedByAnyInState(state); paused {
		return "paused_by_state:" + pauseState
	}
	stateKey := strings.ToLower(issue.State)
	if limit, ok := cfg.Agent.MaxConcurrentAgentsByState[stateKey]; ok {
		if countRunningInState(state, issue.State) >= limit {
			return "per_state_limit"
		}
	}
	if blocker, blocked := firstUnresolvedBlocker(issue, state); blocked {
		return "blocked_by:" + blockerIdentifier(blocker)
	}
	return ""
}

// IsEligible returns true when an issue passes all dispatch eligibility checks.
func IsEligible(issue domain.Issue, state State, cfg *config.Config) bool {
	return IneligibleReason(issue, state, cfg) == ""
}

// SortForDispatch sorts issues: priority ASC (nil last), then created_at oldest first,
// then identifier lexicographic.
func SortForDispatch(issues []domain.Issue) []domain.Issue {
	out := make([]domain.Issue, len(issues))
	copy(out, issues)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Priority == nil && b.Priority == nil:
			// fall through
		case a.Priority == nil:
			return false
		case b.Priority == nil:
			return true
		case *a.Priority != *b.Priority:
			return *a.Priority < *b.Priority
		}
		switch {
		case a.CreatedAt == nil && b.CreatedAt == nil:
			// fall through
		case a.CreatedAt == nil:
			return false
		case b.CreatedAt == nil:
			return true
		case !a.CreatedAt.Equal(*b.CreatedAt):
			return a.CreatedAt.Before(*b.CreatedAt)
		}
		return a.Identifier < b.Identifier
	})
	return out
}

// AvailableSlots returns how many more agents can be dispatched globally.
// It reads state.MaxConcurrentAgents (snapshotted from cfg at the start of
// each tick under cfgMu) rather than cfg directly, so the event loop can call
// it lock-free throughout a tick.
func AvailableSlots(state State) int {
	n := state.MaxConcurrentAgents - len(state.Running)
	if n < 0 {
		return 0
	}
	return n
}

func isActiveState(s string, state State) bool {
	for _, a := range state.ActiveStates {
		if strings.EqualFold(s, a) {
			return true
		}
	}
	return false
}

func isTerminalState(s string, state State) bool {
	for _, t := range state.TerminalStates {
		if strings.EqualFold(s, t) {
			return true
		}
	}
	return false
}

func countRunningInState(state State, issueState string) int {
	count := 0
	for _, entry := range state.Running {
		if strings.EqualFold(entry.Issue.State, issueState) {
			count++
		}
	}
	return count
}
