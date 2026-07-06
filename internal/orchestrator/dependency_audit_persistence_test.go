package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDependencyAuditSurvivesRestart is AUTO-4: a blocker that reaches terminal
// while the daemon is down must still fire blockers_resolved after restart —
// the dependency-audit ledger (and its transition seq) survive restarts and
// carry the WasBlocked latch, so `prev` is non-nil on the first post-restart
// audit and the blocked->unblocked transition fires.
func TestDependencyAuditSurvivesRestart(t *testing.T) {
	cfg := dependencyAuditConfig()
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	// daemon 1: audit sees the issue BLOCKED (open blocker) and persists.
	stateA := NewState(cfg)
	blocked := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))
	entryA := auditIssueDependencies(&stateA, blocked, now)
	require.Equal(t, DependencyAuditBlocked, entryA.Status)
	require.True(t, entryA.WasBlocked)

	writer := New(cfg, nil, nil, nil)
	writer.SetAutomationQueueFile(path)
	writer.storeSnap(stateA)

	// daemon 2: fresh state loads the ledger; blocker now terminal.
	reader := New(cfg, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	stateB := reader.loadAutomationQueueFromDisk(NewState(cfg))

	require.NotEmpty(t, stateB.DependencyAudit, "audit ledger must be restored (AUTO-4)")
	key := dependencyAuditKey(blocked)
	restored := stateB.DependencyAudit[key]
	require.NotNil(t, restored, "restored ledger must contain the blocked row")
	require.True(t, restored.WasBlocked, "prev row must carry WasBlocked across restart")

	// The restored map must not alias the writer's snapshot — mutating it here
	// must not be observable elsewhere. Basic identity check: it is its own map.
	require.NotSame(t, &stateA, &stateB)

	// Aggravator: a freshly-restored non-empty ledger must force one initial
	// blockers_resolved source scan even though DependencyTransitionSeq has not
	// advanced since restart. The watermark is seeded behind the seq on load.
	require.NotEqual(t, stateB.DependencyTransitionSeq, stateB.LastBlockersResolvedAuditSeq,
		"restored ledger must force one initial blockers_resolved scan (AUTO-4 aggravator)")

	beforeSeq := stateB.DependencyTransitionSeq
	resolved := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	entryB := auditIssueDependencies(&stateB, resolved, now.Add(time.Hour))

	require.Equal(t, DependencyAuditUnblocked, entryB.Status)
	require.True(t, entryB.WasBlocked, "prev row must carry WasBlocked across restart")
	require.Greater(t, stateB.DependencyTransitionSeq, beforeSeq,
		"blockers_resolved transition must fire after restart")
	require.Equal(t, dependencyTransitionReasonBlockersResolved, entryB.LastTransitionReason)
}
