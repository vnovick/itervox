package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func dependencyAuditConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled", "closed"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	return cfg
}

func dependencyIssue(blockers ...domain.BlockerRef) domain.Issue {
	return domain.Issue{
		ID:         "issue-1",
		Identifier: "ENG-1",
		Title:      "Dependent issue",
		State:      "Todo",
		BlockedBy:  blockers,
	}
}

func blockerRef(identifier string, state *string) domain.BlockerRef {
	id := identifier + "-id"
	url := "https://example.com/" + identifier
	return domain.BlockerRef{
		ID:         &id,
		Identifier: &identifier,
		State:      state,
		URL:        &url,
	}
}

func strPtr(v string) *string { return &v }

// enableDependencyRefreshForTest arms the off-loop dependency-refresh config
// fields that reconcileDependencyRefresh needs to actually launch a batch.
// dependencyAuditConfig leaves them zero (selectDependencyRefreshBatch treats
// batchSize<=0 as "refresh disabled"), which is fine for tests that only
// exercise the inline candidate-state audit but not for tests that depend on
// a row outside the active/candidate states being picked up by a refresh
// batch — those must opt in explicitly.
func enableDependencyRefreshForTest(cfg *config.Config) {
	cfg.Agent.DependencyAuditRefreshBatchSize = 100
	cfg.Agent.DependencyAuditRefreshTimeoutMs = 5000
}

// applyOffLoopDependencyRefresh waits for the dependency-refresh batch that
// onTick just launched (via reconcileDependencyRefresh) and folds its result
// back into state, the same way the real event loop's
// EventDependencyAuditRefreshed handler does. Before Task 6 this refresh ran
// synchronously inside onTick (refreshKnownDependencyAudits /
// auditBlockersResolvedAutomationSources); it is now off-loop, so any test
// asserting on a row that only a refresh batch (not the inline candidate
// audit) can reach must call this after onTick and before checking state.
func applyOffLoopDependencyRefresh(t *testing.T, o *Orchestrator, state *State) {
	t.Helper()
	require.True(t, state.DepsRefreshInFlight, "expected onTick to have launched a refresh batch")
	o.depsRefreshWg.Wait()
	select {
	case ev := <-o.events:
		require.Equal(t, EventDependencyAuditRefreshed, ev.Type)
		o.applyDependencyRefreshResult(t.Context(), state, ev.DependencyRefresh, time.Now())
	default:
		t.Fatal("expected a dependency refresh result on o.events after depsRefreshWg.Wait()")
	}
}

func TestDependencyAuditClassifiesNoBlockersAsUnblocked(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	entry := auditIssueDependencies(&state, dependencyIssue(), now)

	require.Equal(t, DependencyAuditUnblocked, entry.Status)
	require.False(t, entry.WasBlocked)
	require.Empty(t, entry.BlockedBy)
	require.Empty(t, entry.UnresolvedBlockers)
	require.Empty(t, entry.ResolvedBlockers)
}

func TestDependencyAuditClassifiesNonTerminalBlockerAsBlocked(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))

	entry := auditIssueDependencies(&state, issue, now)

	require.Equal(t, DependencyAuditBlocked, entry.Status)
	require.True(t, entry.WasBlocked)
	require.Equal(t, now, entry.FirstBlockedAt)
	require.Len(t, entry.UnresolvedBlockers, 1)
	require.Equal(t, "ENG-0", *entry.UnresolvedBlockers[0].Identifier)
}

func TestDependencyAuditPausedNonTerminalBlockerRemainsBlocked(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	state.PausedIdentifiers["ENG-0"] = "blocker-id"
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))

	entry := auditIssueDependencies(&state, issue, now)

	require.Equal(t, DependencyAuditBlocked, entry.Status)
	require.Len(t, entry.UnresolvedBlockers, 1)
	require.Equal(t, "ENG-0", *entry.UnresolvedBlockers[0].Identifier)
	require.Empty(t, entry.ResolvedBlockers)
}

func TestDependencyAuditClassifiesUnknownBlockerStateAsUnknownAndUnresolved(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := dependencyIssue(blockerRef("ENG-0", nil))

	entry := auditIssueDependencies(&state, issue, now)

	require.Equal(t, DependencyAuditUnknown, entry.Status)
	// AUTO-1: an Unknown status (transient tracker outage; blocker state nil) is
	// the dispatch fail-safe but is NOT evidence the issue was ever genuinely
	// blocked, so it must not arm the WasBlocked latch. Arming it here let an
	// outage flap (terminal -> nil -> terminal) mis-fire blockers_resolved.
	require.False(t, entry.WasBlocked)
	require.Len(t, entry.UnresolvedBlockers, 1)
	require.Nil(t, entry.UnresolvedBlockers[0].State)
}

func TestDependencyAuditTransitionsToUnblockedWhenAllBlockersResolve(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	start := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	blockedIssue := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))

	blocked := auditIssueDependencies(&state, blockedIssue, start)
	require.Equal(t, DependencyAuditBlocked, blocked.Status)

	resolvedIssue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	resolvedAt := start.Add(5 * time.Minute)
	unblocked := auditIssueDependencies(&state, resolvedIssue, resolvedAt)

	require.Equal(t, DependencyAuditUnblocked, unblocked.Status)
	require.True(t, unblocked.WasBlocked)
	require.Equal(t, start, unblocked.FirstBlockedAt)
	require.Equal(t, resolvedAt, unblocked.UnblockedAt)
	require.Equal(t, int64(1), state.DependencyTransitionSeq)
	require.Equal(t, int64(1), unblocked.LastTransitionVersion)
	require.Equal(t, "blockers_resolved", unblocked.LastTransitionReason)
	require.True(t, issueHasResolvedBlockersTransition(&blocked, unblocked))
}

func TestDependencyAuditPreservesBlockerMetadataAndSources(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := dependencyIssue(
		blockerRef("ENG-0", strPtr("In Progress")),
		blockerRef("#10", strPtr("closed")),
	)

	entry := auditIssueDependencies(&state, issue, now)

	require.Equal(t, []DependencyAuditSource{
		DependencySourceTrackerRelation,
		DependencySourceIssueText,
	}, entry.Sources)
	require.Len(t, entry.BlockedBy, 2)
	require.Equal(t, "ENG-0", *entry.BlockedBy[0].Identifier)
	require.Equal(t, "In Progress", *entry.BlockedBy[0].State)
	require.Equal(t, "https://example.com/ENG-0", *entry.BlockedBy[0].URL)
	require.Equal(t, "#10", *entry.BlockedBy[1].Identifier)
	require.Equal(t, "closed", *entry.BlockedBy[1].State)
	require.Equal(t, "https://example.com/#10", *entry.BlockedBy[1].URL)
}

// TestDependencySourceForBlockerSubIssue pins the sub_issue provenance
// label (wave-2 polish plan Task 3, item 3 / gh issue #53): a BlockerRef
// tagged Origin == "sub_issue" by internal/tracker/linear/normalize.go must
// map to DependencySourceSubIssue, not the generic DependencySourceTrackerRelation
// used for explicit "blocks" relations.
func TestDependencySourceForBlockerSubIssue(t *testing.T) {
	ident := "ENG-2"
	subIssue := domain.BlockerRef{Identifier: &ident, Origin: "sub_issue"}
	require.Equal(t, DependencySourceSubIssue, dependencySourceForBlocker(subIssue))

	relation := domain.BlockerRef{Identifier: &ident}
	require.Equal(t, DependencySourceTrackerRelation, dependencySourceForBlocker(relation))

	textIdent := "#7"
	textRef := domain.BlockerRef{Identifier: &textIdent}
	require.Equal(t, DependencySourceIssueText, dependencySourceForBlocker(textRef))
}

// TestDependencyAuditSourcesIncludesSubIssue pins the same behavior through
// the full auditIssueDependencies path (not just the unit function), so the
// Sources slice surfaced on DependencyAuditEntry — and from there through
// cmd/itervox/snapshot_rows.go to the dashboard — carries the sub_issue
// label end to end.
func TestDependencyAuditSourcesIncludesSubIssue(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	subRef := blockerRef("ENG-2", strPtr("Todo"))
	subRef.Origin = "sub_issue"
	issue := dependencyIssue(subRef)

	entry := auditIssueDependencies(&state, issue, now)

	require.Equal(t, []DependencyAuditSource{DependencySourceSubIssue}, entry.Sources)
}

func TestDependencyAuditOnTickAuditsFetchedIssues(t *testing.T) {
	cfg := dependencyAuditConfig()
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	state := NewState(cfg)

	out := o.onTick(t.Context(), state)

	entry := out.DependencyAudit[issue.ID]
	require.NotNil(t, entry)
	require.Equal(t, DependencyAuditBlocked, entry.Status)
	require.Equal(t, "ENG-0", *entry.UnresolvedBlockers[0].Identifier)
	require.Empty(t, out.Running)
}

func TestAutomationQueueDependencyOrder(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.Command = "codex"
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}}
	state := NewState(cfg)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	first := dependencyIssue(blockerRef("ENG-0", strPtr("In Progress")))
	second := domain.Issue{
		ID:         "issue-2",
		Identifier: "ENG-2",
		Title:      "Second dependent issue",
		State:      "Todo",
		BlockedBy:  []domain.BlockerRef{blockerRef("ENG-00", strPtr("In Progress"))},
	}
	dispatch := AutomationDispatch{
		AutomationID: "unblock-worker",
		ProfileName:  "default",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}
	enqueueAutomation(&state, first, dispatch, "blocked_by:ENG-0", now)
	enqueueAutomation(&state, second, dispatch, "blocked_by:ENG-00", now.Add(time.Second))

	firstResolved := first
	firstResolved.BlockedBy = []domain.BlockerRef{blockerRef("ENG-0", strPtr("Done"))}
	secondResolved := second
	secondResolved.BlockedBy = []domain.BlockerRef{blockerRef("ENG-00", strPtr("Done"))}
	auditFetchedIssueDependencies(&state, firstResolved, now.Add(2*time.Minute))
	auditFetchedIssueDependencies(&state, secondResolved, now.Add(2*time.Minute))

	o := New(cfg, nil, &agenttest.FakeRunner{Stall: true}, nil)
	o.drainAutomationQueue(t.Context(), &state, now.Add(3*time.Minute))

	require.Contains(t, state.Running, first.ID)
	require.Len(t, state.AutomationQueueOrder, 1)
	require.Equal(t, automationQueueKey(second, dispatch), state.AutomationQueueOrder[0])
	require.Equal(t, second.Identifier, state.AutomationQueue[state.AutomationQueueOrder[0]].Issue.Identifier)
}

func TestAutomationQueueDrainRefreshesTerminalIssueBeforeDispatch(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.Command = "codex"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}}
	stale := dependencyIssue()
	stale.State = "Todo"
	terminal := stale
	terminal.State = "Done"
	mt := tracker.NewMemoryTracker([]domain.Issue{terminal}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	state := NewState(cfg)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	dispatch := AutomationDispatch{
		AutomationID: "nightly-sweep",
		ProfileName:  "default",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}
	enqueueAutomation(&state, stale, dispatch, "no_slots", now)

	o.drainAutomationQueue(t.Context(), &state, now.Add(time.Minute))

	require.Empty(t, state.Running)
	require.Empty(t, state.AutomationQueue)
	require.Empty(t, state.AutomationQueueOrder)
}

func TestAutomationQueueDrainDropsDeletedIssueBeforeDispatch(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.Command = "codex"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}}
	stale := dependencyIssue()
	stale.State = "Todo"
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	state := NewState(cfg)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	dispatch := AutomationDispatch{
		AutomationID: "nightly-sweep",
		ProfileName:  "default",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}
	enqueueAutomation(&state, stale, dispatch, "no_slots", now)

	o.drainAutomationQueue(t.Context(), &state, now.Add(time.Minute))

	require.Empty(t, state.Running)
	require.Empty(t, state.AutomationQueue)
	require.Empty(t, state.AutomationQueueOrder)
}

func TestBlockersResolvedAutomationQueuesOnAuditTransition(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {AllowedActions: []string{config.AgentActionMoveState}}}
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	issue.State = "Backlog"
	busy := domain.Issue{ID: "busy", Identifier: "ENG-0", Title: "Busy", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue, busy}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{
		ID:          "unblock-backlog-to-todo",
		ProfileName: "pm",
		States:      []string{"backlog", "Backlog"},
		MoveToState: "Todo",
	}})
	state := NewState(cfg)
	state.Running["busy"] = &RunEntry{
		Issue: busy,
	}
	state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		IssueState:         "Backlog",
		Status:             DependencyAuditBlocked,
		WasBlocked:         true,
		BlockedBy:          []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
	}

	out := o.onTick(t.Context(), state)
	applyOffLoopDependencyRefresh(t, o, &out)

	require.Len(t, out.AutomationQueue, 1)
	var queued *AutomationQueueEntry
	for _, entry := range out.AutomationQueue {
		queued = entry
	}
	require.NotNil(t, queued)
	require.Contains(t, queued.ID, "blockers_resolved:"+issue.ID+":1")
	require.Equal(t, config.AutomationTriggerBlockersResolved, queued.Trigger.Type)
	require.Equal(t, int64(1), queued.Trigger.DependencyAuditVersion)
	require.Equal(t, "Todo", queued.MoveToState)
	require.Len(t, queued.Trigger.ResolvedBlockers, 1)
	require.Equal(t, "ENG-0", *queued.Trigger.ResolvedBlockers[0].Identifier)
	require.Len(t, queued.Trigger.PreviouslyBlockedBy, 1)
	require.Equal(t, "ENG-0", *queued.Trigger.PreviouslyBlockedBy[0].Identifier)
}

func TestBlockersResolvedAutomationHonorsStateFilter(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {AllowedActions: []string{config.AgentActionMoveState}}}
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	issue.State = "Review"
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{
		ID:          "unblock-backlog-to-todo",
		ProfileName: "pm",
		States:      []string{"backlog", "Backlog"},
		MoveToState: "Todo",
	}})
	state := NewState(cfg)
	state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		IssueState:         "Review",
		Status:             DependencyAuditBlocked,
		WasBlocked:         true,
		BlockedBy:          []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
	}

	out := o.onTick(t.Context(), state)

	require.Empty(t, out.AutomationQueue)
	require.Empty(t, out.Running)
}

func TestDependencyAuditRefreshesKnownRowsOutsideCandidateStates(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}}
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	issue.State = "Backlog"
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	state := NewState(cfg)
	start := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		IssueState:         "Backlog",
		Status:             DependencyAuditBlocked,
		WasBlocked:         true,
		FirstBlockedAt:     start,
		BlockedBy:          []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		LastAuditedAt:      start,
	}

	out := o.onTick(t.Context(), state)
	applyOffLoopDependencyRefresh(t, o, &out)

	entry := out.DependencyAudit[issue.ID]
	require.NotNil(t, entry)
	require.Equal(t, DependencyAuditUnblocked, entry.Status)
	require.Equal(t, "Backlog", entry.IssueState)
	require.Equal(t, int64(1), entry.LastTransitionVersion)
	require.Equal(t, dependencyTransitionReasonBlockersResolved, entry.LastTransitionReason)
	require.Len(t, entry.ResolvedBlockers, 1)
	require.Equal(t, "ENG-0", *entry.ResolvedBlockers[0].Identifier)
}

func TestKnownDependencyAuditRefreshDispatchesUnscopedBlockersResolvedAutomation(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {AllowedActions: []string{config.AgentActionMoveState}}}
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	issue.State = "Backlog"
	busy := domain.Issue{ID: "busy", Identifier: "ENG-99", Title: "Busy", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue, busy}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{
		ID:          "unblock-any-state",
		ProfileName: "pm",
		MoveToState: "Todo",
	}})
	state := NewState(cfg)
	state.Running[busy.ID] = &RunEntry{Issue: busy}
	state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		IssueState:         "Backlog",
		Status:             DependencyAuditBlocked,
		WasBlocked:         true,
		BlockedBy:          []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		LastAuditedAt:      time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	}

	out := o.onTick(t.Context(), state)
	applyOffLoopDependencyRefresh(t, o, &out)

	require.Len(t, out.AutomationQueue, 1)
	var queued *AutomationQueueEntry
	for _, entry := range out.AutomationQueue {
		queued = entry
	}
	require.NotNil(t, queued)
	require.Equal(t, config.AutomationTriggerBlockersResolved, queued.Trigger.Type)
	require.Equal(t, int64(1), queued.Trigger.DependencyAuditVersion)
	require.Equal(t, "Todo", queued.MoveToState)
}

func TestKnownDependencyAuditRefreshMigratesIdentifierKeyToIssueID(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Tracker.ActiveStates = []string{"Todo"}
	issue := dependencyIssue(blockerRef("ENG-0", strPtr("Done")))
	issue.State = "Backlog"
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	state := NewState(cfg)
	state.DependencyAudit[issue.Identifier] = &DependencyAuditEntry{
		Identifier:         issue.Identifier,
		IssueState:         "Backlog",
		Status:             DependencyAuditBlocked,
		WasBlocked:         true,
		BlockedBy:          []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-0", strPtr("In Progress"))},
		LastAuditedAt:      time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	}

	out := o.onTick(t.Context(), state)
	applyOffLoopDependencyRefresh(t, o, &out)

	require.NotContains(t, out.DependencyAudit, issue.Identifier)
	entry := out.DependencyAudit[issue.ID]
	require.NotNil(t, entry)
	require.Equal(t, DependencyAuditUnblocked, entry.Status)
}
