package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

func TestIssueStatusHistoryJanitorPrunesStaleAndAbsentIdentifiers(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour

	state := &State{
		IssueStatusHistory: map[string][]IssueStatusChange{},
		PrevIssueStates:    map[string]string{},
	}

	// Build fixtures spanning 30 days. Half live (in candidate set), half stale.
	for i := range 600 {
		id := identifierForFixture(i)
		// Spread `at` values from now-30d up to now-1m so a fixed retention
		// of 7d carves a clear pruneable / kept boundary.
		age := time.Duration(30*24-(i%30)*24) * time.Hour
		state.IssueStatusHistory[id] = []IssueStatusChange{
			{Identifier: id, FromState: "Todo", ToState: "In Progress", Source: StatusSourceTrackerObserved, At: now.Add(-age)},
		}
		state.PrevIssueStates[id] = "In Progress"
	}
	require.Len(t, state.IssueStatusHistory, 600)
	require.Len(t, state.PrevIssueStates, 600)

	// Mark the first 20 identifiers as live regardless of age. They must
	// survive even when the timestamp is 30 days old.
	candidates := map[string]struct{}{}
	for i := range 20 {
		candidates[identifierForFixture(i)] = struct{}{}
	}

	// PrevIssueStates entry with NO history. Treated as "very old" — should
	// be pruned when absent from candidates.
	state.PrevIssueStates["ORPHAN-1"] = "Done"

	statusRemoved, prevRemoved := pruneIssueStatusHistory(state, candidates, now, retention)

	// Count the expected survivors: live ids (any age) + non-live ids with
	// at >= now-7d.
	expectedSurvivors := 0
	for i := range 600 {
		id := identifierForFixture(i)
		if _, live := candidates[id]; live {
			expectedSurvivors++
			continue
		}
		age := time.Duration(30*24-(i%30)*24) * time.Hour
		if age < retention {
			expectedSurvivors++
		}
	}
	assert.Len(t, state.IssueStatusHistory, expectedSurvivors,
		"IssueStatusHistory should keep live + within-retention entries only")
	assert.Equal(t, 600-expectedSurvivors, statusRemoved)

	// Every live id must survive both maps.
	for id := range candidates {
		_, gotHistory := state.IssueStatusHistory[id]
		_, gotPrev := state.PrevIssueStates[id]
		assert.True(t, gotHistory, "live id %q should keep history", id)
		assert.True(t, gotPrev, "live id %q should keep PrevIssueStates entry", id)
	}

	// The orphan entry has no history and is absent from candidates, so it
	// must be pruned.
	_, orphanPresent := state.PrevIssueStates["ORPHAN-1"]
	assert.False(t, orphanPresent, "orphan PrevIssueStates entry without history should be pruned")
	assert.GreaterOrEqual(t, prevRemoved, 1, "orphan removal must be counted")

	// PrevIssueStates and IssueStatusHistory must stay in sync: every
	// remaining history identifier must also have a PrevIssueStates entry,
	// and vice versa (excluding orphans we deliberately deleted).
	for id := range state.IssueStatusHistory {
		_, hasPrev := state.PrevIssueStates[id]
		assert.True(t, hasPrev, "history kept for %q but PrevIssueStates dropped — maps out of sync", id)
	}
}

func TestIssueStatusHistoryJanitorTreatsZeroAtAsAged(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	state := &State{
		IssueStatusHistory: map[string][]IssueStatusChange{
			"NO-AT-1": {{Identifier: "NO-AT-1", ToState: "Done"}}, // zero At
		},
		PrevIssueStates: map[string]string{"NO-AT-1": "Done"},
	}
	statusRemoved, prevRemoved := pruneIssueStatusHistory(state, nil, now, 7*24*time.Hour)
	assert.Equal(t, 1, statusRemoved)
	assert.Equal(t, 1, prevRemoved)
	assert.Empty(t, state.IssueStatusHistory)
	assert.Empty(t, state.PrevIssueStates)
}

func TestDependencyAuditJanitorPrunesTerminalAndNotQueued(t *testing.T) {
	state := &State{
		TerminalStates:  []string{"Done", "Cancelled"},
		DependencyAudit: map[string]*DependencyAuditEntry{},
		AutomationQueue: map[string]*AutomationQueueEntry{},
		Running:         map[string]*RunEntry{},
	}

	// 1. Terminal + not queued + not running → should prune.
	state.DependencyAudit["TERM-1"] = &DependencyAuditEntry{
		IssueID:    "id-term-1",
		Identifier: "TERM-1",
		IssueState: "Done",
		Status:     DependencyAuditUnblocked,
	}

	// 2. Terminal + queued → must survive (queued blockers_resolved would
	//    lose context if the row vanishes).
	state.DependencyAudit["TERM-2"] = &DependencyAuditEntry{
		IssueID:    "id-term-2",
		Identifier: "TERM-2",
		IssueState: "Done",
	}
	state.AutomationQueue["queue-key-term-2"] = &AutomationQueueEntry{
		Issue: domain.Issue{Identifier: "TERM-2", ID: "id-term-2"},
	}

	// 3. Terminal + currently running → must survive (reconcile paths still
	//    inspect the row before clearing it).
	state.DependencyAudit["TERM-3"] = &DependencyAuditEntry{
		Identifier: "TERM-3",
		IssueState: "Cancelled",
	}
	state.Running["TERM-3"] = &RunEntry{Issue: domain.Issue{Identifier: "TERM-3"}}

	// 4. Non-terminal + neither queued nor running → must survive (the
	//    janitor only targets terminal rows).
	state.DependencyAudit["LIVE-1"] = &DependencyAuditEntry{
		Identifier: "LIVE-1",
		IssueState: "In Progress",
		Status:     DependencyAuditBlocked,
	}

	// 5. Nil entry → defensive prune (shouldn't normally happen, but the
	//    janitor must self-heal corrupt rows).
	state.DependencyAudit["NIL-1"] = nil

	removed := pruneTerminalDependencyAudit(state)
	assert.Equal(t, 2, removed, "TERM-1 and NIL-1 should be removed")

	_, term1Present := state.DependencyAudit["TERM-1"]
	_, nil1Present := state.DependencyAudit["NIL-1"]
	assert.False(t, term1Present, "TERM-1 (terminal, idle) should be pruned")
	assert.False(t, nil1Present, "nil row should be pruned")

	_, term2Present := state.DependencyAudit["TERM-2"]
	_, term3Present := state.DependencyAudit["TERM-3"]
	_, live1Present := state.DependencyAudit["LIVE-1"]
	assert.True(t, term2Present, "TERM-2 (queued) must be kept")
	assert.True(t, term3Present, "TERM-3 (running) must be kept")
	assert.True(t, live1Present, "LIVE-1 (non-terminal) must be kept regardless")
}

func TestDependencyAuditJanitorMatchesQueueByIssueID(t *testing.T) {
	// The audit row may have an Identifier different from the queue entry's
	// Issue.Identifier when the queue entry references the IssueID instead.
	// The janitor must consult both keys before deleting.
	state := &State{
		TerminalStates: []string{"Done"},
		DependencyAudit: map[string]*DependencyAuditEntry{
			"key-1": {
				IssueID:    "id-1",
				Identifier: "ID-A",
				IssueState: "Done",
			},
		},
		AutomationQueue: map[string]*AutomationQueueEntry{
			"q-1": {Issue: domain.Issue{ID: "id-1"}}, // no Identifier
		},
		Running: map[string]*RunEntry{},
	}
	removed := pruneTerminalDependencyAudit(state)
	assert.Equal(t, 0, removed)
	assert.NotNil(t, state.DependencyAudit["key-1"])
}

// identifierForFixture returns a stable identifier for table tests.
func identifierForFixture(i int) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	a := letters[i%len(letters)]
	b := letters[(i/len(letters))%len(letters)]
	return string([]byte{a, b}) + "-" + itoa3(i)
}

func itoa3(i int) string {
	const digits = "0123456789"
	if i < 0 {
		i = -i
	}
	out := []byte{digits[(i/100)%10], digits[(i/10)%10], digits[i%10]}
	return string(out)
}
