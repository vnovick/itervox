package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestBlockersResolvedScanRunsOncePerInterval: with the seq unchanged the
// scan is due once per dependency_audit_refresh_interval_ms, not every tick.
func TestBlockersResolvedScanRunsOncePerInterval(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Agent.DependencyAuditRefreshIntervalMs = int((10 * time.Minute).Milliseconds())
	ct := newCountingTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := &Orchestrator{cfg: cfg, tracker: ct, events: make(chan OrchestratorEvent, 1)}
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{ID: "a", ProfileName: "pm", States: []string{"Backlog"}}})
	state := NewState(cfg)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	run := func(at time.Time) {
		o.reconcileDependencyRefresh(t.Context(), &state, at)
		if state.DepsRefreshInFlight {
			o.depsRefreshWg.Wait()
			o.applyDependencyRefreshResult(t.Context(), &state, (<-o.events).DependencyRefresh, at)
		}
	}
	run(now)
	require.Equal(t, int64(1), ct.fetchByStates.Load(), "the first tick scans")
	run(now.Add(time.Minute))
	require.Equal(t, int64(1), ct.fetchByStates.Load(), "not again within the interval")
	run(now.Add(10 * time.Minute))
	require.Equal(t, int64(2), ct.fetchByStates.Load(), "again once the interval passed")
}

func TestBlockersResolvedScanStates(t *testing.T) {
	backlog := []string{"Backlog", "Icebox"}
	for _, tc := range []struct {
		name  string
		rules []BlockersResolvedAutomation
		want  []string
	}{
		{"states bound the match", []BlockersResolvedAutomation{{States: []string{"Backlog"}}}, []string{"Backlog"}},
		{"no states: backlog states", []BlockersResolvedAutomation{{}}, backlog},
		{"match any: states plus backlog", []BlockersResolvedAutomation{{States: []string{"Triage"}, MatchMode: config.AutomationFilterMatchAny}}, []string{"Triage", "Backlog", "Icebox"}},
		{"union, case-insensitive dedup", []BlockersResolvedAutomation{{States: []string{"backlog"}}, {}}, []string{"backlog", "Icebox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, blockersResolvedScanStates(tc.rules, backlog))
		})
	}
	assert.Empty(t, blockersResolvedScanStates([]BlockersResolvedAutomation{{}}, nil), "no states and no backlog states: nothing to scan")
}

// byIDWithoutBlockerStatesTracker mimics the GitHub adapter: a list read
// (FetchIssuesByStates) resolves blocker states, a by-ID read
// (FetchIssueStatesByIDs) leaves them unknown.
type byIDWithoutBlockerStatesTracker struct {
	*tracker.MemoryTracker
	mu           sync.Mutex
	blockerState string
}

func (f *byIDWithoutBlockerStatesTracker) dependent(withState bool) domain.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := blockerRef("ENG-0", nil)
	if withState {
		st := f.blockerState
		ref.State = &st
	}
	issue := dependencyIssue(ref)
	issue.State = "Backlog"
	return issue
}

func (f *byIDWithoutBlockerStatesTracker) FetchIssuesByStates(_ context.Context, _ []string) ([]domain.Issue, error) {
	return []domain.Issue{f.dependent(true)}, nil
}

func (f *byIDWithoutBlockerStatesTracker) FetchIssueStatesByIDs(_ context.Context, _ []string) ([]domain.Issue, error) {
	return []domain.Issue{f.dependent(false)}, nil
}

// TestBlockersResolvedScanIsNotSupersededByByIDRefresh: when a batch holds
// both the scan and a row refresh of the same issue, the row is not fetched
// again. On a GitHub-shaped tracker the by-ID copy has no blocker states;
// applied after the scan it would mark the row unknown and, through the
// staleness guard, discard the scan's unblocked copy — the rule would never
// fire for a Backlog issue.
func TestBlockersResolvedScanIsNotSupersededByByIDRefresh(t *testing.T) {
	cfg := dependencyAuditConfig()
	enableDependencyRefreshForTest(cfg)
	cfg.Agent.DependencyAuditRefreshIntervalMs = 1
	cfg.Agent.MaxConcurrentAgents = 0 // queue, never start
	cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {AllowedActions: []string{config.AgentActionMoveState}}}
	fake := &byIDWithoutBlockerStatesTracker{
		MemoryTracker: tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		blockerState:  "In Progress",
	}
	o := New(cfg, fake, nil, nil)
	o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{ID: "unblock", ProfileName: "pm", States: []string{"Backlog"}, MoveToState: "Todo"}})
	state := NewState(cfg)
	state.MaxConcurrentAgents = 0
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	run := func(at time.Time) {
		o.reconcileDependencyRefresh(t.Context(), &state, at)
		require.True(t, state.DepsRefreshInFlight)
		o.depsRefreshWg.Wait()
		o.applyDependencyRefreshResult(t.Context(), &state, (<-o.events).DependencyRefresh, at)
	}

	run(now)
	require.Equal(t, DependencyAuditBlocked, state.DependencyAudit["issue-1"].Status)

	fake.mu.Lock()
	fake.blockerState = "Done"
	fake.mu.Unlock()
	run(now.Add(time.Second)) // both the scan and the row's refresh are due

	require.Equal(t, DependencyAuditUnblocked, state.DependencyAudit["issue-1"].Status)
	require.Len(t, state.AutomationQueue, 1, "the scan's unblocked copy must fire the rule")
}
