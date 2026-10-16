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

// scopeTracker is a tracker whose scoped poll (FetchCandidateIssues) leaves
// out the issues marked out of scope — reassigned away from an assignee
// scope (#182) — while reads by ID, which are unscoped, still return them
// in their active state. Exactly what Linear does.
type scopeTracker struct {
	*tracker.MemoryTracker
	mu  sync.Mutex
	out map[string]bool
}

func (s *scopeTracker) setOut(id string, out bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out[id] = out
}

func (s *scopeTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	all, err := s.MemoryTracker.FetchCandidateIssues(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(all, func(i domain.Issue) bool { return s.out[i.ID] }), err
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
// reassigned out of the tracker's scope mid-run keeps its running worker (no
// cancel), is marked out of scope for the dashboard, and gets no new run —
// neither from the poll nor from a retry, whose by-ID read is unscoped.
// Handed back, it is dispatched again.
func TestReassignedOutOfScopeFinishesRunWithoutNewDispatch(t *testing.T) {
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
		out:           map[string]bool{},
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

	// A person reassigns ENG-1 to someone else. It stays in Todo.
	st.setOut(issue.ID, true)
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

	// Handed back: in scope again, dispatched again.
	o.cancelAndCleanupWorker(issue.Identifier)
	st.setOut(issue.ID, false)
	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "back in scope: dispatched")
	rr.waitRuns(t, 2)
	assert.Empty(t, state.OutOfScope)
}
