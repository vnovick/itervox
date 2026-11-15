package orchestrator

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// scopeTracker is a tracker whose scoped poll (FetchCandidateIssues) keeps
// only the issues in its scope — project alpha AND assigned to me AND
// labelled agent (#182) — while reads by ID, which are unscoped, still
// return every issue in its active state. Exactly what Linear does.
type scopeTracker struct {
	*tracker.MemoryTracker
	mu       sync.Mutex
	project  map[string]string
	assignee map[string]string
	labels   map[string][]string
}

func (s *scopeTracker) set(fn func(*scopeTracker)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *scopeTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	all, err := s.MemoryTracker.FetchCandidateIssues(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(all, func(i domain.Issue) bool {
		return s.project[i.ID] != "alpha" || s.assignee[i.ID] != "me" || !slices.Contains(s.labels[i.ID], "agent")
	}), err
}

// parkingRunner parks every run until its context ends and keeps each run's
// context, so a test can tell whether a worker was cancelled.
type parkingRunner struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (r *parkingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.ctxs = append(r.ctxs, ctx)
	r.mu.Unlock()
	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func (r *parkingRunner) runs() []context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ctxs)
}

func (r *parkingRunner) waitRuns(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return len(r.runs()) >= n }, 5*time.Second, 5*time.Millisecond, "want %d runs", n)
}

// TestReassignedOutOfScopeFinishesRunWithoutNewDispatch (#182): an issue
// that leaves the tracker's scope mid-run — reassigned away, its label
// removed, or moved to another project while still assigned — keeps its
// running worker (no cancel), is marked out of scope for the dashboard, and
// gets no new run: neither from the poll nor from a retry, whose by-ID read
// is unscoped. Back in scope, it is dispatched again.
func TestReassignedOutOfScopeFinishesRunWithoutNewDispatch(t *testing.T) {
	for _, leave := range []struct {
		name          string
		leave, rejoin func(*scopeTracker)
	}{
		{"reassigned", func(s *scopeTracker) { s.assignee["id1"] = "ada" }, func(s *scopeTracker) { s.assignee["id1"] = "me" }},
		{"label removed", func(s *scopeTracker) { s.labels["id1"] = nil }, func(s *scopeTracker) { s.labels["id1"] = []string{"agent"} }},
		{"project moved while assigned", func(s *scopeTracker) { s.project["id1"] = "beta" }, func(s *scopeTracker) { s.project["id1"] = "alpha" }},
	} {
		t.Run(leave.name, func(t *testing.T) {
			outOfScopeLifecycle(t, leave.leave, leave.rejoin)
		})
	}
}

func outOfScopeLifecycle(t *testing.T, leave, rejoin func(*scopeTracker)) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Agent.MaxTurns = 1
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	st := &scopeTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		project:       map[string]string{"id1": "alpha"},
		assignee:      map[string]string{"id1": "me"},
		labels:        map[string][]string{"id1": {"agent"}},
	}
	rr := &parkingRunner{}
	o := New(cfg, st, rr, nil)
	t.Cleanup(func() {
		cancel()
		o.cancelAndCleanupWorker(issue.Identifier)
		o.workersWg.Wait(5 * time.Second)
	})
	state := NewState(cfg)

	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "in scope: dispatched")
	rr.waitRuns(t, 1)

	// The issue leaves the scope. It stays in Todo.
	st.set(leave)
	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "the running worker is kept")
	assert.NoError(t, rr.runs()[0].Err(), "and not cancelled")
	assert.Contains(t, state.OutOfScope, issue.Identifier, "marked out of scope for the dashboard")
	assert.Len(t, rr.runs(), 1, "no second run")

	// The run ends and leaves a retry behind (a failed turn); the retry is
	// due. Its by-ID read still finds ENG-1 in Todo, so only the scope
	// marker can keep it from starting a new run.
	delete(state.Running, issue.ID)
	state = ScheduleRetry(state, issue.ID, 1, issue.Identifier, "turn failed", time.Now(), 0)
	state.RetryAttempts[issue.ID].DueAt = time.Now().Add(-time.Second)
	state = o.onTick(ctx, state)
	assert.NotContains(t, state.Running, issue.ID, "no new run from the retry")
	assert.NotContains(t, state.RetryAttempts, issue.ID, "the retry is released")
	assert.NotContains(t, state.Claimed, issue.ID, "and so is the claim")
	assert.Len(t, rr.runs(), 1, "still one run")
	state = o.onTick(ctx, state)
	assert.Len(t, rr.runs(), 1, "the poll does not offer it either")
	assert.Empty(t, state.OutOfScope, "nothing left to mark")

	// Back in scope: dispatched again.
	o.cancelAndCleanupWorker(issue.Identifier)
	st.set(rejoin)
	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "back in scope: dispatched")
	rr.waitRuns(t, 2)
	assert.Empty(t, state.OutOfScope)
}
