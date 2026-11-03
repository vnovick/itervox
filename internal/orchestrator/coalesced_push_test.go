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

// TestInputRequiredQuestionPushedWithoutWaitingForInterval (#115
// acceptance): with a push interval far longer than the test, the agent's
// question is due at once, takes the issue's earlier batched write with it
// (the outbox delivers an issue's writes in order), and wakes the flusher.
func TestInputRequiredQuestionPushedWithoutWaitingForInterval(t *testing.T) {
	orch, ob, state, issue := inputRequiredHarness(t)
	ob.SetPushInterval(time.Hour)
	// A write batched earlier for the same issue.
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindCreateComment, IssueID: issue.ID, Identifier: issue.Identifier, Body: "progress note",
	}))
	require.Empty(t, ob.Due(time.Now()), "a batched write waits for the interval")

	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}
	state = orch.handleEvent(context.Background(), state, exitNeedingInput(issue))
	require.NotNil(t, state.InputRequiredIssues["ENG-1"])

	queued := ob.Snapshot()
	require.Len(t, queued, 2)
	question := queued[1]
	assert.True(t, question.Urgent, "an input-required question is an urgent write")
	assert.Equal(t, state.InputRequiredIssues["ENG-1"].QuestionCommentKey, question.CommentKey)

	now := time.Now()
	for _, e := range queued {
		assert.False(t, e.NextAttemptAt.After(now), "%q is due at once, not after the interval", e.Body)
	}
	select {
	case <-ob.Wake():
	default:
		t.Fatal("the question must wake the flusher")
	}
}

// storeLikeTracker stands in for the local tracker store: list reads carry
// the pending overlay, TrackerCopy the tracker's own state.
type storeLikeTracker struct {
	*tracker.MemoryTracker
	copies map[string]domain.Issue
}

func (s storeLikeTracker) TrackerCopy(id string) (domain.Issue, bool) {
	iss, ok := s.copies[id]
	return iss, ok
}

// TestReconcileJudgesQueuedMoveAgainstStoreTrackerCopy (#115): behind the
// store the listed state already shows the queued move, so judging the move
// against it would drop it as "already_applied" before it was ever pushed.
// Reconciliation reads the store's tracker copy instead, keeps the move, and
// still drops it once the tracker copy shows it landed.
func TestReconcileJudgesQueuedMoveAgainstStoreTrackerCopy(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	ob.SetPushInterval(time.Hour)
	st := storeLikeTracker{
		MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil),
		copies:        map[string]domain.Issue{"id-a": {ID: "id-a", Identifier: "ENG-A", State: "In Progress"}},
	}
	o := &Orchestrator{tracker: st}
	o.SetOutbox(ob)
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "In Review", FromState: "In Progress",
	}))

	listed := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Review"}} // overlaid by the store
	syncing := o.reconcileAndOverlayOutbox(listed, time.Now())
	require.Len(t, ob.PendingFor("id-a"), 1, "the queued move survives until it is pushed")
	assert.Contains(t, syncing, "ENG-A")
	assert.Equal(t, "In Review", listed[0].State)

	st.copies["id-a"] = domain.Issue{ID: "id-a", Identifier: "ENG-A", State: "In Review"}
	o.reconcileAndOverlayOutbox(listed, time.Now())
	assert.Empty(t, ob.PendingFor("id-a"), "once the tracker copy shows the move it is already applied")
}

// TestUrgentWriteListCallSites pins the documented urgent list (#115): the
// max-retries-exhausted notice, the failed-state transition and the
// rate-limit budget-exhausted notice are enqueued as urgent, while an
// ordinary managed comment (a backend-fallback notice) waits for its batch.
func TestUrgentWriteListCallSites(t *testing.T) {
	orch, ob, state, issue := inputRequiredHarness(t)
	ob.SetPushInterval(time.Hour)

	orch.postManagedComment(issue, "backend fallback notice")
	orch.commentMaxRetriesExhausted(issue, maxRetriesExhaustedBody(3, "boom"))
	orch.commentRateLimitCapExhausted(issue, "fast")
	_ = orch.asyncDiscardAndTransitionTo(state, issue.ID, issue.Identifier, "Failed", issue.State)
	orch.discardWg.Wait()

	urgent := map[string]bool{}
	for _, e := range ob.Snapshot() {
		key := string(e.Kind)
		switch {
		case e.Kind == outbox.KindUpdateState:
			key = "failed_state"
		case strings.Contains(e.Body, "backend fallback notice"):
			key = "backend_fallback"
		case strings.Contains(e.Body, "maximum retries exhausted"):
			key = "max_retries"
		case strings.Contains(e.Body, "budget exhausted"):
			key = "rate_limit_budget"
		}
		urgent[key] = e.Urgent
	}
	assert.Equal(t, map[string]bool{
		"backend_fallback":  false,
		"max_retries":       true,
		"rate_limit_budget": true,
		"failed_state":      true,
	}, urgent)
}
