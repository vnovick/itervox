package orchestrator

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/tracker"
)

// filterTracker is a tracker with a tracker filter (#198): its scoped poll
// keeps the issues the filter matches, reads by ID are unscoped (and fresh:
// they carry the current labels), and MatchesFilter evaluates the same
// filter in memory — exactly the Linear and GitHub adapters' contract.
type filterTracker struct {
	*tracker.MemoryTracker
	filter issuefilter.Filter
	mu     sync.Mutex
	labels map[string][]string
}

func (f *filterTracker) overlay(issues []domain.Issue) []domain.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range issues {
		issues[i].Labels = slices.Clone(f.labels[issues[i].ID])
	}
	return issues
}

func (f *filterTracker) setLabels(id string, labels ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[id] = labels
}

func (f *filterTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	all, err := f.MemoryTracker.FetchCandidateIssues(ctx)
	return slices.DeleteFunc(f.overlay(all), func(i domain.Issue) bool {
		in, _ := f.MatchesFilter(i)
		return !in
	}), err
}

func (f *filterTracker) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	all, err := f.MemoryTracker.FetchIssueStatesByIDs(ctx, ids)
	return f.overlay(all), err
}

func (f *filterTracker) MatchesFilter(is domain.Issue) (bool, bool) {
	return f.filter.Match(is, issuefilter.Env{})
}

var _ tracker.ScopeMatcher = (*filterTracker)(nil)

// TestIssueLeavingFilterFinishesWithoutRedispatch (#198): an issue that
// stops matching the tracker filter mid-run (here: it gains a label the
// filter excludes) keeps its running worker, is marked out of scope, and
// gets no new run from the poll or its retry. A retry that was waiting
// when the issue left the filter — never marked, because the issue was not
// running — is released too: its unscoped by-ID read is checked against
// the filter in memory.
func TestIssueLeavingFilterFinishesWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Agent.MaxTurns = 1
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	waiting := domain.Issue{ID: "id2", Identifier: "ENG-2", Title: "T2", State: "Todo"}
	ft := &filterTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{issue, waiting}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		filter:        issuefilter.Filter{{LabelsAny: []string{"agent"}, LabelsNone: []string{"blocked"}}},
		labels:        map[string][]string{"id1": {"agent"}, "id2": {"agent"}},
	}
	rr := &parkingRunner{}
	o := New(cfg, ft, rr, nil)
	t.Cleanup(func() {
		cancel()
		o.cancelAndCleanupWorker(issue.Identifier)
		o.cancelAndCleanupWorker(waiting.Identifier)
		o.workersWg.Wait(5 * time.Second)
	})
	state := NewState(cfg)
	// ENG-2 waits on a retry (claimed, not running) before the first poll.
	state = ScheduleRetry(state, waiting.ID, 1, waiting.Identifier, "turn failed", time.Now(), 60_000)

	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "in the filter: dispatched")
	rr.waitRuns(t, 1)

	// Both leave the filter: a label the filter excludes.
	ft.setLabels("id1", "agent", "blocked")
	ft.setLabels("id2", "agent", "blocked")
	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "the running worker is kept")
	assert.NoError(t, rr.runs()[0].Err(), "and not cancelled")
	assert.Contains(t, state.OutOfScope, issue.Identifier, "marked out of scope")

	// ENG-2's retry falls due: its by-ID read still finds it in Todo.
	state.RetryAttempts[waiting.ID].DueAt = time.Now().Add(-time.Second)
	state = o.onTick(ctx, state)
	assert.NotContains(t, state.Running, waiting.ID, "the waiting retry starts no run")
	assert.NotContains(t, state.RetryAttempts, waiting.ID, "it is released")
	assert.NotContains(t, state.Claimed, waiting.ID)

	// ENG-1's run ends with a retry; it is released, not re-dispatched.
	delete(state.Running, issue.ID)
	state = ScheduleRetry(state, issue.ID, 1, issue.Identifier, "turn failed", time.Now(), 0)
	state.RetryAttempts[issue.ID].DueAt = time.Now().Add(-time.Second)
	state = o.onTick(ctx, state)
	state = o.onTick(ctx, state)
	assert.NotContains(t, state.Running, issue.ID)
	assert.NotContains(t, state.RetryAttempts, issue.ID)
	assert.Len(t, rr.runs(), 1, "one run in all")

	// Back in the filter: dispatched again.
	o.cancelAndCleanupWorker(issue.Identifier)
	ft.setLabels("id1", "agent")
	state = o.onTick(ctx, state)
	require.Contains(t, state.Running, issue.ID, "back in the filter: dispatched")
	rr.waitRuns(t, 2)
}
