package orchestrator_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// freshStackProvider creates a fresh workspace each time, reporting it as
// stacked on stackedOn ("" = the blocker's branch was not available).
type freshStackProvider struct {
	path, stackedOn string
	removes         atomic.Int64
}

func (p *freshStackProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: p.path, Identifier: identifier, CreatedNow: true, StackedOn: p.stackedOn}, nil
}
func (p *freshStackProvider) RemoveWorkspace(context.Context, string, string) error {
	p.removes.Add(1)
	return nil
}
func (p *freshStackProvider) ResolvePath(string) string { return p.path }

// reviewBlockedIssues is ENG-2, blocked only by ENG-1, which is in review.
func reviewBlockedIssues() []domain.Issue {
	blocker := makeIssue("id1", "ENG-1", "In Review", nil, nil)
	dependent := makeIssue("id2", "ENG-2", "Todo", nil, nil)
	ident, id, st := "ENG-1", "id1", "In Review"
	dependent.BlockedBy = []domain.BlockerRef{{ID: &id, Identifier: &ident, State: &st}}
	return []domain.Issue{blocker, dependent}
}

func runReviewStack(t *testing.T, stackedPRs bool, stackedOn string, wait func(*orchestrator.Orchestrator, *tracker.MemoryTracker, *promptCaptureRunner) bool) (*orchestrator.Orchestrator, *promptCaptureRunner, *freshStackProvider) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "In Review"
	cfg.Workspace.BaseBranch = "main"
	cfg.Dependencies.StackedPRs = stackedPRs
	mt := tracker.NewMemoryTracker(reviewBlockedIssues(), cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	ws := &freshStackProvider{path: t.TempDir(), stackedOn: stackedOn}
	orch := orchestrator.New(cfg, mt, runner, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool { return wait(orch, mt, runner) }, 6*time.Second, 20*time.Millisecond)
	return orch, runner, ws
}

func runnerCalls(r *promptCaptureRunner) int {
	n, _ := r.snapshot()
	return n
}

// TestDependentStartsStackedWhileBlockerInReview (#73 follow-up): with
// stacked_prs on, an issue whose only blocker is in completion_state is
// dispatched, stacked on the blocker's branch, instead of waiting for the
// blocker to merge.
func TestDependentStartsStackedWhileBlockerInReview(t *testing.T) {
	_, runner, _ := runReviewStack(t, true, "itervox/eng-1", func(_ *orchestrator.Orchestrator, mt *tracker.MemoryTracker, r *promptCaptureRunner) bool {
		issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id2"})
		return runnerCalls(r) > 0 && err == nil && len(issues) == 1 && issues[0].State == "In Review"
	})
	_, prompts := runner.snapshot()
	require.NotEmpty(t, prompts)
	assert.Contains(t, prompts[0], "## Stacked Branch", "the run is stacked on the in-review blocker")
}

// TestDependentWaitsWhenBlockerBranchUnavailable: if the worktree cannot be
// stacked on the in-review blocker's branch, no agent runs (it would build on
// base_branch without the blocker's code); the miss is recorded and the
// issue waits for the blocker as before, without retrying every tick.
func TestDependentWaitsWhenBlockerBranchUnavailable(t *testing.T) {
	orch, runner, ws := runReviewStack(t, true, "", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker, _ *promptCaptureRunner) bool {
		return o.Snapshot().StackUnavailable["ENG-2"] != ""
	})
	assert.Equal(t, "ENG-1@in review", orch.Snapshot().StackUnavailable["ENG-2"])
	time.Sleep(200 * time.Millisecond) // several more ticks
	assert.Equal(t, 0, runnerCalls(runner), "no agent may run on an unstacked worktree")
	assert.Equal(t, int64(1), ws.removes.Load(), "the fresh worktree is removed once, not re-created every tick")
	_, running := orch.Snapshot().Running["id2"]
	assert.False(t, running)
}

// TestDependentBlockedWithoutStackedPRs: without stacked_prs the in-review
// blocker still holds the dependent, as before.
func TestDependentBlockedWithoutStackedPRs(t *testing.T) {
	_, runner, _ := runReviewStack(t, false, "itervox/eng-1", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker, _ *promptCaptureRunner) bool {
		return o.Snapshot().DependencyAudit != nil && len(o.Snapshot().DependencyAudit) > 0
	})
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, runnerCalls(runner))
}

// TestReviewStackGate covers the dispatch gate directly.
func TestReviewStackGate(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.StackOnReviewState = "in review"
	issues := reviewBlockedIssues()
	dependent := issues[1]

	assert.Equal(t, "", orchestrator.IneligibleReason(dependent, state, cfg), "only blocker in review: admitted")

	state.StackUnavailable["ENG-2"] = "ENG-1@in review"
	assert.Contains(t, orchestrator.IneligibleReason(dependent, state, cfg), "blocked_by", "a recorded miss holds it")
	delete(state.StackUnavailable, "ENG-2")

	inProgress := "In Progress"
	d2 := dependent
	d2.BlockedBy = []domain.BlockerRef{dependent.BlockedBy[0]}
	d2.BlockedBy[0].State = &inProgress
	assert.Contains(t, orchestrator.IneligibleReason(d2, state, cfg), "blocked_by", "blocker not in review yet")

	otherID, otherIdent, otherState := "id3", "ENG-3", "In Review"
	d3 := dependent
	d3.BlockedBy = append([]domain.BlockerRef{}, dependent.BlockedBy...)
	d3.BlockedBy = append(d3.BlockedBy, domain.BlockerRef{ID: &otherID, Identifier: &otherIdent, State: &otherState})
	assert.Contains(t, orchestrator.IneligibleReason(d3, state, cfg), "blocked_by", "two blockers: no single base to stack on")

	d4 := dependent
	d4.BlockedBy = []domain.BlockerRef{{ID: &otherID, State: &otherState}}
	assert.Contains(t, orchestrator.IneligibleReason(d4, state, cfg), "blocked_by", "a blocker without an identifier names no branch")

	state.StackOnReviewState = ""
	assert.Contains(t, orchestrator.IneligibleReason(dependent, state, cfg), "blocked_by", "stacked_prs off")
}
