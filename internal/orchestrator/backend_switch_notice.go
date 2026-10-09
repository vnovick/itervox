package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// backendSwitchNoticeHeading is the heading of the daemon-owned prompt block
// a run gets when Itervox moved the issue off another backend (CORE-101).
const backendSwitchNoticeHeading = "## Backend Switch Notice"

// BackendSwitchNotice describes why this run is on a different backend or
// profile than the one the issue last ran on (CORE-101). Built on the event
// loop at spawn time and handed to the worker by value; the worker never
// reads State.
type BackendSwitchNotice struct {
	PreviousBackend string
	PreviousProfile string
	Backend         string
	Profile         string
	Reason          string
	// LimitResetsAt is the previous backend's vendor-published reset (from
	// its breaker, CORE-050/053); zero when unknown.
	LimitResetsAt time.Time
}

// backendSwitchNotice returns the notice for a run of identifier on
// (backend, profile), or nil for an ordinary run. Sources, in order:
//   - a rate_limited automation dispatch: its trigger names the failed
//     backend and profile;
//   - the persisted switch provenance State.AutoSwitchInfo (rate_limited
//     automation or backend_fallback, CORE-055), while the override it
//     describes is still the backend this run uses.
//
// INVARIANT: event loop only.
func (o *Orchestrator) backendSwitchNotice(state *State, identifier, backend, profile string, automation *AutomationDispatch) *BackendSwitchNotice {
	var n *BackendSwitchNotice
	fromKey := ""
	if automation != nil && automation.Trigger.Type == config.AutomationTriggerRateLimited {
		n = &BackendSwitchNotice{
			PreviousBackend: automation.Trigger.FailedBackend,
			PreviousProfile: automation.Trigger.FailedProfile,
			Reason:          fmt.Sprintf("rate_limited automation %q", automation.AutomationID),
		}
	} else if rec, ok := state.AutoSwitchInfo[identifier]; ok && (rec.ToBackend == "" || rec.ToBackend == backend) {
		n = &BackendSwitchNotice{PreviousBackend: rec.FromBackend, PreviousProfile: rec.FromProfile, Reason: rec.Reason}
		fromKey = rec.FromKey
	}
	if n == nil {
		return nil
	}
	n.Backend, n.Profile = backend, profile
	n.LimitResetsAt = previousBackendReset(state, n.PreviousBackend, fromKey)
	return n
}

// previousBackendReset is the vendor-published reset of the breaker the
// issue left: fromKey when known, else the latest reset among that
// backend's breakers on any host.
func previousBackendReset(state *State, backend, fromKey string) time.Time {
	var reset time.Time
	for key, e := range state.BackendHealth {
		if !e.ResetKnown || e.LimitedUntil.IsZero() {
			continue
		}
		if fromKey != "" && key != fromKey {
			continue
		}
		if fromKey == "" && e.Backend != backend {
			continue
		}
		if e.LimitedUntil.After(reset) {
			reset = e.LimitedUntil
		}
	}
	return reset
}

func labelOrDefault(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}

// buildBackendSwitchNoticeBlock renders the notice. The prompt assembly
// puts it after the operator-reply envelope and the prior handoffs and
// before any profile block, so an instructions_file cannot drop it.
func buildBackendSwitchNoticeBlock(n *BackendSwitchNotice) string {
	if n == nil {
		return ""
	}
	reset := "not published"
	if !n.LimitResetsAt.IsZero() {
		reset = n.LimitResetsAt.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{
		backendSwitchNoticeHeading,
		"",
		fmt.Sprintf("Itervox moved this issue off backend `%s` (profile `%s`): %s.",
			labelOrDefault(n.PreviousBackend), labelOrDefault(n.PreviousProfile), n.Reason),
		fmt.Sprintf("This run uses backend `%s` with profile `%s`. The previous backend's limit resets at: %s.",
			labelOrDefault(n.Backend), labelOrDefault(n.Profile), reset),
		"",
		"The previous agent session is not resumed. Continue from the workspace and the prior handoffs above; do not redo finished work.",
	}, "\n")
}

// runBindings is the Liquid `run` object (CORE-101): the run context plus
// the switch provenance, empty strings on an ordinary run, and the branch a
// pull request from this run should target (#73).
func runBindings(timestamp, handoffPath, prBaseBranch string, n *BackendSwitchNotice) map[string]any {
	run := map[string]any{
		"timestamp":        timestamp,
		"handoff_path":     handoffPath,
		"pr_base_branch":   prBaseBranch,
		"previous_backend": "",
		"previous_profile": "",
		"switch_reason":    "",
		"limit_resets_at":  "",
	}
	if n != nil {
		run["previous_backend"] = n.PreviousBackend
		run["previous_profile"] = n.PreviousProfile
		run["switch_reason"] = n.Reason
		if !n.LimitResetsAt.IsZero() {
			run["limit_resets_at"] = n.LimitResetsAt.UTC().Format(time.RFC3339)
		}
	}
	return map[string]any{"run": run}
}
