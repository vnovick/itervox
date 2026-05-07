package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// itervoxQuestionBody returns a body matching the format produced by the
// TerminalInputRequired handler in event_loop.go.
func itervoxQuestionBody(question string) string {
	return itervoxCommentPrefix + "\n\n" + question + "\n\n---\n_Reply via the Itervox dashboard to continue._"
}

// makeCommentWalkOrch builds a minimal Orchestrator wired to a MemoryTracker
// seeded with the given issue (which itself carries pre-populated Comments).
// The runner is a non-stalling FakeRunner so any goroutine spawned by
// checkTrackerReplies can complete cleanly.
func makeCommentWalkOrch(t *testing.T, issue domain.Issue) *Orchestrator {
	t.Helper()
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"todo", "in-progress"}
	cfg.Tracker.TerminalStates = []string{"done", "cancelled"}
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	return New(cfg, mt, agenttest.NewFakeRunner(nil), nil)
}

func ts(epochSec int64) *time.Time {
	t := time.Unix(epochSec, 0).UTC()
	return &t
}

// ---------------------------------------------------------------------------
// recoverInputRequired — descending order (Linear-style)
// ---------------------------------------------------------------------------

// Linear's GraphQL connection (`comments(first: 50, orderBy: createdAt)` in
// internal/tracker/linear/queries.go:53) returns comments in DESCENDING order
// (newest first). Walking the array by position assuming chronological order
// inverts the logic and misreads any non-itervox comment as a "user reply".
// This test pins the timestamp-based behaviour: with no real reply, an
// unanswered itervox question must be recoverable regardless of array order.
func TestRecoverInputRequired_DescendingOrder_NoUserReply(t *testing.T) {
	t0 := ts(1000) // older — agent's own status comment
	t2 := ts(2000) // newer — itervox question

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			// Linear order: newest first.
			{Body: itervoxQuestionBody("Which approach: A or B?"), CreatedAt: t2, AuthorName: "itervox-bot"},
			{Body: "Status: looking into it", CreatedAt: t0, AuthorName: "agent"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	entry := orch.recoverInputRequired(context.Background(), issue)
	require.NotNil(t, entry, "expected recovery: 🤖 question is the most-recent comment, no user reply after it")
	assert.Equal(t, "id1", entry.IssueID)
	assert.Equal(t, "ENG-1", entry.Identifier)
	assert.Equal(t, "Which approach: A or B?", entry.Context)
}

func TestRecoverInputRequired_DescendingOrder_WithUserReply(t *testing.T) {
	t0 := ts(1000) // older
	t2 := ts(2000) // itervox question
	t3 := ts(3000) // newest — real user reply

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			{Body: "Use approach A.", CreatedAt: t3, AuthorName: "human"},
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
			{Body: "Status update", CreatedAt: t0, AuthorName: "agent"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	entry := orch.recoverInputRequired(context.Background(), issue)
	assert.Nil(t, entry, "expected nil: user reply (CreatedAt=3000) is later than itervox question (CreatedAt=2000)")
}

// ---------------------------------------------------------------------------
// recoverInputRequired — ascending order (GitHub-style)
// ---------------------------------------------------------------------------

func TestRecoverInputRequired_AscendingOrder_NoUserReply(t *testing.T) {
	t0 := ts(1000) // older — agent's own comment
	t2 := ts(2000) // newer — itervox question

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			// GitHub order: oldest first.
			{Body: "Status: looking into it", CreatedAt: t0, AuthorName: "agent"},
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	entry := orch.recoverInputRequired(context.Background(), issue)
	require.NotNil(t, entry)
	assert.Equal(t, "Which approach?", entry.Context)
}

func TestRecoverInputRequired_AscendingOrder_WithUserReply(t *testing.T) {
	t2 := ts(2000) // itervox question
	t3 := ts(3000) // user reply

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
			{Body: "Use approach A.", CreatedAt: t3, AuthorName: "human"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	entry := orch.recoverInputRequired(context.Background(), issue)
	assert.Nil(t, entry, "expected nil: user reply (CreatedAt=3000) is later than itervox question (CreatedAt=2000)")
}

// ---------------------------------------------------------------------------
// recoverInputRequired — defensive: nil CreatedAt skipped, no question found
// ---------------------------------------------------------------------------

func TestRecoverInputRequired_NoComments_ReturnsNil(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "in-progress"}
	orch := makeCommentWalkOrch(t, issue)
	assert.Nil(t, orch.recoverInputRequired(context.Background(), issue))
}

func TestRecoverInputRequired_NilCreatedAt_DoesNotPanic(t *testing.T) {
	t2 := ts(2000)
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			{Body: "no timestamp comment", CreatedAt: nil, AuthorName: "agent"},
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	entry := orch.recoverInputRequired(context.Background(), issue)
	require.NotNil(t, entry)
	assert.Equal(t, "Which approach?", entry.Context)
}

// ---------------------------------------------------------------------------
// checkTrackerReplies — descending order preserves entry (regression test)
// ---------------------------------------------------------------------------

// The 49-loop bug: with comments in Linear-descending order, today's index-
// based walk treats the older non-itervox comment as a user reply, deletes
// the InputRequiredIssues entry, and dispatches a "resumed" worker. This
// test pins the correct behaviour: state.InputRequiredIssues must be
// preserved when no genuine user reply is present.
func TestCheckTrackerReplies_DescendingOrder_PreservesEntry(t *testing.T) {
	t0 := ts(1000) // agent's own pre-sentinel comment
	t2 := ts(2000) // itervox question (most recent)

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			// Linear order: newest first.
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
			{Body: "Status: looking into it", CreatedAt: t0, AuthorName: "agent"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 1
	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID:    "id1",
		Identifier: "ENG-1",
		Context:    "Which approach?",
		QueuedAt:   *t2,
	}

	state = orch.checkTrackerReplies(context.Background(), state)

	_, present := state.InputRequiredIssues["ENG-1"]
	assert.True(t, present, "InputRequiredIssues entry must NOT be deleted when no real user reply exists")
	assert.Empty(t, state.Running, "no worker should be dispatched")
	assert.Empty(t, state.Claimed, "no claim should be set")
}

// ---------------------------------------------------------------------------
// checkTrackerReplies — descending order, real reply detected
// ---------------------------------------------------------------------------

func TestCheckTrackerReplies_DescendingOrder_DetectsRealReply(t *testing.T) {
	t0 := ts(1000)
	t2 := ts(2000)
	t3 := ts(3000) // real user reply, newest

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			{Body: "Use approach A.", CreatedAt: t3, AuthorName: "human"},
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
			{Body: "Status update", CreatedAt: t0, AuthorName: "agent"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	// Cancel context after the test body so the spawned runWorkerWithResume
	// goroutine can drain. sendExit will still deliver into o.events (capacity
	// 64) without blocking even though no Run() loop is consuming.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 1
	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID:    "id1",
		Identifier: "ENG-1",
		Context:    "Which approach?",
		QueuedAt:   *t2,
	}

	state = orch.checkTrackerReplies(ctx, state)

	_, present := state.InputRequiredIssues["ENG-1"]
	assert.False(t, present, "InputRequiredIssues entry must be deleted when a real user reply is detected")
	assert.Contains(t, state.Claimed, "id1", "issue should be claimed for the resumed worker")
}

// ---------------------------------------------------------------------------
// checkTrackerReplies — ascending order baseline (GitHub-style happy path)
// ---------------------------------------------------------------------------

func TestCheckTrackerReplies_AscendingOrder_PreservesEntry(t *testing.T) {
	t0 := ts(1000)
	t2 := ts(2000)

	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "T",
		State:      "in-progress",
		Comments: []domain.Comment{
			{Body: "Status: looking into it", CreatedAt: t0, AuthorName: "agent"},
			{Body: itervoxQuestionBody("Which approach?"), CreatedAt: t2, AuthorName: "itervox-bot"},
		},
	}
	orch := makeCommentWalkOrch(t, issue)

	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 1
	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID:    "id1",
		Identifier: "ENG-1",
		Context:    "Which approach?",
		QueuedAt:   *t2,
	}

	state = orch.checkTrackerReplies(context.Background(), state)

	_, present := state.InputRequiredIssues["ENG-1"]
	assert.True(t, present, "InputRequiredIssues entry must be preserved on the GitHub-style ascending path too")
}
