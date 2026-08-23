package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

const dependencyTransitionReasonBlockersResolved = "blockers_resolved"

func auditIssueDependencies(state *State, issue domain.Issue, now time.Time) DependencyAuditEntry {
	if state.DependencyAudit == nil {
		state.DependencyAudit = make(map[string]*DependencyAuditEntry)
	}
	key := dependencyAuditKey(issue)
	prev := state.DependencyAudit[key]

	unresolved := unresolvedBlockers(issue, *state)
	next := DependencyAuditEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		IssueState:         issue.State,
		Status:             dependencyStatusForIssue(issue, *state),
		Sources:            dependencySources(issue),
		BlockedBy:          copyBlockerRefs(issue.BlockedBy),
		UnresolvedBlockers: copyBlockerRefs(unresolved),
		ResolvedBlockers:   resolvedBlockers(issue, unresolved),
		LastAuditedAt:      now,
	}

	if prev != nil {
		next.WasBlocked = prev.WasBlocked
		next.FirstBlockedAt = prev.FirstBlockedAt
		next.UnblockedAt = prev.UnblockedAt
		next.LastTransitionVersion = prev.LastTransitionVersion
		next.LastTransitionReason = prev.LastTransitionReason
		// Refresh bookkeeping is owned by the off-loop refresh path, not by
		// the audit. The audit runs for every candidate issue every tick
		// (event_loop.go:118); without these three lines it would wipe the
		// throttle timestamp once per tick and the refresh interval would
		// never apply. InFlight in particular must survive, or a row whose
		// fetch is still outstanding becomes eligible for a second batch.
		next.InFlight = prev.InFlight
		next.ConsecutiveFailures = prev.ConsecutiveFailures
		next.LastRefreshAttemptAt = prev.LastRefreshAttemptAt
	}

	// AUTO-1: only a known Blocked status arms the WasBlocked latch. Unknown is
	// the dispatch-time fail-safe (a transient tracker outage flips blocker
	// states to nil), but it is NOT evidence that the issue was ever genuinely
	// blocked, so it must not arm the blockers_resolved transition. The audit
	// Status for unknown remains DependencyAuditUnknown (dispatch behaviour is
	// unchanged); only the latch that gates the transition changes here.
	if next.Status == DependencyAuditBlocked {
		next.WasBlocked = true
		if next.FirstBlockedAt.IsZero() {
			next.FirstBlockedAt = now
		}
	}

	fired := issueHasResolvedBlockersTransition(prev, next)
	if fired {
		state.DependencyTransitionSeq++
		next.UnblockedAt = now
		next.LastTransitionVersion = state.DependencyTransitionSeq
		next.LastTransitionReason = dependencyTransitionReasonBlockersResolved
	}

	stored := next
	if fired {
		// AUTO-1: disarm the persisted WasBlocked latch once the transition has
		// been consumed so a later transient-outage flap
		// (unblocked -> unknown -> unblocked) cannot re-fire blockers_resolved.
		// The returned entry keeps WasBlocked=true so this pass's callers still
		// observe the genuine blocked->unblocked history; only the copy that
		// becomes `prev` on the next audit is disarmed.
		stored.WasBlocked = false
	}
	state.DependencyAudit[key] = &stored
	return next
}

func dependencyAuditKey(issue domain.Issue) string {
	if issue.ID != "" {
		return issue.ID
	}
	return issue.Identifier
}

func dependencyStatusForIssue(issue domain.Issue, state State) DependencyAuditStatus {
	if len(issue.BlockedBy) == 0 {
		return DependencyAuditUnblocked
	}
	unresolved := unresolvedBlockers(issue, state)
	if len(unresolved) == 0 {
		return DependencyAuditUnblocked
	}
	for _, blocker := range unresolved {
		if blocker.State == nil {
			return DependencyAuditUnknown
		}
	}
	return DependencyAuditBlocked
}

func unresolvedBlockers(issue domain.Issue, state State) []domain.BlockerRef {
	if len(issue.BlockedBy) == 0 {
		return nil
	}
	out := make([]domain.BlockerRef, 0, len(issue.BlockedBy))
	for _, blocker := range issue.BlockedBy {
		if blockerResolvedForDispatch(blocker, state) {
			continue
		}
		out = append(out, blocker)
	}
	return out
}

func resolvedBlockers(issue domain.Issue, unresolved []domain.BlockerRef) []domain.BlockerRef {
	if len(issue.BlockedBy) == 0 {
		return nil
	}
	unresolvedKeys := make(map[string]struct{}, len(unresolved))
	for _, blocker := range unresolved {
		unresolvedKeys[blockerKey(blocker)] = struct{}{}
	}
	out := make([]domain.BlockerRef, 0, len(issue.BlockedBy)-len(unresolved))
	for _, blocker := range issue.BlockedBy {
		if _, found := unresolvedKeys[blockerKey(blocker)]; found {
			continue
		}
		out = append(out, blocker)
	}
	return copyBlockerRefs(out)
}

func blockerResolvedForDispatch(blocker domain.BlockerRef, state State) bool {
	if blocker.State != nil && isTerminalState(*blocker.State, state) {
		return true
	}
	return false
}

func dependencySources(issue domain.Issue) []DependencyAuditSource {
	if len(issue.BlockedBy) == 0 {
		return nil
	}
	out := make([]DependencyAuditSource, 0, len(issue.BlockedBy))
	seen := make(map[DependencyAuditSource]struct{}, 2)
	for _, blocker := range issue.BlockedBy {
		source := dependencySourceForBlocker(blocker)
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	return out
}

func dependencySourceForBlocker(blocker domain.BlockerRef) DependencyAuditSource {
	if blocker.Origin == domain.BlockerOriginSubIssue {
		return DependencySourceSubIssue
	}
	if blocker.Identifier != nil && strings.HasPrefix(*blocker.Identifier, "#") {
		return DependencySourceIssueText
	}
	return DependencySourceTrackerRelation
}

func issueHasResolvedBlockersTransition(prev *DependencyAuditEntry, next DependencyAuditEntry) bool {
	if prev == nil {
		return false
	}
	if next.Status != DependencyAuditUnblocked {
		return false
	}
	if prev.Status == DependencyAuditUnblocked {
		return false
	}
	// AUTO-1: gate the transition solely on the WasBlocked latch, which is now
	// armed only by a known Blocked status. The prior fallback clauses
	// (len(prev.BlockedBy) > 0 || len(prev.UnresolvedBlockers) > 0) fired for any
	// prev that merely had blocker rows — including an Unknown outage state whose
	// blockers were never genuinely blocking — which produced spurious/duplicate
	// blockers_resolved dispatches on tracker-outage flaps.
	return prev.WasBlocked
}

func auditFetchedIssueDependencies(state *State, issue domain.Issue, now time.Time) DependencyAuditEntry {
	recordObservedIssueState(state, issue, now)
	entry := auditIssueDependencies(state, issue, now)
	updateAutomationQueueIssueFromDependencyAudit(state, issue, entry, now)
	return entry
}

func (o *Orchestrator) auditFetchedIssueDependenciesAndDispatch(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
) DependencyAuditEntry {
	beforeSeq := state.DependencyTransitionSeq
	entry := auditFetchedIssueDependencies(state, issue, now)
	if entry.LastTransitionVersion > beforeSeq && entry.Status == DependencyAuditUnblocked {
		o.dispatchMatchingBlockersResolvedAutomations(ctx, state, issue, entry, now)
	}
	return entry
}

// blockersResolvedQueueIdentifiers returns the set of issue identifiers and IDs
// referenced by any blockers_resolved-shaped automation queue entry. Used by
// the dependency-audit refresh path to prioritise the rows whose freshness
// matters most: queued automations cannot fire until the audit row reflects
// the post-unblock state. v0.2.0 audit P0-3.
func blockersResolvedQueueIdentifiers(state *State) map[string]struct{} {
	if len(state.AutomationQueue) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(state.AutomationQueue))
	for _, entry := range state.AutomationQueue {
		if entry == nil {
			continue
		}
		if entry.TriggerType != config.AutomationTriggerBlockersResolved &&
			entry.Trigger.Type != config.AutomationTriggerBlockersResolved {
			continue
		}
		if entry.Issue.Identifier != "" {
			out[entry.Issue.Identifier] = struct{}{}
		}
		if entry.Issue.ID != "" {
			out[entry.Issue.ID] = struct{}{}
		}
	}
	return out
}

func deduplicateStringsFold(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func updateAutomationQueueIssueFromDependencyAudit(state *State, issue domain.Issue, audit DependencyAuditEntry, now time.Time) {
	if len(state.AutomationQueue) == 0 {
		return
	}
	for _, queued := range state.AutomationQueue {
		if queued == nil || !sameIssueIdentity(queued.Issue, issue) {
			continue
		}
		queued.Issue = issue
		if queued.Reason == AutomationQueueReasonBlockedBy && audit.Status == DependencyAuditUnblocked {
			queued.Status = AutomationQueueQueued
			queued.Reason = AutomationQueueReasonReady
			queued.ReasonDetail = ""
			queued.LastAttemptAt = now
		}
	}
}

func sameIssueIdentity(a, b domain.Issue) bool {
	if a.ID != "" && b.ID != "" && a.ID == b.ID {
		return true
	}
	return a.Identifier != "" && b.Identifier != "" && a.Identifier == b.Identifier
}

func firstUnresolvedBlocker(issue domain.Issue, state State) (domain.BlockerRef, bool) {
	unresolved := unresolvedBlockers(issue, state)
	if len(unresolved) == 0 {
		return domain.BlockerRef{}, false
	}
	return unresolved[0], true
}

func blockerIdentifier(blocker domain.BlockerRef) string {
	if blocker.Identifier != nil {
		return *blocker.Identifier
	}
	return ""
}

func blockerKey(blocker domain.BlockerRef) string {
	if blocker.ID != nil && *blocker.ID != "" {
		return "id:" + *blocker.ID
	}
	if blocker.Identifier != nil && *blocker.Identifier != "" {
		return "identifier:" + *blocker.Identifier
	}
	if blocker.URL != nil && *blocker.URL != "" {
		return "url:" + *blocker.URL
	}
	return "unknown"
}
