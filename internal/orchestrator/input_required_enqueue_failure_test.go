package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// failingCommentTracker is a MemoryTracker whose keyless CreateComment always
// fails, so the direct fallback for a failed question enqueue fails too. The
// keyed methods stay promoted, so the orchestrator still sees an
// IdempotentCommenter and keys the question.
type failingCommentTracker struct {
	*tracker.MemoryTracker
}

func (f failingCommentTracker) CreateComment(context.Context, string, string) (*domain.Comment, error) {
	return nil, errors.New("tracker unavailable")
}

// brokenOutboxHarness wires a real orchestrator to a real outbox whose
// directory has been made read-only after opening, so every Enqueue's persist
// fails and rolls the entry back — the disk-full / EROFS / permissions case.
func brokenOutboxHarness(t *testing.T, tr tracker.Tracker) (*Orchestrator, *outbox.Outbox, State) {
	t.Helper()
	cfg := testConfig()
	orch := New(cfg, tr, &blockedRunner{}, nil)
	dir := t.TempDir()
	ob, err := outbox.New(filepath.Join(dir, "outbox.json"))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	orch.SetWriteSink(NewOutboxWriteSink(ob))
	orch.SetOutbox(ob)
	return orch, ob, NewState(cfg)
}

// awaitEvent returns the next event a background goroutine sent the event
// loop, standing in for Run's select in these handleEvent-driven tests.
func awaitEvent(t *testing.T, orch *Orchestrator) OrchestratorEvent {
	t.Helper()
	select {
	case ev := <-orch.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event reached the event loop: a failed question enqueue must fall back to a direct post")
		return OrchestratorEvent{}
	}
}

// TestQuestionEnqueueFailureClearsKey (CORE-121): when the outbox cannot
// accept the question, the question must still reach the tracker (keyless, as
// a direct post), and the entry must then be matched by that comment's id —
// not by a key no tracker comment will ever carry.
func TestQuestionEnqueueFailureClearsKey(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
	cfg := testConfig()
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch, ob, state := brokenOutboxHarness(t, mt)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	require.Empty(t, ob.Snapshot(), "precondition: the enqueue failed and rolled back")
	require.NotEmpty(t, state.InputRequiredIssues["ENG-1"].QuestionCommentKey,
		"the key stays until the fallback post is confirmed")

	state = orch.handleEvent(context.Background(), state, awaitEvent(t, orch))
	orch.commentWg.Wait()

	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	detail, err := mt.FetchIssueDetail(context.Background(), issue.ID)
	require.NoError(t, err)
	require.Len(t, detail.Comments, 1, "the question reached the tracker through the keyless fallback")
	assert.Equal(t, detail.Comments[0].ID, entry.QuestionCommentID)
	assert.Empty(t, entry.QuestionCommentKey, "a key no tracker comment carries must not stay authoritative")
	assert.Contains(t, detail.Comments[0].Body, "Which file should I edit?")

	mt.AddHumanComment(issue.ID, "use foo.go")
	state = orch.checkTrackerReplies(context.Background(), state)

	assert.NotContains(t, state.InputRequiredIssues, "ENG-1")
	resume := state.PendingInputResumes["ENG-1"]
	require.NotNil(t, resume, "a tracker reply below the fallback question resumes the agent")
	assert.Equal(t, "use foo.go", resume.UserMessage)
}

// TestQuestionEnqueueFailureDoesNotConsumeOldReply (CORE-121): when both the
// enqueue and the keyless fallback fail, the entry must keep its key. Clearing
// it would drop reply detection back to the latest-Itervox-question match,
// which would pick a PREVIOUS round's question and resume the agent with the
// human's answer to that older question.
func TestQuestionEnqueueFailureDoesNotConsumeOldReply(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Needs input", State: "In Progress"}
	cfg := testConfig()
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	_, err := mt.CreateComment(context.Background(), issue.ID, itervoxCommentPrefix+"\n\nOld question?")
	require.NoError(t, err)
	mt.AddHumanComment(issue.ID, "old answer")

	orch, _, state := brokenOutboxHarness(t, failingCommentTracker{mt})
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	key := state.InputRequiredIssues["ENG-1"].QuestionCommentKey
	require.NotEmpty(t, key)

	state = orch.handleEvent(context.Background(), state, awaitEvent(t, orch))
	orch.commentWg.Wait()

	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.Equal(t, key, entry.QuestionCommentKey, "a failed fallback keeps the key so no stale question can match")
	assert.Empty(t, entry.QuestionCommentID)

	state = orch.checkTrackerReplies(context.Background(), state)
	assert.Contains(t, state.InputRequiredIssues, "ENG-1", "still waiting for an answer to THIS question")
	assert.NotContains(t, state.PendingInputResumes, "ENG-1", "the previous round's answer must not resume the agent")
}
