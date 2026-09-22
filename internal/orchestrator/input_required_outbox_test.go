package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// inputRequiredHarness wires a real orchestrator to a real outbox sink.
func inputRequiredHarness(t *testing.T) (*Orchestrator, *outbox.Outbox, State, domain.Issue) {
	t.Helper()
	cfg := testConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	orch.SetWriteSink(NewOutboxWriteSink(ob))
	orch.SetOutbox(ob)
	return orch, ob, NewState(cfg), issue
}

func exitNeedingInput(issue domain.Issue) OrchestratorEvent {
	return OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalInputRequired,
			StartedAt:      time.Now(),
		},
		InputRequiredEntry: &InputRequiredEntry{
			IssueID: issue.ID, Identifier: issue.Identifier,
			SessionID: "agent-session-1", Context: "Which file should I edit?",
			Backend: "claude", Command: "claude", QueuedAt: time.Now(),
		},
	}
}

func TestInputRequiredQuestionEnqueuedWithKey(t *testing.T) {
	orch, ob, state, issue := inputRequiredHarness(t)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))

	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	require.NotEmpty(t, entry.QuestionCommentKey, "the entry must know its question's key before delivery")

	queued := ob.Snapshot()
	require.Len(t, queued, 1, "the question is enqueued, not posted directly")
	assert.Equal(t, outbox.KindCreateComment, queued[0].Kind)
	assert.Equal(t, entry.QuestionCommentKey, queued[0].CommentKey)
	assert.True(t, strings.HasPrefix(queued[0].Body, itervoxCommentPrefix))
	assert.Contains(t, queued[0].Body, tracker.ManagedCommentMarker)
}

func TestInputRequiredReplyEnqueuedAfterQuestion(t *testing.T) {
	orch, ob, state, issue := inputRequiredHarness(t)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	questionKey := state.InputRequiredIssues["ENG-1"].QuestionCommentKey

	ctx, cancel := context.WithCancel(context.Background())
	state = orch.handleEvent(ctx, state, OrchestratorEvent{
		Type: EventProvideInput, Identifier: "ENG-1", Message: "Edit main.go",
	})
	cancel()

	queued := ob.Snapshot()
	require.Len(t, queued, 2)
	assert.Equal(t, questionKey, queued[0].CommentKey, "question first")
	assert.Contains(t, queued[1].Body, "Edit main.go", "reply second")
	assert.NotEqual(t, questionKey, queued[1].CommentKey, "the reply has its own key")
	assert.NotEmpty(t, queued[1].CommentKey)

	// Per-issue FIFO: only the question is deliverable until it flushes.
	due := ob.Due(time.Now().Add(time.Hour))
	require.Len(t, due, 1)
	assert.Equal(t, questionKey, due[0].CommentKey)
}

func TestTrackerReplyWaitsForUndeliveredQuestion(t *testing.T) {
	orch, _, state, issue := inputRequiredHarness(t)
	// A PREVIOUS round left a question + a human reply on the tracker.
	mt := orch.tracker.(*tracker.MemoryTracker)
	_, err := mt.CreateComment(context.Background(), issue.ID, itervoxCommentPrefix+"\n\nprevious round")
	require.NoError(t, err)
	mt.AddHumanComment(issue.ID, "answer to the previous round")

	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	require.Contains(t, state.InputRequiredIssues, "ENG-1")

	state = orch.checkTrackerReplies(context.Background(), state)

	assert.Contains(t, state.InputRequiredIssues, "ENG-1",
		"the new question is still in the outbox; the old round's reply must not resume the agent")
	assert.NotContains(t, state.PendingInputResumes, "ENG-1")
}

// keylessTracker hides MemoryTracker's IdempotentCommenter methods: embedding
// the INTERFACE (not the concrete type) promotes only tracker.Tracker's methods.
type keylessTracker struct{ tracker.Tracker }

func TestQuestionKeyOmittedWhenTrackerCannotKeyComments(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, keylessTracker{mt}, &blockedRunner{}, nil)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	orch.SetWriteSink(NewOutboxWriteSink(ob))

	state := NewState(cfg)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))

	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.Empty(t, entry.QuestionCommentKey,
		"a key the tracker will never carry would make reply detection wait forever")
	require.Len(t, ob.Snapshot(), 1, "the question is still posted")
}

// TestQuestionKeyOmittedOnDirectSink guards the fix-round-1 ruling: a key is
// authoritative, and authoritative-ness is only safe when a durable outbox
// Retry stands behind the post. The direct sink has no retry, so even a
// tracker that CAN key comments (MemoryTracker implements
// tracker.IdempotentCommenter) must not get a key on this path — a failed
// single attempt would otherwise strand the entry with a key the tracker
// never carried, and reply detection would wait forever.
func TestQuestionKeyOmittedOnDirectSink(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)
	// Deliberately no SetWriteSink / SetOutbox: default direct sink.

	state := NewState(cfg)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))

	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.Empty(t, entry.QuestionCommentKey,
		"the direct sink has no retry; a key here could strand the entry forever")

	// The direct path posts from a commentWg-tracked goroutine — wait for it
	// deterministically instead of sleeping.
	orch.commentWg.Wait()

	detail, err := mt.FetchIssueDetail(context.Background(), issue.ID)
	require.NoError(t, err)
	require.Len(t, detail.Comments, 1, "the question is still posted")
	assert.True(t, strings.HasPrefix(detail.Comments[0].Body, itervoxCommentPrefix))
	assert.NotContains(t, detail.Comments[0].Body, "<!-- itervox:ck:",
		"no key marker: the comment must be posted keyless on the direct sink")
}

func TestInlineQuestionBodyAsksForTrackerReply(t *testing.T) {
	entry := &InputRequiredEntry{Context: "Which file?"}
	off := buildInputRequiredComment(entry, false)
	on := buildInputRequiredComment(entry, true)

	assert.True(t, strings.HasPrefix(off, itervoxCommentPrefix))
	assert.True(t, strings.HasPrefix(on, itervoxCommentPrefix))
	assert.Contains(t, off, "Itervox dashboard")
	assert.NotContains(t, on, "dashboard", "inline mode must not point the human at a reply box that is hidden")
	assert.Contains(t, on, "Reply to this comment")
}
