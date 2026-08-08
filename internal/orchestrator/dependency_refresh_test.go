package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// InFlight must never survive a restart. DependencyAudit is persisted through
// the automation-queue envelope, so a crash mid-refresh would otherwise leave
// rows permanently in-flight and silently unrefreshable — the same class of
// bug as a stale worker claim.
func TestDependencyAuditRestoreClearsInFlight(t *testing.T) {
	restored := copyDependencyAuditMapForRestore(map[string]*DependencyAuditEntry{
		"issue-1": {
			IssueID:              "issue-1",
			Identifier:           "ENG-1",
			InFlight:             true,
			ConsecutiveFailures:  2,
			LastRefreshAttemptAt: time.Now(),
		},
	})

	entry := restored["issue-1"]
	require.NotNil(t, entry)
	assert.False(t, entry.InFlight, "InFlight must be cleared on restore")
	// Failure history and the attempt timestamp are legitimately durable —
	// only the in-flight latch is transient.
	assert.Equal(t, 2, entry.ConsecutiveFailures)
	assert.False(t, entry.LastRefreshAttemptAt.IsZero())
}

func auditEntry(id, ident string, auditedAt, attemptedAt time.Time) *DependencyAuditEntry {
	return &DependencyAuditEntry{
		IssueID:              id,
		Identifier:           ident,
		LastAuditedAt:        auditedAt,
		LastRefreshAttemptAt: attemptedAt,
	}
}

func TestSelectDependencyRefreshBatchOldestFirst(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	older := now.Add(-20 * time.Minute)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{
		"a": auditEntry("a", "ENG-1", old, time.Time{}),
		"b": auditEntry("b", "ENG-2", older, time.Time{}),
	}}

	got := selectDependencyRefreshBatch(state, now, time.Minute, 10)

	require.Len(t, got, 2)
	assert.Equal(t, "b", got[0].Key, "oldest LastAuditedAt refreshes first")
	assert.Equal(t, "a", got[1].Key)
}

func TestSelectDependencyRefreshBatchPrioritisesQueuedBlockersResolved(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{
		DependencyAudit: map[string]*DependencyAuditEntry{
			// "a" is much older, but "b" has a queued blockers_resolved consumer
			// waiting on fresh state, so it must jump the line.
			"a": auditEntry("a", "ENG-1", now.Add(-time.Hour), time.Time{}),
			"b": auditEntry("b", "ENG-2", now.Add(-time.Minute), time.Time{}),
		},
		AutomationQueue: map[string]*AutomationQueueEntry{
			"q1": {
				TriggerType: config.AutomationTriggerBlockersResolved,
				Issue:       domain.Issue{ID: "b", Identifier: "ENG-2"},
			},
		},
	}

	got := selectDependencyRefreshBatch(state, now, time.Minute, 10)

	require.Len(t, got, 2)
	assert.Equal(t, "b", got[0].Key, "queued blockers_resolved consumer wins over age")
}

func TestSelectDependencyRefreshBatchSkipsInFlight(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	entry := auditEntry("a", "ENG-1", now.Add(-time.Hour), time.Time{})
	entry.InFlight = true
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{"a": entry}}

	assert.Empty(t, selectDependencyRefreshBatch(state, now, time.Minute, 10))
}

func TestSelectDependencyRefreshBatchHonoursInterval(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{
		// Attempted 10s ago against a 60s interval — not yet eligible.
		"a": auditEntry("a", "ENG-1", now.Add(-time.Hour), now.Add(-10*time.Second)),
		// Attempted 90s ago — eligible.
		"b": auditEntry("b", "ENG-2", now.Add(-time.Hour), now.Add(-90*time.Second)),
	}}

	got := selectDependencyRefreshBatch(state, now, time.Minute, 10)

	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].Key)
}

func TestSelectDependencyRefreshBatchCapsAtBatchSize(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{}}
	for i := range 10 {
		key := fmt.Sprintf("k%02d", i)
		state.DependencyAudit[key] = auditEntry(key, "ENG-"+key, now.Add(-time.Hour), time.Time{})
	}

	assert.Len(t, selectDependencyRefreshBatch(state, now, time.Minute, 3), 3)
}

// Selection is the only place that arms the latch, so the caller cannot forget.
func TestSelectDependencyRefreshBatchMarksRowsInFlight(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{
		"a": auditEntry("a", "ENG-1", now.Add(-time.Hour), time.Time{}),
	}}

	selectDependencyRefreshBatch(state, now, time.Minute, 10)

	assert.True(t, state.DependencyAudit["a"].InFlight)
	assert.Equal(t, now, state.DependencyAudit["a"].LastRefreshAttemptAt)
}

// Rows with neither an ID nor an identifier are unfetchable. The old
// fetchDependencyAuditIssue deleted them in its default branch; preserve that.
func TestSelectDependencyRefreshBatchDropsUnfetchableRows(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{
		"ghost": auditEntry("", "", now.Add(-time.Hour), time.Time{}),
	}}

	assert.Empty(t, selectDependencyRefreshBatch(state, now, time.Minute, 10))
	assert.NotContains(t, state.DependencyAudit, "ghost")
}

// The worker classifies each target and reports back. It must never touch
// State — the only thing it produces is a result value.
func TestRunDependencyRefreshClassifiesResults(t *testing.T) {
	tr := newFakeDepsTracker()
	tr.byID = map[string]*domain.Issue{
		"ok-1": {ID: "ok-1", Identifier: "ENG-1", Title: "t", State: "Todo"},
	}
	tr.notFound = map[string]bool{"gone-1": true}
	tr.failing = map[string]bool{"flaky-1": true}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 1)}
	o.depsRefreshWg.Add(1)

	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		Targets: []dependencyRefreshTarget{
			{Key: "ok-1", IssueID: "ok-1"},
			{Key: "gone-1", IssueID: "gone-1"},
			{Key: "flaky-1", IssueID: "flaky-1"},
		},
		Timeout:   time.Second,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	require.Equal(t, EventDependencyAuditRefreshed, ev.Type)
	res := ev.DependencyRefresh
	require.NotNil(t, res)
	assert.ElementsMatch(t, []string{"ok-1", "gone-1", "flaky-1"}, res.BatchKeys,
		"every batch key must be reported so the handler can clear InFlight")
	require.Len(t, res.Issues, 1)
	assert.Equal(t, "ok-1", res.Issues[0].RequestKey)
	assert.Equal(t, []string{"gone-1"}, res.MissingKeys)
	assert.Equal(t, []string{"flaky-1"}, res.FailedKeys)
}

// A panic in the worker must not take the daemon down, and must still produce
// a result so the handler releases the latch.
func TestRunDependencyRefreshPanicStillReports(t *testing.T) {
	o := &Orchestrator{tracker: newPanicDepsTracker(), events: make(chan OrchestratorEvent, 1)}
	o.depsRefreshWg.Add(1)

	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		Targets:   []dependencyRefreshTarget{{Key: "boom", IssueID: "boom"}},
		Timeout:   time.Second,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	require.Equal(t, EventDependencyAuditRefreshed, ev.Type)
	assert.Equal(t, []string{"boom"}, ev.DependencyRefresh.BatchKeys)
}

// A batch that runs out of time must still report every key it held, so the
// handler can release InFlight on the targets it never reached.
func TestRunDependencyRefreshTimeoutStillReportsAllKeys(t *testing.T) {
	tr := newFakeDepsTracker()
	tr.delay = 200 * time.Millisecond
	tr.byID = map[string]*domain.Issue{
		"a": {ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"},
		"b": {ID: "b", Identifier: "ENG-2", Title: "t", State: "Todo"},
		"c": {ID: "c", Identifier: "ENG-3", Title: "t", State: "Todo"},
	}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 1)}
	o.depsRefreshWg.Add(1)

	// 250ms budget against 200ms/fetch — the third target cannot be reached.
	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		Targets: []dependencyRefreshTarget{
			{Key: "a", IssueID: "a"},
			{Key: "b", IssueID: "b"},
			{Key: "c", IssueID: "c"},
		},
		Timeout:   250 * time.Millisecond,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	res := ev.DependencyRefresh
	assert.ElementsMatch(t, []string{"a", "b", "c"}, res.BatchKeys,
		"unreached targets must still appear in BatchKeys or their InFlight latch never clears")
	assert.Less(t, len(res.Issues), 3, "the batch should not have completed every fetch")
}

// A blockers_resolved fetch failure must NOT be reported as OK, because the
// handler advances the seq watermark only on success — advancing it after a
// failed fetch would skip the one scan that finds newly-unblocked work.
func TestRunDependencyRefreshBlockersResolvedFailureNotOK(t *testing.T) {
	tr := newFakeDepsTracker()
	tr.statesErr = errors.New("tracker down")
	o := &Orchestrator{
		tracker: tr,
		events:  make(chan OrchestratorEvent, 1),
	}
	o.depsRefreshWg.Add(1)

	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		States:    []string{"Todo"},
		Timeout:   time.Second,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	res := ev.DependencyRefresh
	assert.True(t, res.BlockersResolvedRan)
	assert.False(t, res.BlockersResolvedOK)
}

// The success path is the load-bearing counterpart to
// TestRunDependencyRefreshBlockersResolvedFailureNotOK: the handler only
// advances the seq watermark when BlockersResolvedOK is true, so a
// regression that always leaves it false would silently stop
// blockers_resolved automations from ever firing again.
func TestRunDependencyRefreshBlockersResolvedSuccess(t *testing.T) {
	tr := newFakeDepsTracker()
	tr.statesIssues = []domain.Issue{
		{ID: "unblocked-1", Identifier: "ENG-9", Title: "t", State: "Todo"},
	}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 1)}
	o.depsRefreshWg.Add(1)

	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		States:    []string{"Todo"},
		Timeout:   time.Second,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	res := ev.DependencyRefresh
	assert.True(t, res.BlockersResolvedRan)
	assert.True(t, res.BlockersResolvedOK)
	assert.Equal(t, tr.statesIssues, res.BlockersResolvedIssues)
}

// A target with only an Identifier (no IssueID) must be fetched via
// FetchIssueByIdentifier, not FetchIssueDetail. The fake's byID map alone
// cannot distinguish the two call paths (both are keyed by string lookup),
// so the fake records which entry point was actually invoked — if the
// switch in fetchDependencyRefreshIssue were ever accidentally swapped, this
// counter assertion (not the returned issue) is what catches it.
func TestRunDependencyRefreshUsesIdentifierPathWhenIssueIDEmpty(t *testing.T) {
	tr := newFakeDepsTracker()
	tr.byID = map[string]*domain.Issue{
		"ENG-9": {ID: "id-9", Identifier: "ENG-9", Title: "t", State: "Todo"},
	}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 1)}
	o.depsRefreshWg.Add(1)

	go o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		Targets:   []dependencyRefreshTarget{{Key: "ident-1", Identifier: "ENG-9"}},
		Timeout:   time.Second,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	o.depsRefreshWg.Wait()

	res := ev.DependencyRefresh
	require.Len(t, res.Issues, 1)
	assert.Equal(t, "ident-1", res.Issues[0].RequestKey)
	assert.Equal(t, int64(1), tr.identifierCalls.Load(),
		"FetchIssueByIdentifier must be the entry point taken when IssueID is empty")
	assert.Equal(t, int64(0), tr.detailEntryCalls.Load(),
		"FetchIssueDetail must not be called directly for an identifier-only target")
}

type fakeDepsTracker struct {
	*tracker.MemoryTracker
	byID         map[string]*domain.Issue
	notFound     map[string]bool
	failing      map[string]bool
	statesIssues []domain.Issue
	statesErr    error
	calls        atomic.Int64
	delay        time.Duration
	// detailEntryCalls / identifierCalls count entries into FetchIssueDetail
	// and FetchIssueByIdentifier respectively, distinct from the shared
	// fetch logic FetchIssueByIdentifier delegates to. Used to prove which
	// tracker method the worker's switch actually called.
	detailEntryCalls atomic.Int64
	identifierCalls  atomic.Int64
}

func newFakeDepsTracker() *fakeDepsTracker {
	return &fakeDepsTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
}

func (f *fakeDepsTracker) FetchIssueDetail(_ context.Context, id string) (*domain.Issue, error) {
	f.calls.Add(1)
	f.detailEntryCalls.Add(1)
	return f.lookup(id)
}

func (f *fakeDepsTracker) FetchIssueByIdentifier(_ context.Context, ident string) (*domain.Issue, error) {
	f.calls.Add(1)
	f.identifierCalls.Add(1)
	return f.lookup(ident)
}

// lookup is the shared byID-map lookup used by both FetchIssueDetail and
// FetchIssueByIdentifier. Keeping it separate from the two methods above
// means detailEntryCalls / identifierCalls only count which entry point the
// caller used, not shared internal plumbing — so they stay meaningful for
// proving which tracker method the worker's switch actually invoked.
func (f *fakeDepsTracker) lookup(id string) (*domain.Issue, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.notFound[id] {
		return nil, tracker.ErrNotFound
	}
	if f.failing[id] {
		return nil, errors.New("transient tracker error")
	}
	if issue, ok := f.byID[id]; ok {
		return issue, nil
	}
	return nil, tracker.ErrNotFound
}

func (f *fakeDepsTracker) FetchIssuesByStates(_ context.Context, _ []string) ([]domain.Issue, error) {
	f.calls.Add(1)
	if f.statesErr != nil {
		return nil, f.statesErr
	}
	return f.statesIssues, nil
}

type panicDepsTracker struct{ *tracker.MemoryTracker }

func newPanicDepsTracker() *panicDepsTracker {
	return &panicDepsTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
}

func (p *panicDepsTracker) FetchIssueDetail(_ context.Context, _ string) (*domain.Issue, error) {
	panic("deliberate test panic")
}

func refreshTestState() *State {
	return &State{
		DependencyAudit:     map[string]*DependencyAuditEntry{},
		DepsRefreshInFlight: true,
		AutomationQueue:     map[string]*AutomationQueueEntry{},
		TerminalStates:      []string{"Done"},
	}
}

func TestApplyDependencyRefreshClearsLatchAndInFlight(t *testing.T) {
	o := &Orchestrator{}
	now := time.Now()
	state := refreshTestState()
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BatchKeys:  []string{"a"},
		FailedKeys: []string{"a"},
		StartedAt:  now.Add(-2 * time.Second),
		FinishedAt: now,
	}, now)

	assert.False(t, state.DepsRefreshInFlight)
	assert.False(t, state.DependencyAudit["a"].InFlight)
	assert.Equal(t, int64(2000), state.DepsRefreshLastDurationMs)
}

func TestApplyDependencyRefreshNotFoundDeletesRow(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyAudit["gone"] = &DependencyAuditEntry{IssueID: "gone", InFlight: true}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BatchKeys:   []string{"gone"},
		MissingKeys: []string{"gone"},
	}, time.Now())

	assert.NotContains(t, state.DependencyAudit, "gone")
}

func TestApplyDependencyRefreshTransientErrorRetainsRow(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", InFlight: true, ConsecutiveFailures: 1,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BatchKeys:  []string{"a"},
		FailedKeys: []string{"a"},
	}, time.Now())

	require.Contains(t, state.DependencyAudit, "a", "transient failure must not drop the row")
	assert.Equal(t, 2, state.DependencyAudit["a"].ConsecutiveFailures)
}

func TestApplyDependencyRefreshSuccessResetsFailureCount(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true, ConsecutiveFailures: 3,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		// N-1: entry.LastAuditedAt is unset (zero) above; StartedAt must be
		// later than that or the freshness guard's !Before(...) treats the
		// zero/zero tie as "stale" and skips the row, defeating the test.
		StartedAt: time.Now(),
		BatchKeys: []string{"a"},
		Issues: []DependencyRefreshIssue{{
			RequestKey: "a",
			Issue:      domain.Issue{ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"},
		}},
	}, time.Now())

	require.Contains(t, state.DependencyAudit, "a")
	assert.Equal(t, 0, state.DependencyAudit["a"].ConsecutiveFailures)
}

// A result can land after the row was pruned (terminal, absent from tracker).
// Applying it would resurrect a deliberately-dropped row.
func TestApplyDependencyRefreshDropsResultForPrunedRow(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState() // DependencyAudit deliberately empty

	require.NotPanics(t, func() {
		o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
			BatchKeys: []string{"pruned"},
			Issues: []DependencyRefreshIssue{{
				RequestKey: "pruned",
				Issue:      domain.Issue{ID: "pruned", Identifier: "ENG-9", Title: "t", State: "Todo"},
			}},
		}, time.Now())
	})

	assert.Empty(t, state.DependencyAudit, "a pruned row must not be resurrected")
}

// The watermark must advance only on a successful states fetch — advancing it
// after a failure would skip the one scan that finds newly-unblocked work.
func TestApplyDependencyRefreshWatermarkOnlyOnSuccess(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyTransitionSeq = 7
	state.LastBlockersResolvedAuditSeq = 3

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BlockersResolvedRan: true,
		BlockersResolvedOK:  false,
	}, time.Now())
	assert.Equal(t, int64(3), state.LastBlockersResolvedAuditSeq, "must not advance on failure")

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BlockersResolvedRan: true,
		BlockersResolvedOK:  true,
		SeqAtLaunch:         7,
	}, time.Now())
	assert.Equal(t, int64(7), state.LastBlockersResolvedAuditSeq)
}

// BatchKeys must be cleared unconditionally, independent of which outcome
// list (if any) a key appears in. This is the partial-batch shape: a
// deadline fired mid-fetch, so "b" is in BatchKeys but in none of
// Issues/MissingKeys/FailedKeys. A rewrite that iterated FailedKeys instead
// of BatchKeys to clear InFlight would still pass every other test here
// (because "a" appears in both), so this test exists specifically to catch
// that regression via "b".
func TestApplyDependencyRefreshClearsInFlightForKeyAbsentFromEveryOutcomeList(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyAudit["a"] = &DependencyAuditEntry{IssueID: "a", InFlight: true}
	state.DependencyAudit["b"] = &DependencyAuditEntry{IssueID: "b", InFlight: true}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		BatchKeys:  []string{"a", "b"},
		FailedKeys: []string{"a"},
	}, time.Now())

	assert.False(t, state.DependencyAudit["b"].InFlight,
		"BatchKeys must clear InFlight even for a key reported in no outcome list")
}

// A row keyed by Identifier (no IssueID yet) whose fetched issue carries an
// ID must migrate to the ID-keyed row: the old key is retired and the new
// key inherits the reset bookkeeping.
func TestApplyDependencyRefreshMigratesKeyOnIdentityChange(t *testing.T) {
	o := &Orchestrator{}
	now := time.Now()
	state := refreshTestState()
	state.DependencyAudit["ENG-1"] = &DependencyAuditEntry{
		Identifier: "ENG-1", InFlight: true, ConsecutiveFailures: 2,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		// N-1: entry.LastAuditedAt is unset (zero) above; StartedAt must be
		// later than that or the freshness guard's !Before(...) treats the
		// zero/zero tie as "stale" and skips the row, defeating the test.
		StartedAt: now,
		BatchKeys: []string{"ENG-1"},
		Issues: []DependencyRefreshIssue{{
			RequestKey: "ENG-1",
			Issue:      domain.Issue{ID: "lin_123", Identifier: "ENG-1", Title: "t", State: "Todo"},
		}},
	}, now)

	assert.NotContains(t, state.DependencyAudit, "ENG-1", "old identifier-only key must be retired")
	require.Contains(t, state.DependencyAudit, "lin_123")
	assert.False(t, state.DependencyAudit["lin_123"].InFlight)
	assert.Equal(t, 0, state.DependencyAudit["lin_123"].ConsecutiveFailures)
	assert.Equal(t, now, state.DependencyAudit["lin_123"].LastRefreshAttemptAt)
}

// entry.LastRefreshAttemptAt = now is the line the whole refresh throttle
// depends on. Prove it end-to-end: after a successful apply, the row must
// not be re-selected by selectDependencyRefreshBatch within the interval.
func TestApplyDependencyRefreshSuccessSetsAttemptTimestampThatThrottles(t *testing.T) {
	o := &Orchestrator{}
	now := time.Now()
	state := refreshTestState()
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		// N-1: entry.LastAuditedAt is unset (zero) above; StartedAt must be
		// later than that or the freshness guard's !Before(...) treats the
		// zero/zero tie as "stale" and skips the row, defeating the test.
		StartedAt: now,
		BatchKeys: []string{"a"},
		Issues: []DependencyRefreshIssue{{
			RequestKey: "a",
			Issue:      domain.Issue{ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"},
		}},
	}, now)

	assert.Empty(t, selectDependencyRefreshBatch(state, now.Add(time.Second), time.Hour, 10),
		"a row refreshed moments ago must not be re-selected against a 1h interval")
}

// Pre-Gap-A this test asserted the watermark reflected DependencyTransitionSeq
// AFTER this call's own processing (the live seq) — the watermark assignment
// used to read state.DependencyTransitionSeq directly, so it had to run AFTER
// the BlockersResolvedIssues loop to pick up a transition fired by that very
// loop. Under Gap A (see task-6-brief.md) the watermark is pinned to
// result.SeqAtLaunch instead, captured by the launch site before this call
// ever runs — see TestApplyDependencyRefreshWatermarkUsesLaunchSeq. That means
// a transition fired by this call's own processing is now, by construction,
// NOT folded into the watermark: DependencyTransitionSeq ends up ahead of
// LastBlockersResolvedAuditSeq, and pendingBlockersResolvedStates correctly
// reads that as "still pending", issuing one more (safe, idempotent) fetch
// next tick rather than risking a same-window transition going unabsorbed.
func TestApplyDependencyRefreshWatermarkDoesNotAbsorbTransitionFiredByThisCall(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DependencyTransitionSeq = 5
	// Final-review fix: seeded at 3, not 5, so this test actually
	// discriminates. Seeding the watermark at the same value the assertion
	// expects (5) meant the assertion below passed even if the watermark
	// assignment at the end of applyDependencyRefreshResult were deleted
	// entirely — the seeded value would just pass through untouched. Seeding
	// a different value (3) forces the assertion to prove the watermark was
	// actually written to result.SeqAtLaunch (5) by this call.
	state.LastBlockersResolvedAuditSeq = 3
	state.DependencyAudit["b"] = &DependencyAuditEntry{
		IssueID: "b", Identifier: "ENG-2",
		Status: DependencyAuditBlocked, WasBlocked: true,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		// N-1: entry "b"'s LastAuditedAt is unset (zero) above; StartedAt
		// must be later than that or the freshness guard's !Before(...)
		// treats the zero/zero tie as "stale" and skips the issue, which
		// would mean it never gets audited at all and this test's very
		// premise (a transition fires during this call) never happens.
		StartedAt:           time.Now(),
		SeqAtLaunch:         5,
		BlockersResolvedRan: true,
		BlockersResolvedOK:  true,
		BlockersResolvedIssues: []domain.Issue{
			{ID: "b", Identifier: "ENG-2", Title: "t", State: "Todo"},
		},
	}, time.Now())

	require.Equal(t, int64(6), state.DependencyTransitionSeq,
		"sanity check: the processed issue must have actually fired a transition")
	assert.Equal(t, int64(5), state.LastBlockersResolvedAuditSeq,
		"watermark is pinned to SeqAtLaunch, not the live seq this call's own processing advanced to")
}

// The per-tick candidate audit (event_loop.go:118) must not wipe the
// bookkeeping the refresh throttle depends on. Without the carry-over, a row
// refreshed moments ago is re-selected immediately and the interval config
// does nothing.
func TestCandidateAuditPreservesRefreshBookkeeping(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{
		DependencyAudit: map[string]*DependencyAuditEntry{},
		AutomationQueue: map[string]*AutomationQueueEntry{},
		TerminalStates:  []string{"Done"},
	}
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID:              "a",
		Identifier:           "ENG-1",
		ConsecutiveFailures:  4,
		LastRefreshAttemptAt: now,
		InFlight:             true,
	}

	auditFetchedIssueDependencies(state, domain.Issue{
		ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo",
	}, now.Add(time.Second))

	entry := state.DependencyAudit["a"]
	require.NotNil(t, entry)
	assert.Equal(t, 4, entry.ConsecutiveFailures, "failure count must survive the audit")
	assert.True(t, entry.InFlight, "in-flight latch must survive the audit")
	assert.Equal(t, now, entry.LastRefreshAttemptAt, "refresh timestamp must survive the audit")

	// The throttle must now actually throttle.
	assert.Empty(t, selectDependencyRefreshBatch(state, now.Add(2*time.Second), time.Hour, 10),
		"row refreshed 2s ago must not be re-selected against a 1h interval")
}

// refreshTestCfg mirrors the PRODUCTION defaults rather than picking
// convenient round numbers. A fixture that diverges from the shipped default
// is how a defect hides: in the analyzer sub-project every test ran at
// chunk_size 4 while production defaulted to 75, and that single mismatch is
// why six task reviews all missed a progress counter that was structurally
// frozen on the default config. Keep these in step with
// internal/config's Default* constants.
func refreshTestCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Agent.DependencyAuditRefreshIntervalMs = config.DefaultDependencyAuditRefreshIntervalMs
	cfg.Agent.DependencyAuditRefreshTimeoutMs = config.DefaultDependencyAuditRefreshTimeoutMs
	cfg.Agent.DependencyAuditRefreshBatchSize = config.DefaultDependencyAuditRefreshBatchSize
	return cfg
}

// THE point of this whole change: onTick must issue zero tracker calls on the
// dependency-audit path.
func TestReconcileDependencyRefreshIssuesNoInlineTrackerCalls(t *testing.T) {
	tr := &fakeDepsTracker{byID: map[string]*domain.Issue{
		"a": {ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"},
	}}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 4), cfg: refreshTestCfg()}
	state := refreshTestState()
	state.DepsRefreshInFlight = false
	state.DependencyAudit["a"] = &DependencyAuditEntry{IssueID: "a", Identifier: "ENG-1"}

	o.reconcileDependencyRefresh(context.Background(), state, time.Now())

	assert.Equal(t, int64(0), tr.calls.Load(),
		"reconcileDependencyRefresh must not fetch on the calling goroutine")
	assert.True(t, state.DepsRefreshInFlight, "the latch must be armed before returning")

	<-o.events // let the worker finish so -race sees a clean shutdown
	o.depsRefreshWg.Wait()
}

// A slow tracker must not slow the caller down.
func TestReconcileDependencyRefreshDoesNotBlockOnSlowTracker(t *testing.T) {
	tr := &fakeDepsTracker{
		delay: 2 * time.Second,
		byID:  map[string]*domain.Issue{"a": {ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"}},
	}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 4), cfg: refreshTestCfg()}
	state := refreshTestState()
	state.DepsRefreshInFlight = false
	state.DependencyAudit["a"] = &DependencyAuditEntry{IssueID: "a", Identifier: "ENG-1"}

	start := time.Now()
	o.reconcileDependencyRefresh(context.Background(), state, time.Now())
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 200*time.Millisecond,
		"caller returned in %v; it is blocking on the tracker", elapsed)

	<-o.events
	o.depsRefreshWg.Wait()
}

// Single-flight: a second call while a batch is in flight must be a no-op.
func TestReconcileDependencyRefreshSingleFlight(t *testing.T) {
	o := &Orchestrator{tracker: &fakeDepsTracker{}, events: make(chan OrchestratorEvent, 4), cfg: refreshTestCfg()}
	state := refreshTestState()
	state.DepsRefreshInFlight = true
	state.DepsRefreshStartedAt = time.Now()
	state.DependencyAudit["a"] = &DependencyAuditEntry{IssueID: "a", Identifier: "ENG-1"}

	o.reconcileDependencyRefresh(context.Background(), state, time.Now())

	assert.False(t, state.DependencyAudit["a"].InFlight, "no new batch may be selected")
}

// The watchdog is what recovers from a dropped event send, a panicked worker,
// or a hung tracker. Without it the latch would never clear.
func TestReconcileDependencyRefreshWatchdogClearsStuckLatch(t *testing.T) {
	o := &Orchestrator{tracker: &fakeDepsTracker{}, events: make(chan OrchestratorEvent, 4), cfg: refreshTestCfg()}
	now := time.Now()
	state := refreshTestState()
	state.DepsRefreshInFlight = true
	// timeout is 30s in refreshTestCfg; go well past timeout + grace.
	state.DepsRefreshStartedAt = now.Add(-10 * time.Minute)
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	o.reconcileDependencyRefresh(context.Background(), state, now)

	// The watchdog cleared the stuck latch, and the row became eligible again
	// in the same call — so it is in-flight under a NEW batch.
	assert.True(t, state.DepsRefreshInFlight)
	assert.True(t, state.DependencyAudit["a"].InFlight)

	<-o.events
	o.depsRefreshWg.Wait()
}

// Gap B: a result from a batch the watchdog already abandoned must not clear
// the latch or the rows of the batch that replaced it.
func TestApplyDependencyRefreshDropsStaleGeneration(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DepsRefreshGeneration = 7
	state.DepsRefreshInFlight = true
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation: 6, // stale — belongs to the abandoned batch
		BatchKeys:  []string{"a"},
		FailedKeys: []string{"a"},
	}, time.Now())

	assert.True(t, state.DepsRefreshInFlight, "stale result must not clear the live latch")
	assert.True(t, state.DependencyAudit["a"].InFlight, "stale result must not release a live batch's row")
	assert.Equal(t, 0, state.DependencyAudit["a"].ConsecutiveFailures, "stale result must not record a failure")
}

// Gap A: the watermark advances to the launch-time seq, so a transition
// recorded by another path DURING the fetch window is not absorbed and still
// triggers a rescan next tick.
func TestApplyDependencyRefreshWatermarkUsesLaunchSeq(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	state.DepsRefreshGeneration = 1
	state.LastBlockersResolvedAuditSeq = 3
	// Launch observed seq 5; while the batch was in flight the poll path
	// advanced it to 9 for issues this batch never fetched.
	state.DependencyTransitionSeq = 9

	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation:          1,
		SeqAtLaunch:         5,
		BlockersResolvedRan: true,
		BlockersResolvedOK:  true,
	}, time.Now())

	assert.Equal(t, int64(5), state.LastBlockersResolvedAuditSeq,
		"watermark must advance only to the launch seq, leaving seq 9 to trigger a rescan")
	assert.NotEqual(t, state.DependencyTransitionSeq, state.LastBlockersResolvedAuditSeq,
		"absorbing the live seq would skip the rescan that finds newly-unblocked work")
}

// The watchdog must bump the generation, or the abandoned batch's result would
// still match and clear the replacement batch's latch.
func TestReconcileDependencyRefreshWatchdogBumpsGeneration(t *testing.T) {
	o := &Orchestrator{tracker: newFakeDepsTracker(), events: make(chan OrchestratorEvent, 4), cfg: refreshTestCfg()}
	now := time.Now()
	state := refreshTestState()
	state.DepsRefreshInFlight = true
	state.DepsRefreshStartedAt = now.Add(-10 * time.Minute)
	state.DepsRefreshGeneration = 4
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	o.reconcileDependencyRefresh(context.Background(), state, now)

	assert.Greater(t, state.DepsRefreshGeneration, int64(4),
		"watchdog must invalidate the abandoned batch's generation")

	<-o.events
	o.depsRefreshWg.Wait()
}

// Gap C (Task 6 review) — a stale fetched snapshot must not re-arm the
// AUTO-1 WasBlocked latch and cause a duplicate blockers_resolved dispatch.
// Modeled directly on the review's probe:
//
//	step1 status=blocked   wasBlocked=true  seq=0
//	step2 status=unblocked seq=1  (first fire)
//	step3 after stale apply: status=blocked wasBlocked=true  <- the bug
//	step4 seq=2  (was 1)                                     <- the bug
//
// Synchronously this was impossible: the fetch and the audit were the same
// event-loop turn, so an older snapshot could never land after a newer one.
// Asynchronously a batch can fetch issue X early, spend the rest of its
// timeout on other rows, and land the stale X snapshot several ticks after
// the candidate loop already observed (and dispatched on) the unblock. The
// automation queue key embeds LastTransitionVersion
// (blockers_resolved:<id>:<version>), so a second fire at a higher version
// does not dedupe against the first — a genuine duplicate dispatch.
func TestApplyDependencyRefreshSkipsStaleSnapshotThatWouldReArmAUTO1(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	blockerNotDone := "In Progress"
	blockedIssue := domain.Issue{
		ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo",
		BlockedBy: []domain.BlockerRef{blockerRef("ENG-0", &blockerNotDone)},
	}
	unblockedIssue := domain.Issue{ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"}

	batchLaunchedAt := time.Now()

	// step1: audit blocked. Arms WasBlocked.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, blockedIssue, batchLaunchedAt.Add(-time.Minute))
	require.Equal(t, DependencyAuditBlocked, state.DependencyAudit["a"].Status)
	require.True(t, state.DependencyAudit["a"].WasBlocked)
	require.Equal(t, int64(0), state.DependencyTransitionSeq)

	// step2: audit unblocked — fires, seq -> 1. Observed AFTER
	// batchLaunchedAt, simulating the candidate loop seeing the unblock
	// while an in-flight refresh batch (launched at batchLaunchedAt) is
	// still fetching other rows.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, batchLaunchedAt.Add(time.Minute))
	require.Equal(t, int64(1), state.DependencyTransitionSeq, "sanity: the unblock must have fired")
	require.False(t, state.DependencyAudit["a"].WasBlocked, "the latch must be disarmed after firing")

	// step3: the in-flight batch (launched BEFORE the unblock was observed)
	// lands its stale "still blocked" snapshot of the SAME issue.
	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation: state.DepsRefreshGeneration,
		StartedAt:  batchLaunchedAt,
		BatchKeys:  []string{"a"},
		Issues: []DependencyRefreshIssue{{
			RequestKey: "a",
			Issue:      blockedIssue, // stale — still shows the OLD blocked state
		}},
	}, time.Now())

	// The stale snapshot must have been skipped entirely: the seq must not
	// have moved, and the latch must still be disarmed from step2.
	require.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a stale fetched snapshot must not be re-audited at all")
	require.False(t, state.DependencyAudit["a"].WasBlocked,
		"a stale fetched snapshot must not re-arm the WasBlocked latch")

	// step4: a genuine re-audit of unblocked (e.g. next tick's candidate
	// loop) must NOT refire — proves the latch really wasn't re-armed.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, batchLaunchedAt.Add(2*time.Minute))
	assert.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a re-armed latch would cause a duplicate blockers_resolved dispatch — this is the v0.2.0 AUTO-1 finding reopened")
}

// erroringCandidateTracker always fails FetchCandidateIssues, simulating a
// tracker outage (revoked token, bad config, network partition). Used by
// TestReclaimStuckDependencyRefreshRunsWhenCandidateFetchFails (Gap D).
type erroringCandidateTracker struct {
	*tracker.MemoryTracker
}

func (e *erroringCandidateTracker) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, errors.New("tracker outage")
}

// Gap D (Task 6 review) — the watchdog must run even when the candidate
// fetch fails. onTick returns early at the FetchCandidateIssues error check
// (event_loop.go), which used to mean reconcileDependencyRefresh — and with
// it the watchdog — never ran during a tracker outage. That is exactly the
// scenario the watchdog exists to recover from: a hung or permanently-broken
// tracker would otherwise strand DepsRefreshInFlight and every row's
// InFlight forever, since nothing else ever clears them.
func TestReclaimStuckDependencyRefreshRunsWhenCandidateFetchFails(t *testing.T) {
	tr := &erroringCandidateTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	cfg := refreshTestCfg()
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 4), cfg: cfg}
	state := NewState(cfg)
	state.DepsRefreshInFlight = true
	// timeout is 30s in refreshTestCfg; go well past timeout + grace.
	state.DepsRefreshStartedAt = time.Now().Add(-10 * time.Minute)
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", InFlight: true,
	}

	out := o.onTick(context.Background(), state)

	assert.False(t, out.DepsRefreshInFlight,
		"the watchdog must release a stranded latch even when FetchCandidateIssues fails")
	require.NotNil(t, out.DependencyAudit["a"])
	assert.False(t, out.DependencyAudit["a"].InFlight,
		"the watchdog must release stranded rows even when FetchCandidateIssues fails")
}

// Gap E (Task 6 review) — a row the candidate loop already refreshed THIS
// tick must not be immediately eligible again. The deleted
// refreshKnownDependencyAudits skipped rows with LastAuditedAt.Equal(now);
// selectDependencyRefreshBatch had no equivalent, so a freshly-observed
// candidate row (LastRefreshAttemptAt unset, so the interval throttle never
// applies the first time) was immediately eligible for a redundant
// FetchIssueDetail on the very tick it was first audited — and, combined
// with a slow batch, the enabler for Gap C.
func TestSelectDependencyRefreshBatchSkipsRowAuditedThisSameTick(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	state := &State{DependencyAudit: map[string]*DependencyAuditEntry{
		"same-tick": auditEntry("same-tick", "ENG-1", now, time.Time{}),
		"stale":     auditEntry("stale", "ENG-2", now.Add(-time.Hour), time.Time{}),
	}}

	got := selectDependencyRefreshBatch(state, now, time.Minute, 10)

	require.Len(t, got, 1, "only the row audited before this tick should be selected")
	assert.Equal(t, "stale", got[0].Key)
}

// Gap F (Task 6 review) — a non-positive config value must fall back to the
// shared default, not silently disable the refresh (batch size) or produce
// an instantly-expired context that makes every batch fetch nothing while
// still arming the throttle (timeout).
func TestDependencyRefreshClampsNonPositiveConfigToDefaults(t *testing.T) {
	cfg := &config.Config{} // all three fields at their Go zero value
	assert.Equal(t, time.Duration(config.DefaultDependencyAuditRefreshTimeoutMs)*time.Millisecond, dependencyRefreshTimeout(cfg))
	assert.Equal(t, time.Duration(config.DefaultDependencyAuditRefreshIntervalMs)*time.Millisecond, dependencyRefreshInterval(cfg))
	assert.Equal(t, config.DefaultDependencyAuditRefreshBatchSize, dependencyRefreshBatchSize(cfg))

	cfg.Agent.DependencyAuditRefreshTimeoutMs = -1
	cfg.Agent.DependencyAuditRefreshIntervalMs = -1
	cfg.Agent.DependencyAuditRefreshBatchSize = -1
	assert.Equal(t, time.Duration(config.DefaultDependencyAuditRefreshTimeoutMs)*time.Millisecond, dependencyRefreshTimeout(cfg),
		"a negative value must clamp the same as zero")
	assert.Equal(t, time.Duration(config.DefaultDependencyAuditRefreshIntervalMs)*time.Millisecond, dependencyRefreshInterval(cfg))
	assert.Equal(t, config.DefaultDependencyAuditRefreshBatchSize, dependencyRefreshBatchSize(cfg))

	// A positive value passes through unchanged.
	cfg.Agent.DependencyAuditRefreshTimeoutMs = 1234
	cfg.Agent.DependencyAuditRefreshIntervalMs = 5678
	cfg.Agent.DependencyAuditRefreshBatchSize = 42
	assert.Equal(t, 1234*time.Millisecond, dependencyRefreshTimeout(cfg))
	assert.Equal(t, 5678*time.Millisecond, dependencyRefreshInterval(cfg))
	assert.Equal(t, 42, dependencyRefreshBatchSize(cfg))
}

// Gap F, end to end — before the clamp, a hand-constructed config.Config with
// all refresh fields left at zero silently disabled the off-loop refresh
// (selectDependencyRefreshBatch treats batchSize<=0 as "no batch"). This
// proves the footgun is closed: reconcileDependencyRefresh must still launch
// a batch against a fully zero-value cfg.
func TestReconcileDependencyRefreshLaunchesWithZeroValueConfig(t *testing.T) {
	tr := &fakeDepsTracker{byID: map[string]*domain.Issue{
		"a": {ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"},
	}}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 4), cfg: &config.Config{}}
	state := refreshTestState()
	state.DepsRefreshInFlight = false
	state.DependencyAudit["a"] = &DependencyAuditEntry{
		IssueID: "a", Identifier: "ENG-1", LastAuditedAt: time.Now().Add(-time.Hour),
	}

	o.reconcileDependencyRefresh(context.Background(), state, time.Now())

	assert.True(t, state.DepsRefreshInFlight,
		"a zero-value cfg must fall back to the default batch size, not silently disable the refresh")

	<-o.events
	o.depsRefreshWg.Wait()
}

// N-1 (Task 6 review round 2) — the Gap C guard used strict `After`, which
// is FALSE when two timestamps are equal. Several same-tick paths
// (processPendingInputResumes, drainAutomationQueueWithCandidates) audit a
// row with the SAME `now` a batch launched under but run AFTER the launch —
// exactly the non-candidate population selectDependencyRefreshBatch
// targets (and deliberately prioritises when a blockers_resolved consumer
// is queued, so the two populations overlap by design). A batch whose
// StartedAt equals a same-tick audit's LastAuditedAt therefore slipped past
// strict After. Modeled on the review's probe:
//
//	after same-tick unblock: status=unblocked seq=1
//	after stale apply:       status=blocked wasBlocked=true   <- the bug
//	after re-observing:      seq=2 (must still be 1)          <- the bug
//
// Gap E guarantees a row selectDependencyRefreshBatch actually chose cannot
// have LastAuditedAt == the launch `now` (selection skips rows audited at
// `now`), so treating equality as stale in the apply guard only ever
// excludes a genuinely later same-tick observation, never the selected
// row's own pre-launch audit.
func TestApplyDependencyRefreshSkipsSameTickSnapshotThatWouldReArmAUTO1(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	blockerNotDone := "In Progress"
	blockedIssue := domain.Issue{
		ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo",
		BlockedBy: []domain.BlockerRef{blockerRef("ENG-0", &blockerNotDone)},
	}
	unblockedIssue := domain.Issue{ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"}

	earlierTick := time.Now().Add(-time.Hour)
	now := time.Now()

	// Audit blocked at an earlier tick. Arms WasBlocked.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, blockedIssue, earlierTick)
	require.Equal(t, DependencyAuditBlocked, state.DependencyAudit["a"].Status)
	require.True(t, state.DependencyAudit["a"].WasBlocked)

	// This tick, a refresh batch launches at `now` and starts fetching "a"
	// (among other rows) off-loop.
	batchStartedAt := now

	// Later in the SAME tick (same `now`), a different in-tick path
	// (processPendingInputResumes / drainAutomationQueueWithCandidates)
	// observes the unblock via its own live fetch and audits with the same
	// `now` — fires, seq -> 1.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, now)
	require.Equal(t, int64(1), state.DependencyTransitionSeq, "sanity: the unblock must have fired")
	require.False(t, state.DependencyAudit["a"].WasBlocked, "the latch must be disarmed after firing")

	// The batch (StartedAt == now, the same instant as the audit above)
	// lands its snapshot, fetched before the same-tick unblock landed.
	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation: state.DepsRefreshGeneration,
		StartedAt:  batchStartedAt,
		BatchKeys:  []string{"a"},
		Issues: []DependencyRefreshIssue{{
			RequestKey: "a",
			Issue:      blockedIssue, // stale
		}},
	}, time.Now())

	require.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a same-tick stale snapshot (LastAuditedAt == StartedAt) must not be re-audited at all")
	require.False(t, state.DependencyAudit["a"].WasBlocked,
		"a same-tick stale snapshot must not re-arm the WasBlocked latch")

	// A genuine later re-observation must NOT refire — proves the latch
	// really wasn't re-armed.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, now.Add(time.Minute))
	assert.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a re-armed latch would cause a duplicate blockers_resolved dispatch")
}

// N-1 residual (Task 6 review round 3) — the BlockersResolvedIssues guard
// (dependency_refresh.go, the second Gap C/N-1 site) mirrors the
// result.Issues guard but had no dedicated regression test of its own: both
// prior AUTO-1 tests (TestApplyDependencyRefreshSkipsStaleSnapshotThatWouldReArmAUTO1
// and TestApplyDependencyRefreshSkipsSameTickSnapshotThatWouldReArmAUTO1)
// route stale data through the result.Issues loop only. A revert of JUST the
// BlockersResolvedIssues guard back to strict `.After` would pass the whole
// suite undetected — this test closes that gap by mirroring
// TestApplyDependencyRefreshSkipsSameTickSnapshotThatWouldReArmAUTO1 exactly,
// except the stale snapshot arrives via BlockersResolvedIssues instead of
// Issues.
func TestApplyDependencyRefreshSkipsSameTickSnapshotInBlockersResolvedLoopThatWouldReArmAUTO1(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()
	blockerNotDone := "In Progress"
	blockedIssue := domain.Issue{
		ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo",
		BlockedBy: []domain.BlockerRef{blockerRef("ENG-0", &blockerNotDone)},
	}
	unblockedIssue := domain.Issue{ID: "a", Identifier: "ENG-1", Title: "t", State: "Todo"}

	earlierTick := time.Now().Add(-time.Hour)
	now := time.Now()

	// Audit blocked at an earlier tick. Arms WasBlocked AND seeds the row so
	// entry != nil below. The BlockersResolvedIssues guard's nil handling is
	// deliberately different from the Issues loop's: there, a nil entry
	// means "never audited before, create it" (intended), so the staleness
	// comparison is only reached when a row already exists — this test must
	// seed the row for the guard's skip branch to be exercised at all.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, blockedIssue, earlierTick)
	require.Equal(t, DependencyAuditBlocked, state.DependencyAudit["a"].Status)
	require.True(t, state.DependencyAudit["a"].WasBlocked)

	// This tick, a refresh batch launches at `now` and starts its
	// blockers_resolved states scan off-loop.
	batchStartedAt := now

	// Later in the SAME tick (same `now`), a different in-tick path audits
	// the unblock — fires, seq -> 1, stamps LastAuditedAt = now.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, now)
	require.Equal(t, int64(1), state.DependencyTransitionSeq, "sanity: the unblock must have fired")
	require.False(t, state.DependencyAudit["a"].WasBlocked, "the latch must be disarmed after firing")

	// The batch (StartedAt == now, the same instant as the audit above)
	// lands its stale snapshot via BlockersResolvedIssues, not Issues.
	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation:          state.DepsRefreshGeneration,
		StartedAt:           batchStartedAt,
		BlockersResolvedRan: true,
		BlockersResolvedOK:  true,
		BlockersResolvedIssues: []domain.Issue{
			blockedIssue, // stale
		},
	}, time.Now())

	require.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a same-tick stale snapshot in BlockersResolvedIssues must not be re-audited at all")
	require.False(t, state.DependencyAudit["a"].WasBlocked,
		"a same-tick stale snapshot in BlockersResolvedIssues must not re-arm the WasBlocked latch")

	// A genuine later re-observation must NOT refire — proves the latch
	// really wasn't re-armed.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssue, now.Add(time.Minute))
	assert.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a re-armed latch would cause a duplicate blockers_resolved dispatch")
}

// IMPORTANT-1 (final whole-branch review) — a THIRD path to the AUTO-1
// regression, this time via key migration rather than same-key staleness.
// The freshness guard in applyDependencyRefreshResult's result.Issues loop
// checked only the REQUEST-key row (the key the batch was launched against);
// it never consulted the DESTINATION row that a key migration writes to.
// Those are different map entries whenever dependencyAuditKey(refreshed.Issue)
// differs from refreshed.RequestKey — e.g. a row first audited under an
// identifier-only key (tracker ID not yet known) later resolves to an
// ID-keyed row once a fuller fetch reveals the ID.
//
// Sequence modeled here:
//  1. seed a row under the OLD identifier-only key ("ENG-1"), blocked.
//  2. seed the NEW ID-keyed row ("issue-1") as already blocked too (as if a
//     prior tick had already begun migrating this issue).
//  3. the per-tick candidate loop (running THIS tick, with the issue's ID now
//     known) audits the NEW key as unblocked — fires blockers_resolved,
//     disarms WasBlocked on "issue-1".
//  4. an in-flight batch LAUNCHED BEFORE step 3 lands its result: RequestKey
//     is the OLD key ("ENG-1"), but the fetched issue snapshot now carries
//     the ID, so dependencyAuditKey resolves it to "issue-1" — the very row
//     step 3 just disarmed. The snapshot is stale (still shows Blocked).
//
// Before the fix, the guard only checked the OLD key's LastAuditedAt (stale,
// so it passed) and never looked at "issue-1"'s own freshness, so the stale
// blocked snapshot re-armed WasBlocked on "issue-1". A later genuine
// Unblocked observation would then refire blockers_resolved a second time —
// the automation queue key embeds LastTransitionVersion, so the second fire
// does not dedupe against the first.
func TestApplyDependencyRefreshSkipsStaleSnapshotOnKeyMigrationThatWouldReArmAUTO1(t *testing.T) {
	o := &Orchestrator{}
	state := refreshTestState()

	blockerNotDone := "In Progress"
	blockedIssueWithID := domain.Issue{
		ID: "issue-1", Identifier: "ENG-1", Title: "t", State: "Todo",
		BlockedBy: []domain.BlockerRef{blockerRef("ENG-0", &blockerNotDone)},
	}
	unblockedIssueWithID := domain.Issue{ID: "issue-1", Identifier: "ENG-1", Title: "t", State: "Todo"}

	batchLaunchedAt := time.Now()
	const requestKey = "ENG-1" // old identifier-only key the batch targeted
	const newKey = "issue-1"   // key dependencyAuditKey resolves the fetched issue to

	// Step 1: a stale leftover row under the old identifier-only key.
	state.DependencyAudit[requestKey] = &DependencyAuditEntry{
		Identifier: "ENG-1", Status: DependencyAuditBlocked, WasBlocked: true,
		LastAuditedAt: batchLaunchedAt.Add(-time.Hour),
	}
	// Step 2: the migrated ID-keyed row, already blocked from an earlier
	// observation, audited before the batch launched.
	state.DependencyAudit[newKey] = &DependencyAuditEntry{
		IssueID: "issue-1", Identifier: "ENG-1",
		Status: DependencyAuditBlocked, WasBlocked: true,
		LastAuditedAt: batchLaunchedAt.Add(-time.Minute),
	}

	// Step 3: the candidate loop, running THIS tick with the ID now known,
	// observes the unblock on the NEW key — fires and disarms.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssueWithID, batchLaunchedAt.Add(time.Minute))
	require.Equal(t, int64(1), state.DependencyTransitionSeq, "sanity: the candidate loop must have fired the transition")
	require.False(t, state.DependencyAudit[newKey].WasBlocked, "sanity: the migrated row's latch must be disarmed after firing")

	// Step 4: the in-flight batch (launched BEFORE the migration/unblock)
	// lands its stale snapshot keyed to the OLD identifier-only request key;
	// the fetched issue itself now carries the ID, so dependencyAuditKey
	// resolves it to the NEW row.
	o.applyDependencyRefreshResult(context.Background(), state, &DependencyRefreshResult{
		Generation: state.DepsRefreshGeneration,
		StartedAt:  batchLaunchedAt,
		BatchKeys:  []string{requestKey},
		Issues: []DependencyRefreshIssue{{
			RequestKey: requestKey,
			Issue:      blockedIssueWithID, // stale — still shows the OLD blocked state
		}},
	}, time.Now())

	require.False(t, state.DependencyAudit[newKey].WasBlocked,
		"a stale snapshot landing on a migrated row must not re-arm the WasBlocked latch")
	require.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a stale snapshot landing on a migrated row must not itself fire a transition")

	// A genuine later re-observation of Unblocked must NOT refire — proves
	// the latch really wasn't re-armed by the migration path.
	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), state, unblockedIssueWithID, batchLaunchedAt.Add(2*time.Minute))
	assert.Equal(t, int64(1), state.DependencyTransitionSeq,
		"a re-armed latch on the migrated row would cause a duplicate blockers_resolved dispatch")
}

// slowDetailTracker sleeps `delay` before every FetchIssueDetail call and
// records whether FetchIssuesByStates was ever invoked. Used by
// TestRunDependencyRefreshStatesScanRunsBeforeTargetLoopExhaustsBudget
// (IMPORTANT-2) to model a batch whose per-row fetches are slow enough to
// exhaust the whole order.Timeout budget before the target loop finishes.
type slowDetailTracker struct {
	*tracker.MemoryTracker
	delay        time.Duration
	statesCalled atomic.Bool
}

func (s *slowDetailTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	time.Sleep(s.delay)
	return s.MemoryTracker.FetchIssueDetail(ctx, issueID)
}

func (s *slowDetailTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	s.statesCalled.Store(true)
	return s.MemoryTracker.FetchIssuesByStates(ctx, states)
}

// IMPORTANT-2 (final whole-branch review) — the blockers_resolved states scan
// must run even when the per-row target loop is slow enough to exhaust the
// whole batch timeout budget. Both passes shared one order.Timeout budget,
// and the target loop ran FIRST, gating the states scan on
// `fetchCtx.Err() == nil`. At shipped defaults this is reachable: batch size
// 100 x the spec's own 300ms tracker p50 = 30 000ms, exactly
// DefaultDependencyAuditRefreshTimeoutMs. Once the target loop burns the
// entire timeout, BlockersResolvedRan never becomes true,
// LastBlockersResolvedAuditSeq never advances, and
// pendingBlockersResolvedStates keeps returning the same states forever —
// permanently starving every blockers_resolved automation sourced from the
// states scan, with no operator-visible signal.
//
// This test sets the timeout well below what the target loop alone needs to
// finish (10 targets x 20ms sleep = 200ms of target work against a 60ms
// budget), so the OLD ordering would have starved the states scan every
// time. With the states scan running first, it must complete regardless of
// how the target loop's budget plays out.
func TestRunDependencyRefreshStatesScanRunsBeforeTargetLoopExhaustsBudget(t *testing.T) {
	tr := &slowDetailTracker{
		MemoryTracker: tracker.NewMemoryTracker(nil, nil, []string{"Blocked"}),
		delay:         20 * time.Millisecond,
	}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 1)}

	const targetCount = 10
	targets := make([]dependencyRefreshTarget, targetCount)
	for i := range targets {
		targets[i] = dependencyRefreshTarget{
			Key:     fmt.Sprintf("k%d", i),
			IssueID: fmt.Sprintf("id-%d", i),
		}
	}

	o.depsRefreshWg.Add(1)
	o.runDependencyRefresh(context.Background(), dependencyRefreshOrder{
		Targets: targets,
		States:  []string{"Blocked"},
		// 10 targets x 20ms delay = 200ms of target-loop work; a 60ms budget
		// guarantees the target loop cannot finish, reproducing the
		// documented worst case (target loop consumes the whole timeout).
		Timeout:   60 * time.Millisecond,
		StartedAt: time.Now(),
	})

	ev := <-o.events
	result := ev.DependencyRefresh
	require.NotNil(t, result)

	assert.True(t, result.BlockersResolvedRan,
		"the states scan must run even though the target loop alone would exhaust the whole timeout budget")
	assert.True(t, result.BlockersResolvedOK, "the states scan must succeed since it runs before the budget is spent")
	assert.True(t, tr.statesCalled.Load(), "FetchIssuesByStates must actually have been called")

	handled := len(result.Issues) + len(result.FailedKeys) + len(result.MissingKeys)
	assert.Less(t, handled, targetCount,
		"sanity check: the budget must genuinely be insufficient for every target, or this test doesn't exercise starvation at all")
}
