package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// countingTracker wraps a MemoryTracker and counts calls to the three
// expensive fetch methods so per-tick budget / candidate-reuse fixes can be
// asserted by call-count rather than wallclock.
//
// Counters are atomic because the orchestrator's worker goroutines may issue
// tracker calls concurrently with the event loop. The Reset / Calls helpers
// run from the test goroutine only.
type countingTracker struct {
	*tracker.MemoryTracker
	fetchDetail     atomic.Int64
	fetchByID       atomic.Int64
	fetchByStates   atomic.Int64
	fetchCandidates atomic.Int64

	// fetchedDetailIDsMu guards fetchedDetailIDs, which records which issue
	// IDs FetchIssueDetail was actually called with — used by
	// TestReconcileDependencyRefreshPrioritisesBlockersResolvedConsumers to
	// assert WHICH rows a clipped batch selected, not just how many. The
	// off-loop refresh worker calls this sequentially from a single
	// goroutine per batch, but a mutex keeps this safe regardless.
	fetchedDetailIDsMu sync.Mutex
	fetchedDetailIDs   []string
}

func newCountingTracker(issues []domain.Issue, active, terminal []string) *countingTracker {
	return &countingTracker{MemoryTracker: tracker.NewMemoryTracker(issues, active, terminal)}
}

func (c *countingTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	c.fetchDetail.Add(1)
	c.fetchedDetailIDsMu.Lock()
	c.fetchedDetailIDs = append(c.fetchedDetailIDs, issueID)
	c.fetchedDetailIDsMu.Unlock()
	return c.MemoryTracker.FetchIssueDetail(ctx, issueID)
}

func (c *countingTracker) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	c.fetchByID.Add(1)
	return c.MemoryTracker.FetchIssueByIdentifier(ctx, identifier)
}

func (c *countingTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	c.fetchByStates.Add(1)
	return c.MemoryTracker.FetchIssuesByStates(ctx, states)
}

func (c *countingTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	c.fetchCandidates.Add(1)
	return c.MemoryTracker.FetchCandidateIssues(ctx)
}

// budgetIssue builds a deterministic Active-state issue for budget tests.
func budgetIssue(i int) domain.Issue {
	id := "id-" + itoa3(i)
	identifier := "BUD-" + itoa3(i)
	return domain.Issue{ID: id, Identifier: identifier, State: "In Progress"}
}

// Ported from the pre-Task-6 TestRefreshKnownDependencyAuditsRespectsPerTickBudget.
// The synchronous per-tick budget (the deleted dependencyAuditRefreshPerTickBudget
// constant) is gone along with refreshKnownDependencyAudits; the cap is now the
// config-driven cfg.Agent.DependencyAuditRefreshBatchSize applied by
// selectDependencyRefreshBatch, and the fetch itself runs off-loop in
// runDependencyRefresh. This test exercises the same fixture (200 rows, cap
// below the total) through the new entry point, reconcileDependencyRefresh,
// and waits for the off-loop worker to finish before asserting the count.
func TestReconcileDependencyRefreshRespectsBatchSizeCap(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.DependencyAuditRefreshBatchSize = 20
	cfg.Agent.DependencyAuditRefreshTimeoutMs = 5000
	cfg.Agent.DependencyAuditRefreshIntervalMs = 0
	state := NewState(cfg)
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	// Build 200 in-tracker issues and 200 corresponding audit rows. Spread
	// LastAuditedAt so the batch-size cap will pick the oldest 20.
	const total = 200
	issues := make([]domain.Issue, 0, total)
	for i := range total {
		issue := budgetIssue(i)
		issues = append(issues, issue)
		state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
			IssueID:       issue.ID,
			Identifier:    issue.Identifier,
			IssueState:    "In Progress",
			Status:        DependencyAuditBlocked,
			LastAuditedAt: now.Add(-time.Duration(total-i) * time.Minute),
		}
	}
	ct := newCountingTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	o := &Orchestrator{cfg: cfg, tracker: ct, events: make(chan OrchestratorEvent, 1)}
	start := time.Now()
	o.reconcileDependencyRefresh(t.Context(), &state, now)
	elapsed := time.Since(start)
	// The caller must not block on the tracker — that is the whole point of
	// Task 6; see TestReconcileDependencyRefreshDoesNotBlockOnSlowTracker for
	// the dedicated coverage. Kept here too since this test's fixture is the
	// one that would have caught the old synchronous cap being silently
	// dropped.
	assert.Less(t, elapsed, 100*time.Millisecond, "reconcileDependencyRefresh must return immediately")

	o.depsRefreshWg.Wait()
	<-o.events

	assert.Equal(t, int64(20), ct.fetchDetail.Load(),
		"refresh must cap at the configured DependencyAuditRefreshBatchSize")
}

// Ported from the pre-Task-6 TestRefreshPrioritisesBlockersResolvedConsumers.
//
// Final-review fix: the original version of this test set
// DependencyAuditRefreshBatchSize to 100 against a 5-row fixture, so nothing
// was ever clipped — it asserted only that all 5 rows were fetched (which
// would be true even if selectDependencyRefreshBatch ignored priority
// entirely) and separately asserted the standalone
// blockersResolvedQueueIdentifiers helper surfaces the right IDs (which says
// nothing about whether selectDependencyRefreshBatch actually CONSULTS that
// helper when clipping). Neither assertion could fail if the priority sort
// were deleted. This version clips the batch to exactly the priority-set
// size (3 of 5) and asserts the fetched IDs are EXACTLY the queued
// blockers_resolved consumers — the only way to actually exercise
// prioritisation under a real cap.
func TestReconcileDependencyRefreshPrioritisesBlockersResolvedConsumers(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.DependencyAuditRefreshBatchSize = 3 // clips to exactly the priority set (3 of 5)
	cfg.Agent.DependencyAuditRefreshTimeoutMs = 5000
	cfg.Agent.DependencyAuditRefreshIntervalMs = 0
	state := NewState(cfg)
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	// 5 audit rows, all with the same age so sorting falls through to
	// priority gating. 3 of the 5 issues have a queued blockers_resolved
	// automation; those must refresh first.
	const total = 5
	issues := make([]domain.Issue, 0, total)
	for i := range total {
		issue := budgetIssue(i)
		issues = append(issues, issue)
		state.DependencyAudit[issue.ID] = &DependencyAuditEntry{
			IssueID:       issue.ID,
			Identifier:    issue.Identifier,
			IssueState:    "In Progress",
			Status:        DependencyAuditBlocked,
			LastAuditedAt: now.Add(-time.Hour),
		}
	}

	// Queue blockers_resolved entries for id-001, id-002, id-003.
	priorityIDs := map[string]struct{}{"id-001": {}, "id-002": {}, "id-003": {}}
	for k := range priorityIDs {
		state.AutomationQueue["q-"+k] = &AutomationQueueEntry{
			ID:           "q-" + k,
			AutomationID: "auto-blockers",
			TriggerType:  config.AutomationTriggerBlockersResolved,
			Issue:        domain.Issue{ID: k},
			Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerBlockersResolved},
		}
	}

	ct := newCountingTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	o := &Orchestrator{cfg: cfg, tracker: ct, events: make(chan OrchestratorEvent, 1)}
	o.reconcileDependencyRefresh(t.Context(), &state, now)
	o.depsRefreshWg.Wait()
	<-o.events

	require.Equal(t, int64(3), ct.fetchDetail.Load(),
		"batch size 3 must clip the 5-row fixture down to exactly the priority set")

	ct.fetchedDetailIDsMu.Lock()
	fetched := append([]string(nil), ct.fetchedDetailIDs...)
	ct.fetchedDetailIDsMu.Unlock()

	fetchedSet := make(map[string]struct{}, len(fetched))
	for _, id := range fetched {
		fetchedSet[id] = struct{}{}
	}
	assert.Equal(t, priorityIDs, fetchedSet,
		"the clipped batch must fetch exactly the queued blockers_resolved consumers, not an arbitrary 3 of 5")
}

func TestDrainAutomationQueueUsesCandidateFetchWhenAvailable(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Polling.IntervalMs = 30000 // 30s — anything > 0 enables the opportunistic skip
	state := NewState(cfg)
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	// 50 queued automations; all referencing issues that will appear in the
	// candidate map. The drain pass must satisfy them entirely from the
	// candidate cache without issuing any tracker fetches.
	const total = 50
	candidateIssues := make(map[string]domain.Issue, total*2)
	for i := range total {
		issue := budgetIssue(i)
		candidateIssues[issue.ID] = issue
		candidateIssues[issue.Identifier] = issue
		key := "q-" + issue.ID
		state.AutomationQueue[key] = &AutomationQueueEntry{
			ID:            key,
			AutomationID:  "auto-noop",
			TriggerType:   "test",
			Issue:         issue,
			QueuedAt:      now.Add(-time.Hour),
			LastAttemptAt: time.Time{}, // never attempted
		}
		state.AutomationQueueOrder = append(state.AutomationQueueOrder, key)
	}
	// Saturate to avoid actually dispatching (which would require runner
	// plumbing); the drain loop will hit AvailableSlots==0 quickly after
	// the candidate-reuse logic runs.
	state.MaxConcurrentAgents = 0

	ct := newCountingTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := &Orchestrator{cfg: cfg, tracker: ct}
	o.drainAutomationQueueWithCandidates(t.Context(), &state, now, candidateIssues)

	assert.Zero(t, ct.fetchDetail.Load(),
		"drain must reuse candidate hints instead of fetching")
	assert.Zero(t, ct.fetchByID.Load(),
		"drain must reuse candidate hints instead of fetching by identifier")
}

// Ported from the pre-Task-6 TestAuditBlockersResolvedSkipsFetchWhenSeqUnchanged.
// The seq short-circuit that used to live inline in
// auditBlockersResolvedAutomationSources now lives in
// pendingBlockersResolvedStates, consulted by reconcileDependencyRefresh at
// launch time; the actual FetchIssuesByStates call happens off-loop in
// runDependencyRefresh, and the watermark only advances once the event loop
// applies the result via applyDependencyRefreshResult. This test drives all
// three ticks through that full async round-trip — launch, wait for the
// worker, apply — instead of calling the deleted synchronous function
// directly, so it still proves the fetch-skip behavior end to end.
func TestReconcileDependencyRefreshSkipsBlockersResolvedFetchWhenSeqUnchanged(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Agent.DependencyAuditRefreshBatchSize = 100
	cfg.Agent.DependencyAuditRefreshTimeoutMs = 5000
	cfg.Agent.DependencyAuditRefreshIntervalMs = 0
	state := NewState(cfg)
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	ct := newCountingTracker(nil, []string{"Backlog"}, cfg.Tracker.TerminalStates)
	o := &Orchestrator{cfg: cfg, tracker: ct, events: make(chan OrchestratorEvent, 1)}
	// Install a registered blockers_resolved automation so the early-exit
	// "no automations configured" branch does not short-circuit the test.
	// Use the exported setter so the automationsMu lock is acquired correctly.
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{
		ID:          "auto-blockers",
		ProfileName: "implementer",
		States:      []string{"Backlog"},
	}})

	// Tick 1 — seq has advanced (NewState sets LastBlockersResolvedAuditSeq
	// = 0, so any non-zero DependencyTransitionSeq triggers the fetch).
	state.DependencyTransitionSeq = 1
	o.reconcileDependencyRefresh(t.Context(), &state, now)
	require.True(t, state.DepsRefreshInFlight, "a batch must have launched")
	o.depsRefreshWg.Wait()
	o.applyDependencyRefreshResult(t.Context(), &state, (<-o.events).DependencyRefresh, now)
	require.Equal(t, int64(1), ct.fetchByStates.Load(),
		"first tick with new transitions must fetch")
	require.Equal(t, int64(1), state.LastBlockersResolvedAuditSeq,
		"watermark must advance after successful fetch")

	// Tick 2 — seq unchanged → pendingBlockersResolvedStates returns nil and
	// there are no DependencyAudit rows either, so no batch launches at all.
	o.reconcileDependencyRefresh(t.Context(), &state, now.Add(time.Minute))
	assert.False(t, state.DepsRefreshInFlight, "no batch should launch when there is nothing pending")
	assert.Equal(t, int64(1), ct.fetchByStates.Load(),
		"unchanged seq must skip the fetch entirely")

	// Tick 3 — seq advances again → fetch resumes.
	state.DependencyTransitionSeq = 2
	o.reconcileDependencyRefresh(t.Context(), &state, now.Add(2*time.Minute))
	require.True(t, state.DepsRefreshInFlight, "a new batch must have launched")
	o.depsRefreshWg.Wait()
	o.applyDependencyRefreshResult(t.Context(), &state, (<-o.events).DependencyRefresh, now.Add(2*time.Minute))
	assert.Equal(t, int64(2), ct.fetchByStates.Load(),
		"advanced seq must trigger the fetch again")
}
