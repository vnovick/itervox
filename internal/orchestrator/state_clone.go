package orchestrator

import (
	"maps"
	"time"
)

// Clone returns a copy of s that shares no mutable memory with it: every map,
// slice and struct pointer reachable from State is copied, so the event loop
// can keep mutating s (in place, without a lock) while another goroutine reads
// the clone. It is the single clone used by storeSnap (event loop → lastSnap)
// and Snapshot (lastSnap → caller). CORE-034.
//
// Deliberate exceptions, enforced by TestStateCloneDeepCopiesAllReferenceFields
// through cloneAllowlist in state_clone_test.go:
//   - RunEntry.WorkerCancel is cleared, not copied: a snapshot reader must
//     never be able to cancel a live worker.
//   - Pointer-to-scalar fields (*string, *int, *time.Time on domain.Issue,
//     domain.BlockerRef, domain.Comment, RunEntry.LastEventAt,
//     RunEntry.RetryAttempt, RetryEntry.Error) are shared: they are immutable
//     values that code replaces wholesale and never writes through.
//
// A new map, slice or pointer field anywhere under State must be handled here
// (or allowlisted with a reason) or the reflection guard fails.
func (s State) Clone() State {
	c := s
	c.ActiveStates = cloneSlice(s.ActiveStates)
	c.TerminalStates = cloneSlice(s.TerminalStates)
	c.PauseDispatchWhenAnyInState = cloneSlice(s.PauseDispatchWhenAnyInState)
	c.Running = copyRunningMap(s.Running)
	c.Claimed = maps.Clone(s.Claimed)
	c.RetryAttempts = copyRetryMap(s.RetryAttempts)
	c.PausedIdentifiers = maps.Clone(s.PausedIdentifiers)
	c.PauseReasons = maps.Clone(s.PauseReasons)
	c.PausedSessions = copyPtrValueMap(s.PausedSessions)
	c.IssueProfiles = maps.Clone(s.IssueProfiles)
	c.IssueBackends = maps.Clone(s.IssueBackends)
	c.AutoSwitchedIdentifiers = maps.Clone(s.AutoSwitchedIdentifiers)
	c.AutoSwitchedAt = maps.Clone(s.AutoSwitchedAt)
	c.SwitchHistory = copySwitchHistory(s.SwitchHistory)
	c.RateLimitCooldowns = maps.Clone(s.RateLimitCooldowns)
	c.RateLimitCapCommentUntil = maps.Clone(s.RateLimitCapCommentUntil)
	c.AutoSwitchInfo = maps.Clone(s.AutoSwitchInfo)
	c.BackendHealth = maps.Clone(s.BackendHealth)
	c.BackendLimitedHolds = maps.Clone(s.BackendLimitedHolds)
	c.ForceReanalyze = maps.Clone(s.ForceReanalyze)
	c.PrevActiveIdentifiers = maps.Clone(s.PrevActiveIdentifiers)
	c.PrevIssueStates = maps.Clone(s.PrevIssueStates)
	c.IssueStatusHistory = copyIssueStatusHistoryMap(s.IssueStatusHistory)
	c.DiscardingIdentifiers = maps.Clone(s.DiscardingIdentifiers)
	c.InputRequiredIssues = copyInputRequiredMap(s.InputRequiredIssues)
	c.PendingInputResumes = copyPendingInputResumeMap(s.PendingInputResumes)
	c.AutomationQueue = copyAutomationQueueMap(s.AutomationQueue)
	c.AutomationQueueOrder = append([]string(nil), s.AutomationQueueOrder...)
	c.ReviewVerdicts = copyReviewVerdictsMap(s.ReviewVerdicts)
	c.ReviewChainIndex = maps.Clone(s.ReviewChainIndex)
	c.ReviewOutcomes = maps.Clone(s.ReviewOutcomes)
	c.DependencyAudit = copyDependencyAuditMap(s.DependencyAudit)
	c.PROpenedDispatched = maps.Clone(s.PROpenedDispatched)
	c.PRMergedDispatched = maps.Clone(s.PRMergedDispatched)
	c.InferredDeps = copyInferredDepsMap(s.InferredDeps)
	c.DepsOverrides = maps.Clone(s.DepsOverrides)
	c.StackUnavailable = maps.Clone(s.StackUnavailable)
	c.PendingReviews = maps.Clone(s.PendingReviews) // value type, no reference fields
	c.DependencyCycles = copyDependencyCycles(s.DependencyCycles)
	c.DependencyAttention = copyDependencyAttention(s.DependencyAttention)
	c.CandidateSeen = append([]CandidateSeenRow(nil), s.CandidateSeen...)
	c.OutboxSyncing = maps.Clone(s.OutboxSyncing)
	c.RecentFailures = cloneSlice(s.RecentFailures)
	c.Totals.SessionCost = maps.Clone(s.Totals.SessionCost)
	c.FailureAcks = maps.Clone(s.FailureAcks)
	return c
}

// cloneSlice copies a slice, preserving nil.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append([]T(nil), s...)
}

// copyPtrValueMap copies a map of pointers to structs that hold no reference
// fields, giving each value its own copy (maps.Clone would share them).
func copyPtrValueMap[K comparable, V any](m map[K]*V) map[K]*V {
	if m == nil {
		return nil
	}
	cp := make(map[K]*V, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		e := *v
		cp[k] = &e
	}
	return cp
}

func copyReviewVerdictsMap(m map[string][]ReviewVerdict) map[string][]ReviewVerdict {
	if m == nil {
		return nil
	}
	cp := make(map[string][]ReviewVerdict, len(m))
	for k, v := range m {
		if v == nil {
			cp[k] = nil
			continue
		}
		vs := make([]ReviewVerdict, len(v))
		for i, rv := range v {
			vs[i] = rv
			vs[i].Reasons = cloneSlice(rv.Reasons)
		}
		cp[k] = vs
	}
	return cp
}

// copySwitchHistory deep-copies the per-issue switch stamps (CORE-052): the
// []time.Time values are appended to in place by recordRateLimitSwitch.
func copySwitchHistory(m map[string][]time.Time) map[string][]time.Time {
	if m == nil {
		return nil
	}
	cp := make(map[string][]time.Time, len(m))
	for k, v := range m {
		cp[k] = cloneSlice(v)
	}
	return cp
}
