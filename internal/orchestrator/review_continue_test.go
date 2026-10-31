package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// reviewPromptRunner records each prompt and holds every turn until
// released.
type reviewPromptRunner struct {
	mu      sync.Mutex
	prompts []string
	release chan struct{}
}

func (r *reviewPromptRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	select {
	case <-r.release:
		return agent.TurnResult{SessionID: "s", ResultText: "ok"}, nil
	case <-ctx.Done():
		return agent.TurnResult{Failed: true}, ctx.Err()
	}
}

func (r *reviewPromptRunner) prompt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.prompts) {
		return ""
	}
	return r.prompts[i]
}

func (r *reviewPromptRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func reviewCfg(root string) *config.Config {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	cfg.Tracker.WorkingState = "In Progress"
	cfg.Tracker.CompletionState = "In Review"
	cfg.Workspace.Root = root
	return cfg
}

func sampleReview(issue domain.Issue, move bool) ReviewContinuation {
	return ReviewContinuation{
		IssueID: issue.ID, Identifier: issue.Identifier, MoveToActive: move,
		Note: "Tighten the error handling overall.",
		Comments: []ReviewContinueComment{
			{Path: "internal/a.go", Line: 14, StartLine: 12, Side: "RIGHT", Body: "Return the wrapped error here.", Quote: "+\treturn nil"},
			{Path: "web/b.ts", Line: 3, Side: "LEFT", Body: "Keep this check."},
		},
	}
}

func operatorReviewFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, HandoffDirRelPath, "*_"+OperatorReviewRole+".md"))
	require.NoError(t, err)
	return m
}

func startLoop(t *testing.T, o *Orchestrator) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	require.Eventually(t, func() bool { return o.loopExitedCh() != nil }, 5*time.Second, 5*time.Millisecond)
	return func() { cancel(); <-done }
}

// TestReviewContinueInPlaceRunsWithoutTrackerMove: a review queued with
// moveToActive=false on an issue in tracker.completion_state is dispatched
// by the event loop without any tracker write (no working-state move, no
// completion move), its run finds the review as an operator-review handoff
// in the Prior Agent Handoffs block, the ledger entry is dropped once
// delivered, and the run's history says it continued from the review.
func TestReviewContinueInPlaceRunsWithoutTrackerMove(t *testing.T) {
	root := t.TempDir()
	cfg := reviewCfg(root)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &reviewPromptRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, workspace.NewManager(cfg))
	ledger := filepath.Join(t.TempDir(), "review_continuations.json")
	o.SetReviewContinuationsFile(ledger)
	stop := startLoop(t, o)
	defer stop()

	_, err := o.QueueReviewContinuation(context.Background(), sampleReview(issue, false))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond, "the run starts")

	p := rr.prompt(0)
	assert.Contains(t, p, "## Prior Agent Handoffs")
	assert.Contains(t, p, "_"+OperatorReviewRole+".md")
	assert.Contains(t, p, "internal/a.go, lines 12-14")
	assert.Contains(t, p, "Return the wrapped error here.")
	assert.Contains(t, p, "web/b.ts, line 3 (base)")
	assert.Contains(t, p, "Tighten the error handling overall.")
	files := operatorReviewFiles(t, filepath.Join(root, "ENG-1"))
	require.Len(t, files, 1, "the review is a handoff file in the issue's workspace")

	var live *RunEntry
	for _, r := range o.Snapshot().Running {
		live = r
	}
	require.NotNil(t, live)
	assert.True(t, live.ContinuedFromReview)
	assert.True(t, live.NoTrackerMove)
	assert.Equal(t, 2, live.ReviewComments)
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(ledger)
		return err == nil && !strings.Contains(string(b), "ENG-1")
	}, 5*time.Second, 5*time.Millisecond, "delivered: the ledger no longer holds it")

	close(rr.release)
	require.Eventually(t, func() bool {
		for _, h := range o.RunHistory() {
			if h.Identifier == "ENG-1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
	var run CompletedRun
	for _, h := range o.RunHistory() {
		if h.Identifier == "ENG-1" {
			run = h
		}
	}
	assert.True(t, run.ContinuedFromReview)
	assert.Equal(t, 2, run.ReviewComments)
	got, err := mt.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	assert.Equal(t, "In Review", got.State, "no tracker write: neither the working state nor the completion move")
}

// TestReviewContinueQueuedUntilAdmittedSurvivesRestart: a review that
// cannot start (no slot) stays queued, is persisted, and after a restart
// starts its run once admitted.
func TestReviewContinueQueuedUntilAdmittedSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(t.TempDir(), "review_continuations.json")
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}

	cfg1 := reviewCfg(root)
	cfg1.Agent.MaxConcurrentAgents = 0 // no slot: every admission is refused
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg1.Tracker.ActiveStates, cfg1.Tracker.TerminalStates)
	rr1 := &reviewPromptRunner{release: make(chan struct{})}
	o1 := New(cfg1, mt, rr1, workspace.NewManager(cfg1))
	o1.SetReviewContinuationsFile(ledger)
	stop1 := startLoop(t, o1)
	_, err := o1.QueueReviewContinuation(context.Background(), sampleReview(issue, false))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(ledger)
		return err == nil && strings.Contains(string(b), `"ENG-1"`) && strings.Contains(string(b), "Return the wrapped error here.")
	}, 5*time.Second, 5*time.Millisecond, "persisted")
	// Several ticks pass; nothing may start.
	assert.Never(t, func() bool { return rr1.count() > 0 }, 300*time.Millisecond, 10*time.Millisecond,
		"held while no slot admits it")
	assert.Len(t, o1.Snapshot().ReviewContinuations, 1)
	stop1()

	cfg2 := reviewCfg(root)
	rr2 := &reviewPromptRunner{release: make(chan struct{})}
	o2 := New(cfg2, mt, rr2, workspace.NewManager(cfg2))
	o2.SetReviewContinuationsFile(ledger)
	stop2 := startLoop(t, o2)
	defer stop2()
	defer close(rr2.release)
	require.Eventually(t, func() bool { return rr2.count() == 1 }, 5*time.Second, 5*time.Millisecond, "starts after the restart")
	assert.Contains(t, rr2.prompt(0), "Return the wrapped error here.")
	require.Len(t, operatorReviewFiles(t, filepath.Join(root, "ENG-1")), 1)
}

// TestReviewContinueWithMoveRunsOnNormalDispatch: a review queued for an
// issue the caller moved to an active state is not dispatched by the
// in-place path; the normal tick dispatches the active issue (with its
// working-state move) and the run takes the review.
func TestReviewContinueWithMoveRunsOnNormalDispatch(t *testing.T) {
	root := t.TempDir()
	cfg := reviewCfg(root)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &reviewPromptRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, workspace.NewManager(cfg))
	stop := startLoop(t, o)
	defer stop()
	defer close(rr.release)

	_, err := o.QueueReviewContinuation(context.Background(), sampleReview(issue, true))
	require.NoError(t, err)
	assert.Never(t, func() bool { return rr.count() > 0 }, 300*time.Millisecond, 10*time.Millisecond,
		"not active, not moved: the in-place path leaves it alone")

	mt.SetIssueState("id-1", "Todo") // the board's Dispatch move
	o.Refresh()
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Contains(t, rr.prompt(0), "Return the wrapped error here.")
	require.Eventually(t, func() bool {
		got, _ := mt.FetchIssueDetail(context.Background(), "id-1")
		return got != nil && got.State == "In Progress"
	}, 5*time.Second, 5*time.Millisecond, "a normal run moves the issue to the working state")
	for _, r := range o.Snapshot().Running {
		assert.True(t, r.ContinuedFromReview)
		assert.False(t, r.NoTrackerMove)
	}
}

// TestReviewContinueRefusedWhileRunLive: with a run live the review is
// refused (the caller steers the run instead).
func TestReviewContinueRefusedWhileRunLive(t *testing.T) {
	root := t.TempDir()
	cfg := reviewCfg(root)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &reviewPromptRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, workspace.NewManager(cfg))
	stop := startLoop(t, o)
	defer stop()
	defer close(rr.release)
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	_, err := o.QueueReviewContinuation(context.Background(), sampleReview(issue, true))
	assert.ErrorIs(t, err, ErrReviewContinueRunLive)
	assert.Empty(t, o.Snapshot().ReviewContinuations)
}

// TestReconcileKeepsInPlaceReviewRun: reconciliation does not stop a run
// started without a tracker move for being outside the active states, but
// still stops it for a terminal state other than completion_state.
func TestReconcileKeepsInPlaceReviewRun(t *testing.T) {
	cfg := reviewCfg(t.TempDir())
	state := NewState(cfg)
	state.CompletionState = "In Review"
	events := make(chan OrchestratorEvent, 4)
	entry := &RunEntry{Issue: domain.Issue{ID: "id-1", Identifier: "ENG-1", State: "In Review"}, RunID: "r1", NoTrackerMove: true}
	state.Running["id-1"] = entry
	refreshed := []domain.Issue{{ID: "id-1", Identifier: "ENG-1", State: "In Review"}}
	state = applyTrackerStates(state, map[string]string{"id-1": "r1"}, refreshed, events, nil, nil)
	assert.Contains(t, state.Running, "id-1", "completion_state: kept")
	refreshed[0].State = "Backlog"
	state = applyTrackerStates(state, map[string]string{"id-1": "r1"}, refreshed, events, nil, nil)
	assert.Contains(t, state.Running, "id-1", "another non-active state: kept")
	refreshed[0].State = "Done"
	state = applyTrackerStates(state, map[string]string{"id-1": "r1"}, refreshed, events, nil, nil)
	assert.NotContains(t, state.Running, "id-1", "a terminal state stops it")
}

// TestIneligibleReasonForContinuation: every dispatch gate except the
// active-state requirement applies.
func TestIneligibleReasonForContinuation(t *testing.T) {
	cfg := reviewCfg(t.TempDir())
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled", "In Review"} // completion may be terminal
	state := NewState(cfg)
	state.CompletionState = "In Review"
	state.MaxConcurrentAgents = 1
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	assert.Equal(t, "", ineligibleReasonForContinuation(issue, state, cfg), "completion_state is admitted even when terminal")
	issue.State = "Backlog"
	assert.Equal(t, "", ineligibleReasonForContinuation(issue, state, cfg), "not active: still admitted")
	issue.State = "Done"
	assert.Equal(t, "terminal_state", ineligibleReasonForContinuation(issue, state, cfg))
	issue.State = "In Review"

	paused := state
	paused.DispatchPaused.Active = true
	assert.Equal(t, IneligibleDispatchPaused, ineligibleReasonForContinuation(issue, paused, cfg))
	full := state
	full.MaxConcurrentAgents = 0
	assert.Equal(t, "no_slots", ineligibleReasonForContinuation(issue, full, cfg))
	blocker, todo := "ENG-0", "Todo"
	blocked := issue
	blocked.BlockedBy = []domain.BlockerRef{{Identifier: &blocker, State: &todo}}
	assert.Contains(t, ineligibleReasonForContinuation(blocked, state, cfg), IneligibleBlockedByPrefix)
	userPaused := state
	userPaused.PausedIdentifiers = map[string]string{"ENG-1": "id-1"}
	assert.Equal(t, "paused", ineligibleReasonForContinuation(issue, userPaused, cfg))
	// The backend breaker is applied by dispatchRun's gate after this check.
}

func TestRenderOperatorReview(t *testing.T) {
	out := RenderOperatorReview(ReviewContinuation{
		Note: "Overall note.",
		Comments: []ReviewContinueComment{
			{Path: "a.go", Line: 5, Body: "One line.", Quote: "+x := \"```\""},
			{Path: "b.go", Line: 9, StartLine: 2, Side: "RIGHT", StartSide: "LEFT", Body: "Across sides."},
		},
	})
	assert.Contains(t, out, "# Operator review")
	assert.Contains(t, out, "(2 comments)")
	assert.Contains(t, out, "## Overall\n\nOverall note.")
	assert.Contains(t, out, "## 1. a.go, line 5")
	assert.Contains(t, out, "````diff\n+x := \"```\"\n````", "a fence longer than any in the quote")
	assert.Contains(t, out, "## 2. b.go, lines 2 (base) to 9")
}
