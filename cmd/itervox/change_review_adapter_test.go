package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// TestReviewFeedEventCarriesNoCommentText (#238): change-review feed
// events map to their feed kinds with the issue, the PR, the destination,
// the review event and a count only.
func TestReviewFeedEventCarriesNoCommentText(t *testing.T) {
	ev := reviewFeedEvent(server.ReviewFeedEvent{Kind: server.FeedKindReviewCommentPosted, Identifier: "ENG-1", Via: "agent", RunID: "run-1"})
	assert.Equal(t, orchestrator.FeedEvent{Kind: orchestrator.FeedReviewCommentPosted, Identifier: "ENG-1",
		Payload: orchestrator.FeedPayload{Via: "agent", RunID: "run-1"}}, ev)

	ev = reviewFeedEvent(server.ReviewFeedEvent{Kind: server.FeedKindPRReviewByOperator, Identifier: "ENG-1",
		PRURL: "https://github.com/acme/app/pull/7", Event: "approve", Count: 2})
	assert.Equal(t, orchestrator.FeedPRReviewSubmittedByOperator, ev.Kind)
	assert.Equal(t, orchestrator.FeedPayload{PRURL: "https://github.com/acme/app/pull/7", Verdict: "approve", Count: 2}, ev.Payload)
	assert.Equal(t, server.FeedKindReviewCommentPosted, orchestrator.FeedReviewCommentPosted, "kind names agree")
	assert.Equal(t, server.FeedKindPRReviewByOperator, orchestrator.FeedPRReviewSubmittedByOperator)
}

// reviewContinueFixture is a REAL orchestrator (running) behind the REAL
// orchestratorAdapter move path, with the move calls recorded.
type reviewContinueFixture struct {
	orch    *orchestrator.Orchestrator
	mt      *tracker.MemoryTracker
	runner  *turnBlockingRunner
	adapter changeReviewAdapter
	moves   []string
	failMov error
}

func newReviewContinueFixture(t *testing.T, issueState string) *reviewContinueFixture {
	t.Helper()
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: issueState}
	f := &reviewContinueFixture{runner: &turnBlockingRunner{started: make(chan struct{})}}
	f.mt = tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	f.orch = orchestrator.New(cfg, f.mt, f.runner, tempWorkspaceProvider{root: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = f.orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() { cancel(); <-done })
	board := &orchestratorAdapter{orch: f.orch, cfg: cfg, tr: f.mt, notify: func() {}}
	f.adapter = changeReviewAdapter{orch: f.orch, tr: f.mt, moveState: func(ctx context.Context, id, state string) error {
		f.moves = append(f.moves, state)
		if f.failMov != nil {
			return f.failMov
		}
		return board.UpdateIssueState(ctx, id, state)
	}}
	require.Eventually(t, func() bool {
		err := f.orch.DropReviewContinuation(context.Background(), "probe", time.Time{})
		return !errors.Is(err, orchestrator.ErrReviewContinueNotRunning)
	}, 5*time.Second, 5*time.Millisecond, "the loop is running")
	return f
}

func (f *reviewContinueFixture) state(t *testing.T) string {
	t.Helper()
	got, err := f.mt.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, err)
	return got.State
}

var sampleInput = server.ReviewContinueInput{Note: "fix it", Comments: []server.ReviewContinueComment{
	{ReviewLineComment: server.ReviewLineComment{Path: "a.go", Line: 3, Side: "RIGHT", Body: "wrap"}},
}}

// TestContinueFromReviewMovesToFirstActiveStateThenDispatches: with
// moveToActive the issue goes through the board's Dispatch move to the first
// active state, and the normal tick starts the run.
func TestContinueFromReviewMovesToFirstActiveStateThenDispatches(t *testing.T) {
	f := newReviewContinueFixture(t, "In Review")
	in := sampleInput
	in.MoveToActive = true
	out, err := f.adapter.ContinueFromReview(context.Background(), "ENG-1", in)
	require.NoError(t, err)
	assert.Equal(t, "Todo", out.MovedTo)
	assert.Equal(t, []string{"Todo"}, f.moves, "the board's Dispatch target: the first active state")
	select {
	case <-f.runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}
	for _, r := range f.orch.Snapshot().Running {
		assert.True(t, r.ContinuedFromReview)
		assert.False(t, r.NoTrackerMove)
	}
}

// TestContinueFromReviewWithoutMoveStartsInPlace: unchecked, no tracker
// write is made and the issue is dispatched in its own state.
func TestContinueFromReviewWithoutMoveStartsInPlace(t *testing.T) {
	f := newReviewContinueFixture(t, "In Review")
	out, err := f.adapter.ContinueFromReview(context.Background(), "ENG-1", sampleInput)
	require.NoError(t, err)
	assert.Empty(t, out.MovedTo)
	assert.Empty(t, f.moves, "no tracker move")
	select {
	case <-f.runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}
	assert.Equal(t, "In Review", f.state(t), "the tracker state is unchanged")
	for _, r := range f.orch.Snapshot().Running {
		assert.True(t, r.NoTrackerMove)
	}
}

// TestContinueFromReviewRefusalsAndRollback: a closed issue needs the move;
// a failed move takes the queued review back.
func TestContinueFromReviewRefusalsAndRollback(t *testing.T) {
	f := newReviewContinueFixture(t, "Done")
	_, err := f.adapter.ContinueFromReview(context.Background(), "ENG-1", sampleInput)
	assert.ErrorIs(t, err, server.ErrReviewContinueIssueClosed)
	_, err = f.adapter.ContinueFromReview(context.Background(), "ENG-404", sampleInput)
	assert.ErrorIs(t, err, server.ErrReviewContinueIssueNotFound)

	f.failMov = errors.New("tracker refused")
	in := sampleInput
	in.MoveToActive = true
	_, err = f.adapter.ContinueFromReview(context.Background(), "ENG-1", in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `move the issue to "Todo"`)
	require.Eventually(t, func() bool { return len(f.orch.Snapshot().ReviewContinuations) == 0 },
		2*time.Second, 10*time.Millisecond, "the failed move takes the review back")
	assert.Equal(t, "Done", f.state(t))
}
