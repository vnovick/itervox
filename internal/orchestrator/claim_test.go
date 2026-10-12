package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// #116 — the claim check.

// humanTracker is a remote-like tracker over the memory tracker: Edit is a
// person changing an issue in the tracker (it stamps UpdatedAt, as a real
// tracker does), and reads by ID are counted per issue.
type humanTracker struct {
	*tracker.MemoryTracker
	mu      sync.Mutex
	edited  map[string]time.Time
	reads   map[string]int
	deleted map[string]bool
	down    bool // reads by ID fail
	details map[string]int
}

func newHumanTracker(issues ...domain.Issue) *humanTracker {
	cfg := automationBaseCfg()
	return &humanTracker{
		MemoryTracker: tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		edited:        map[string]time.Time{},
		reads:         map[string]int{},
		deleted:       map[string]bool{},
		details:       map[string]int{},
	}
}

// detailReads counts the detail reads of id (the tick's claim read).
func (h *humanTracker) detailReads(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.details[id]
}

// Delete is a person deleting the issue in the tracker.
func (h *humanTracker) Delete(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deleted[id] = true
}

// SetDown makes reads by ID fail (the tracker is unreachable).
func (h *humanTracker) SetDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down = down
}

func (h *humanTracker) RateLimitSnapshot() *tracker.RateLimitSnapshot { return nil }

func (h *humanTracker) Edit(id, state string) {
	h.SetIssueState(id, state)
	h.mu.Lock()
	h.edited[id] = time.Now()
	h.mu.Unlock()
}

func (h *humanTracker) stamp(iss *domain.Issue) {
	if at, ok := h.edited[iss.ID]; ok {
		iss.UpdatedAt = &at
	}
}

func (h *humanTracker) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	issues, err := h.MemoryTracker.FetchIssueStatesByIDs(ctx, ids)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		h.reads[id]++
	}
	if h.down {
		return nil, errTrackerDown
	}
	out := issues[:0]
	for i := range issues {
		if h.deleted[issues[i].ID] {
			continue
		}
		h.stamp(&issues[i])
		out = append(out, issues[i])
	}
	return out, err
}

var errTrackerDown = errors.New("tracker unreachable")

func (h *humanTracker) FetchIssueDetail(ctx context.Context, id string) (*domain.Issue, error) {
	iss, err := h.MemoryTracker.FetchIssueDetail(ctx, id)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads[id]++
	h.details[id]++
	if h.down {
		return nil, errTrackerDown
	}
	if h.deleted[id] {
		return nil, &tracker.NotFoundError{Adapter: "test", Identifier: id}
	}
	if iss != nil {
		h.stamp(iss)
	}
	return iss, err
}

func (h *humanTracker) byIDReads(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reads[id]
}

// claimOrchestrator is an orchestrator whose tracker is the local store over
// a humanTracker, with the store pulled once: from here on its list reads
// are the local copy, and Edit makes that copy stale.
func claimOrchestrator(t *testing.T, issues ...domain.Issue) (*Orchestrator, *humanTracker, tracker.Tracker) {
	t.Helper()
	o, _ := breakerOrchestrator(t, fallbackOn())
	h := newHumanTracker(issues...)
	st, _, err := store.Wrap(h, store.Config{
		Path: filepath.Join(t.TempDir(), "tracker_store.json"), Scope: "test",
		ActiveStates: o.cfg.Tracker.ActiveStates, SyncInterval: time.Hour,
	})
	require.NoError(t, err)
	o.tracker = st
	_, err = st.FetchCandidateIssues(context.Background()) // the last pull
	require.NoError(t, err)
	return o, h, st
}

func claimHistory(state State, identifier, reason string) []IssueStatusChange {
	var out []IssueStatusChange
	for _, c := range state.IssueStatusHistory[identifier] {
		if c.Reason == reason {
			out = append(out, c)
		}
	}
	return out
}

// TestIssueMovedOutOfActiveStateInTrackerIsNotDispatched: a person closes an
// issue after the store's last pull. The tick's list still says it is active;
// the claim check reads it from the tracker and does not dispatch it, and the
// store's copy is corrected.
func TestIssueMovedOutOfActiveStateInTrackerIsNotDispatched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed, open := breakerIssue("1"), breakerIssue("2")
	o, h, st := claimOrchestrator(t, closed, open)

	h.Edit(closed.ID, "Done")
	stale, err := st.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, stale, 2, "precondition: the local copy still lists the closed issue as active")

	state := o.onTick(ctx, NewState(o.cfg))
	assert.NotContains(t, state.Running, closed.ID, "the issue the person closed is not dispatched")
	assert.Contains(t, state.Running, open.ID, "an unchanged issue is dispatched as usual")
	assert.Positive(t, h.byIDReads(closed.ID), "the tracker was read for it")
	changes := claimHistory(state, closed.Identifier, ClaimReasonChangedInTracker)
	require.Len(t, changes, 1, "the change is in the issue's status history")
	assert.Equal(t, "In Progress", changes[0].FromState)
	assert.Equal(t, "Done", changes[0].ToState)

	after, err := st.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	for _, iss := range after {
		assert.NotEqual(t, closed.ID, iss.ID, "the store's copy is updated by the read")
	}
}

// TestPendingWriteConflictingWithNewerHumanEditIsDropped: Itervox queued a
// state write (the working state, In Progress) that has not reached the
// tracker; a person then closes the issue. At the next admission the claim
// check sees the newer human change: the queued write is dropped, so it can
// never overwrite the person's state, and the issue is not started.
func TestPendingWriteConflictingWithNewerHumanEditIsDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	issue := breakerIssue("1")
	issue.State = "Todo"
	o, h, _ := claimOrchestrator(t, issue)
	ob, err := outbox.New(filepath.Join(t.TempDir(), "outbox.json"))
	require.NoError(t, err)
	o.SetOutbox(ob)
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindUpdateState, IssueID: issue.ID, Identifier: issue.Identifier,
		TargetState: "In Progress", FromState: "Todo",
	}))
	time.Sleep(5 * time.Millisecond) // the person's change comes after the write was queued
	h.Edit(issue.ID, "Done")

	state := o.onTick(ctx, NewState(o.cfg))
	assert.Empty(t, ob.PendingFor(issue.ID), "the conflicting local write is dropped")
	assert.NotContains(t, state.Running, issue.ID, "the issue is not started")
	got, err := h.MemoryTracker.FetchIssueStatesByIDs(ctx, []string{issue.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Done", got[0].State, "the person's state is kept")
	dropped := claimHistory(state, issue.Identifier, ClaimReasonPendingWriteDropped)
	require.Len(t, dropped, 1, "the drop is in the issue's status history")
	assert.Equal(t, "In Progress", dropped[0].FromState)
	assert.Equal(t, "Done", dropped[0].ToState)
}

// TestPendingWriteWithoutHumanEditSurvivesTheClaimCheck: the claim check
// drops only writes a person's later change superseded; a write still on its
// way is kept and seen as the issue's state.
func TestPendingWriteWithoutHumanEditSurvivesTheClaimCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	issue := breakerIssue("1")
	o, _, _ := claimOrchestrator(t, issue)
	ob, err := outbox.New(filepath.Join(t.TempDir(), "outbox.json"))
	require.NoError(t, err)
	o.SetOutbox(ob)
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindUpdateState, IssueID: issue.ID, Identifier: issue.Identifier,
		TargetState: "Done", FromState: "In Progress",
	}))
	state := NewState(o.cfg)
	view, verdict := o.claimCheck(ctx, &state, issue, nil, admitDispatch, claimActive(&state), time.Now())
	assert.Equal(t, claimRefuse, verdict, "the pending Done write is the issue's state")
	assert.Equal(t, "Done", view.State)
	assert.Len(t, ob.PendingFor(issue.ID), 1, "the write is kept")
}

// TestClaimCheckRunsOnEveryAdmissionPath: on each admission path, an issue
// whose local copy says it may start but which a person closed in the
// tracker is read from the tracker and not started.
func TestClaimCheckRunsOnEveryAdmissionPath(t *testing.T) {
	cases := []struct {
		name  string
		admit func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State
	}{
		{admitDispatch, func(ctx context.Context, o *Orchestrator, state State, _ domain.Issue) State {
			return o.onTick(ctx, state)
		}},
		{admitRetry, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.Claimed[issue.ID] = struct{}{}
			state.RetryAttempts[issue.ID] = &RetryEntry{
				IssueID: issue.ID, Identifier: issue.Identifier, Attempt: 1, DueAt: time.Now().Add(-time.Second),
			}
			return o.fireRetries(ctx, state, time.Now())
		}},
		{admitReviewer, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
			return state
		}},
		{admitAutomation, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			started, _ := o.startAutomationRunOrHold(ctx, &state, issue, time.Now(), AutomationDispatch{ProfileName: "coder"})
			assert.False(t, started)
			return state
		}},
		{admitInputResume, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.PendingInputResumes[issue.Identifier] = &PendingInputResumeEntry{
				IssueID: issue.ID, Identifier: issue.Identifier, SessionID: "s1", UserMessage: "go on",
			}
			return o.processPendingInputResumes(ctx, state, time.Now())
		}},
		{admitFallback, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.IssueProfiles[issue.Identifier] = "coder"
			return limitExit(ctx, o, state, issue, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			issue := breakerIssue("1")
			o, h, _ := claimOrchestrator(t, issue)
			ob, err := outbox.New(filepath.Join(t.TempDir(), "outbox.json"))
			require.NoError(t, err)
			o.SetOutbox(ob)
			// A local write queued before the person's change.
			require.NoError(t, ob.Enqueue(outbox.Entry{
				Kind: outbox.KindUpdateState, IssueID: issue.ID, Identifier: issue.Identifier,
				TargetState: "Todo", FromState: "In Progress",
			}))
			time.Sleep(5 * time.Millisecond)
			h.Edit(issue.ID, "Cancelled")
			before := h.byIDReads(issue.ID)

			state := tc.admit(ctx, o, NewState(o.cfg), issue)
			assert.NotContains(t, state.Running, issue.ID, "a closed issue is not started on the %s path", tc.name)
			assert.Greater(t, h.byIDReads(issue.ID), before, "the %s path read the issue from the tracker", tc.name)
			assert.Empty(t, ob.PendingFor(issue.ID), "the %s path dropped the write the person's change superseded", tc.name)
		})
	}
}

// TestClaimCheckAdmitsAReviewerInTheCompletionState: tracker.completion_state
// may be terminal; a reviewer is still admitted there.
func TestClaimCheckAdmitsAReviewerInTheCompletionState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	issue := breakerIssue("1")
	o, h, _ := claimOrchestrator(t, issue)
	o.cfg.Tracker.CompletionState = "Done"
	h.Edit(issue.ID, "Done")
	state := NewState(o.cfg)
	o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
	assert.Contains(t, state.Running, issue.ID, "the reviewer starts in the completion state")
	assert.Equal(t, "Done", state.Running[issue.ID].Issue.State, "with the tracker's current state")
}

// TestClaimReadFailureDefersTheStart: when the claim check cannot read the
// tracker, nothing is lost: an automation is queued (tracker_unavailable), a
// reviewer gets a retry that reads the issue again, and the tick's dispatch
// happens on a later tick once the tracker answers.
func TestClaimReadFailureDefersTheStart(t *testing.T) {
	t.Run("automation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		issue := breakerIssue("1")
		o, h, _ := claimOrchestrator(t, issue)
		h.SetDown(true)
		state := NewState(o.cfg)
		o.dispatchOrQueueAutomation(ctx, &state, issue, AutomationDispatch{AutomationID: "a1", ProfileName: "coder"}, time.Now())
		assert.NotContains(t, state.Running, issue.ID)
		require.Len(t, state.AutomationQueue, 1, "queued, not dropped")
		for _, e := range state.AutomationQueue {
			assert.Equal(t, AutomationQueueReasonTrackerUnavailable, e.Reason)
		}
		h.SetDown(false)
		o.drainAutomationQueue(ctx, &state, time.Now())
		assert.Contains(t, state.Running, issue.ID, "the queue starts it once the tracker answers")
	})

	t.Run("reviewer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		issue := breakerIssue("1")
		o, h, _ := claimOrchestrator(t, issue)
		h.SetDown(true)
		state := NewState(o.cfg)
		o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
		assert.NotContains(t, state.Running, issue.ID)
		require.Contains(t, state.RetryAttempts, issue.ID, "a reviewer retry, not a lost review")
		assert.Equal(t, "reviewer", state.RetryAttempts[issue.ID].ReviewerProfile)
		h.SetDown(false)
		state = o.fireRetries(ctx, state, time.Now().Add(time.Minute))
		require.Contains(t, state.Running, issue.ID, "the retry starts the reviewer")
		assert.Equal(t, "reviewer", state.Running[issue.ID].Kind)
	})

	t.Run("dispatch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		issue := breakerIssue("1")
		o, h, _ := claimOrchestrator(t, issue)
		h.SetDown(true)
		state := o.onTick(ctx, NewState(o.cfg))
		assert.NotContains(t, state.Running, issue.ID)
		h.SetDown(false)
		state = o.onTick(ctx, state)
		assert.Contains(t, state.Running, issue.ID, "the next tick dispatches it")
	})
}

// TestDeletedIssueLeavesTheStoreAtTheClaimCheck: an issue a person deleted
// after the last pull is not started, and the local store stops listing it
// at once, so it is not read again on every tick until the next pull.
func TestDeletedIssueLeavesTheStoreAtTheClaimCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	issue := breakerIssue("1")
	o, h, st := claimOrchestrator(t, issue)
	h.Delete(issue.ID)

	state := o.onTick(ctx, NewState(o.cfg))
	assert.NotContains(t, state.Running, issue.ID)
	cands, err := st.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Empty(t, cands, "the store no longer lists it")
	// The tick's claim read is a detail read; other subsystems (the
	// dependency audit) may read states by ID on their own schedule.
	reads := h.detailReads(issue.ID)
	require.Positive(t, reads)
	_ = o.onTick(ctx, state)
	assert.Equal(t, reads, h.detailReads(issue.ID), "and the next tick does not read it again")
}

// TestReviewerRetryIsAdmittedInTheCompletionState: a reviewer's retry is
// admitted wherever a reviewer is, including a non-active completion state;
// a worker's retry still needs an active state.
func TestReviewerRetryIsAdmittedInTheCompletionState(t *testing.T) {
	for _, tc := range []struct {
		reviewer string
		starts   bool
	}{{"reviewer", true}, {"", false}} {
		t.Run("reviewer="+tc.reviewer, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			issue := breakerIssue("1")
			o, h, _ := claimOrchestrator(t, issue)
			o.cfg.Tracker.CompletionState = "In Review"
			h.Edit(issue.ID, "In Review")
			state := NewState(o.cfg)
			state.Claimed[issue.ID] = struct{}{}
			state.RetryAttempts[issue.ID] = &RetryEntry{
				IssueID: issue.ID, Identifier: issue.Identifier, Attempt: 1,
				DueAt: time.Now().Add(-time.Second), ReviewerProfile: tc.reviewer,
			}
			state = o.fireRetries(ctx, state, time.Now())
			_, running := state.Running[issue.ID]
			assert.Equal(t, tc.starts, running)
		})
	}
}
