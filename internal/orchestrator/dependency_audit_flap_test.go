package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AUTO-1a: a genuine blocked->unblocked transition fires blockers_resolved
// exactly once. A subsequent transient tracker outage that flaps the blocker
// state (terminal -> nil/unknown -> terminal) must NOT re-fire the transition.
//
// Before the fix the WasBlocked latch was re-armed on the Unknown pass and
// issueHasResolvedBlockersTransition fired again on the return to terminal, so
// DependencyTransitionSeq/LastTransitionVersion incremented a second time and a
// duplicate blockers_resolved automation was dispatched. See v0.2.0 audit
// finding AUTO-1.
func TestOutageFlapDoesNotRefireBlockersResolved(t *testing.T) {
	state := NewState(dependencyAuditConfig())

	blockerInProgress := blockerRef("ENG-0", strPtr("In Progress"))
	blockerDone := blockerRef("ENG-0", strPtr("Done"))
	blockerNil := blockerRef("ENG-0", nil) // D4-fix transient-outage shape
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	e0 := auditIssueDependencies(&state, dependencyIssue(blockerInProgress), now)
	require.Equal(t, DependencyAuditBlocked, e0.Status)

	e1 := auditIssueDependencies(&state, dependencyIssue(blockerDone), now.Add(1*time.Minute))
	require.Equal(t, DependencyAuditUnblocked, e1.Status)
	require.Equal(t, int64(1), e1.LastTransitionVersion, "first legit fire")

	e2 := auditIssueDependencies(&state, dependencyIssue(blockerNil), now.Add(2*time.Minute))
	require.Equal(t, DependencyAuditUnknown, e2.Status)

	e3 := auditIssueDependencies(&state, dependencyIssue(blockerDone), now.Add(3*time.Minute))
	require.Equal(t, DependencyAuditUnblocked, e3.Status)
	require.Equal(t, int64(1), e3.LastTransitionVersion,
		"outage flap must NOT re-fire blockers_resolved (else duplicate automation dispatch)")
	require.Equal(t, int64(1), state.DependencyTransitionSeq,
		"transition sequence must advance exactly once across the flap")
}

// AUTO-1b: an issue whose blockers were ALWAYS terminal (never genuinely
// blocked in this daemon's lifetime) must never fire blockers_resolved after a
// transient outage flap (terminal -> nil/unknown -> terminal).
//
// Before the fix the Unknown pass wrongly set WasBlocked=true, so the return to
// terminal was misread as a blocked->unblocked transition and fired a spurious
// automation. See v0.2.0 audit finding AUTO-1.
func TestNeverBlockedIssueDoesNotFireAfterOutage(t *testing.T) {
	state := NewState(dependencyAuditConfig())

	blockerDone := blockerRef("ENG-0", strPtr("Done"))
	blockerNil := blockerRef("ENG-0", nil)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	e0 := auditIssueDependencies(&state, dependencyIssue(blockerDone), now)
	require.Equal(t, DependencyAuditUnblocked, e0.Status)
	require.Equal(t, int64(0), e0.LastTransitionVersion)

	e1 := auditIssueDependencies(&state, dependencyIssue(blockerNil), now.Add(1*time.Minute))
	require.Equal(t, DependencyAuditUnknown, e1.Status)

	e2 := auditIssueDependencies(&state, dependencyIssue(blockerDone), now.Add(2*time.Minute))
	require.Equal(t, int64(0), e2.LastTransitionVersion,
		"never-blocked issue must not fire blockers_resolved after an outage flap")
	require.Equal(t, int64(0), state.DependencyTransitionSeq)
}
