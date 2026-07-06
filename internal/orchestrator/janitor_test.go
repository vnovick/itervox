package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
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

// pruneTerminalRuntimeLedgers must sweep runtime
// ledgers for issues whose last-observed tracker state is terminal, and
// must leave entries for non-terminal identifiers alone.
func TestPruneTerminalRuntimeLedgers_DropsInputRequired(t *testing.T) {
	state := &State{
		TerminalStates:       []string{"Done", "Cancelled"},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
	}
	state.InputRequiredIssues["DONE-1"] = &InputRequiredEntry{Identifier: "DONE-1"}
	state.InputRequiredIssues["LIVE-1"] = &InputRequiredEntry{Identifier: "LIVE-1"}
	terminal := map[string]struct{}{"DONE-1": {}}
	counts := pruneTerminalRuntimeLedgers(state, terminal)
	assert.Equal(t, 1, counts.InputRequired)
	_, donePresent := state.InputRequiredIssues["DONE-1"]
	_, livePresent := state.InputRequiredIssues["LIVE-1"]
	assert.False(t, donePresent, "terminal identifier must be dropped")
	assert.True(t, livePresent, "non-terminal identifier must survive")
}

func TestPruneTerminalRuntimeLedgers_DropsRetryAttempts(t *testing.T) {
	state := &State{
		TerminalStates:       []string{"Done"},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
	}
	state.RetryAttempts["DONE-1"] = &RetryEntry{Identifier: "DONE-1"}
	state.RetryAttempts["TODO-1"] = &RetryEntry{Identifier: "TODO-1"}
	counts := pruneTerminalRuntimeLedgers(state, map[string]struct{}{"DONE-1": {}})
	assert.Equal(t, 1, counts.Retry)
	assert.Nil(t, state.RetryAttempts["DONE-1"])
	assert.NotNil(t, state.RetryAttempts["TODO-1"])
}

func TestPruneTerminalRuntimeLedgers_DropsAutomationQueue(t *testing.T) {
	state := &State{
		TerminalStates: []string{"Done"},
		AutomationQueue: map[string]*AutomationQueueEntry{
			"q-done": {Issue: domain.Issue{Identifier: "DONE-1"}},
			"q-live": {Issue: domain.Issue{Identifier: "LIVE-1"}},
		},
		AutomationQueueOrder: []string{"q-done", "q-live"},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		RetryAttempts:        map[string]*RetryEntry{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
	}
	counts := pruneTerminalRuntimeLedgers(state, map[string]struct{}{"DONE-1": {}})
	assert.Equal(t, 1, counts.Queue)
	_, donePresent := state.AutomationQueue["q-done"]
	assert.False(t, donePresent, "queue entry with terminal issue must drop")
	assert.NotNil(t, state.AutomationQueue["q-live"])
	assert.Equal(t, []string{"q-live"}, state.AutomationQueueOrder,
		"queue order must shrink in lockstep with the map")
}

func TestPruneTerminalRuntimeLedgers_DropsPaused(t *testing.T) {
	state := &State{
		TerminalStates: []string{"Done"},
		PausedIdentifiers: map[string]string{
			"DONE-1": "id-done",
			"TODO-1": "id-todo",
		},
		PausedSessions: map[string]*PausedSessionInfo{
			"DONE-1":  {SessionID: "sess-1"},
			"id-done": {SessionID: "sess-1"},
		},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
	}
	counts := pruneTerminalRuntimeLedgers(state, map[string]struct{}{"DONE-1": {}})
	assert.Equal(t, 1, counts.Paused)
	_, donePresent := state.PausedIdentifiers["DONE-1"]
	assert.False(t, donePresent)
	assert.Equal(t, "id-todo", state.PausedIdentifiers["TODO-1"])
	_, sessDonePresent := state.PausedSessions["DONE-1"]
	assert.False(t, sessDonePresent, "paused session must be cleaned in lockstep")
}

// pruneAbsentTrackerIssues drops ledger entries for
// identifiers that have been absent from both the current and the previous
// tick's candidate set. Live workers (state.Running) MUST be preserved.
func TestPruneAbsentTrackerIssues_DropsAbsentFromBothTicks(t *testing.T) {
	state := &State{
		Running:              map[string]*RunEntry{},
		PrevIssueStates:      map[string]string{"LIVE-1": "In Progress"},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
	}
	state.InputRequiredIssues["DELETED-1"] = &InputRequiredEntry{Identifier: "DELETED-1"}
	state.InputRequiredIssues["LIVE-1"] = &InputRequiredEntry{Identifier: "LIVE-1"}
	state.IssueProfiles["DELETED-1"] = "reviewer"
	currentActive := map[string]struct{}{"LIVE-1": {}}
	// Prior tick's poll also lacked DELETED-1 — two consecutive absences.
	prevActive := map[string]struct{}{"LIVE-1": {}}
	counts := pruneAbsentTrackerIssues(state, currentActive, prevActive)
	assert.Equal(t, 1, counts.InputRequired)
	assert.Equal(t, 1, counts.Profile)
	_, gonePresent := state.InputRequiredIssues["DELETED-1"]
	assert.False(t, gonePresent)
	assert.NotNil(t, state.InputRequiredIssues["LIVE-1"])
}

func TestPruneAbsentTrackerIssues_RespectsTwoTickGrace(t *testing.T) {
	state := &State{
		Running:         map[string]*RunEntry{},
		PrevIssueStates: map[string]string{},
		InputRequiredIssues: map[string]*InputRequiredEntry{
			"BLIP-1": {Identifier: "BLIP-1"},
		},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
	}
	// Identifier was observed last tick (prevActive contains it) but not
	// this tick. The two-tick grace window must preserve the entry.
	prevActive := map[string]struct{}{"BLIP-1": {}}
	counts := pruneAbsentTrackerIssues(state, map[string]struct{}{}, prevActive)
	assert.Equal(t, 0, counts.InputRequired, "single-tick absence must NOT prune")
	assert.NotNil(t, state.InputRequiredIssues["BLIP-1"])
}

func TestPruneAbsentTrackerIssues_DoesNotPruneRunning(t *testing.T) {
	state := &State{
		Running: map[string]*RunEntry{
			"INFLIGHT-1": {Issue: domain.Issue{Identifier: "INFLIGHT-1"}},
		},
		PrevIssueStates: map[string]string{"SENTINEL-1": "In Progress"},
		InputRequiredIssues: map[string]*InputRequiredEntry{
			"INFLIGHT-1": {Identifier: "INFLIGHT-1"},
		},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
	}
	// Persistence-replay safety gate: need at least one prior observation.
	// Use a sentinel different from INFLIGHT-1 so the test still proves that
	// Running by itself is sufficient to preserve.
	prevActive := map[string]struct{}{"SENTINEL-1": {}}
	// Identifier absent from currentActive AND from prevActive but a worker
	// is running — must NOT prune any sibling ledger entry.
	counts := pruneAbsentTrackerIssues(state, map[string]struct{}{}, prevActive)
	assert.Equal(t, 0, counts.InputRequired, "in-flight worker identifier must keep sibling ledgers alive")
}

// gaps_11 G-2 — PrevIssueStates is observation HISTORY (kept ~7 days by
// pruneIssueStatusHistory), not tracker-presence evidence. An identifier
// whose only trace is a PrevIssueStates entry must still be pruned after two
// consecutive absent polls.
func TestPruneAbsentTrackerIssues_PrevIssueStatesIsNotPresenceEvidence(t *testing.T) {
	state := &State{
		Running:         map[string]*RunEntry{},
		PrevIssueStates: map[string]string{"GONE-1": "Todo", "LIVE-1": "Todo"},
		InputRequiredIssues: map[string]*InputRequiredEntry{
			"GONE-1": {Identifier: "GONE-1"},
		},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
	}
	currentActive := map[string]struct{}{"LIVE-1": {}}
	prevActive := map[string]struct{}{"LIVE-1": {}}
	counts := pruneAbsentTrackerIssues(state, currentActive, prevActive)
	assert.Equal(t, 1, counts.InputRequired,
		"a PrevIssueStates entry alone must not protect a twice-absent identifier")
	_, gonePresent := state.InputRequiredIssues["GONE-1"]
	assert.False(t, gonePresent)
}

// gaps_11 G-12 — pruning an absent issue must leave a status-history row
// (source janitor, reason absent_from_tracker) so the per-issue timeline
// explains the disappearance, mirroring the issue_terminal emission of the
// terminal janitor.
func TestPruneAbsentTrackerIssues_EmitsAbsentFromTrackerReason(t *testing.T) {
	state := &State{
		Running:         map[string]*RunEntry{},
		PrevIssueStates: map[string]string{"GONE-1": "In Progress"},
		InputRequiredIssues: map[string]*InputRequiredEntry{
			"GONE-1": {Identifier: "GONE-1"},
		},
		RetryAttempts:        map[string]*RetryEntry{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
		IssueStatusHistory:   map[string][]IssueStatusChange{},
	}
	prevActive := map[string]struct{}{"OTHER-1": {}}
	counts := pruneAbsentTrackerIssues(state, map[string]struct{}{"OTHER-1": {}}, prevActive)
	require.Equal(t, 1, counts.InputRequired)

	history := state.IssueStatusHistory["GONE-1"]
	found := false
	for _, c := range history {
		if c.Source == StatusSourceJanitor && c.Reason == JanitorReasonAbsentFromTracker {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a janitor/absent_from_tracker status row; got %+v", history)
}

// gaps_11 G-18 — pruning queue rows must recompute backpressure with the
// shared refresh helper so a saturated/paused queue resumes once the sweep
// drops it below the low-water mark, and the pruned shape must flow through
// the storeSnap persistence path (the same write the enqueue/drain paths
// rely on).
func TestPruneAutomationQueue_RecomputesBackpressureAndPersists(t *testing.T) {
	state := &State{
		Running:              map[string]*RunEntry{},
		PrevIssueStates:      map[string]string{},
		InputRequiredIssues:  map[string]*InputRequiredEntry{},
		RetryAttempts:        map[string]*RetryEntry{},
		PausedIdentifiers:    map[string]string{},
		PausedSessions:       map[string]*PausedSessionInfo{},
		IssueProfiles:        map[string]string{},
		IssueBackends:        map[string]string{},
		AutomationQueue:      map[string]*AutomationQueueEntry{},
		AutomationQueueOrder: []string{},
	}
	for _, ident := range []string{"GONE-1", "GONE-2", "GONE-3", "LIVE-1"} {
		key := "q-" + ident
		state.AutomationQueue[key] = &AutomationQueueEntry{
			ID:           key,
			AutomationID: "auto",
			TriggerType:  TestAutomationTriggerType,
			Issue:        domain.Issue{Identifier: ident},
		}
		state.AutomationQueueOrder = append(state.AutomationQueueOrder, key)
	}
	// Saturate: 4 entries at MaxLength 4 → producers paused.
	state.AutomationQueueBackpressure.MaxLength = 4
	refreshAutomationQueueBackpressure(state)
	require.True(t, state.AutomationQueueBackpressure.Saturated)
	require.True(t, state.AutomationQueueBackpressure.PausedProducers)

	keep := func(ident string) bool { return ident == "LIVE-1" }
	removed := pruneAutomationQueue(state, keep)
	require.Equal(t, 3, removed)

	// Length 1 < low-water 3 → saturation cleared, producers resumed.
	assert.Equal(t, 1, state.AutomationQueueBackpressure.Length)
	assert.False(t, state.AutomationQueueBackpressure.Saturated, "prune must clear saturation")
	assert.False(t, state.AutomationQueueBackpressure.PausedProducers, "prune below low-water must resume producers")
	assert.Equal(t, []string{"q-LIVE-1"}, state.AutomationQueueOrder)

	// Persistence: storeSnap (called after every onTick by Run) must write
	// the pruned queue + recomputed backpressure.
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	writer := New(&config.Config{}, nil, nil, nil)
	writer.SetAutomationQueueFile(path)
	writer.storeSnap(*state)

	reader := New(&config.Config{}, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	loaded := reader.loadAutomationQueueFromDisk(NewState(&config.Config{}))
	require.Len(t, loaded.AutomationQueue, 1)
	require.NotNil(t, loaded.AutomationQueue["q-LIVE-1"])
	assert.False(t, loaded.AutomationQueueBackpressure.Saturated)
	assert.False(t, loaded.AutomationQueueBackpressure.PausedProducers)
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
