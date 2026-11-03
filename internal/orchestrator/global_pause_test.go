package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// pauseTestOrch is an orchestrator over one active issue (ENG-1) whose
// runner parks every turn, plus a reviewer profile.
func pauseTestOrch(t *testing.T) (*Orchestrator, *gatedRunner, domain.Issue, context.Context) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles["reviewer"] = config.AgentProfile{Command: "claude"}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		close(rr.release)
		o.workersWg.Wait(5 * time.Second)
	})
	return o, rr, issue, ctx
}

func pausedState(o *Orchestrator, paused bool) State {
	state := NewState(o.cfg)
	state.MaxConcurrentAgents = 3
	state.ActiveStates = o.cfg.Tracker.ActiveStates
	state.TerminalStates = o.cfg.Tracker.TerminalStates
	if paused {
		state.DispatchPaused = DispatchPause{Active: true, Reason: "deploy", PausedAt: time.Now()}
	}
	return state
}

// TestPauseBlocksEveryAdmissionPath (#213): while all dispatch is paused
// nothing new starts on any admission path — dispatch, retries, reviewers,
// automations and pending-input resumes — and the work is held, not
// dropped. Each path runs a control (not paused: the work starts) so the
// paused case cannot pass vacuously.
func TestPauseBlocksEveryAdmissionPath(t *testing.T) {
	for _, paused := range []bool{false, true} {
		name := map[bool]string{false: "control", true: "paused"}[paused]

		t.Run("dispatch/"+name, func(t *testing.T) {
			o, _, issue, ctx := pauseTestOrch(t)
			state := pausedState(o, paused)
			if paused {
				assert.Equal(t, IneligibleDispatchPaused, IneligibleReason(issue, state, o.cfg), "a tick pick is ineligible")
				assert.Equal(t, 0, AvailableSlots(state))
			} else {
				assert.Empty(t, IneligibleReason(issue, state, o.cfg))
			}
			state = o.dispatch(ctx, state, issue, 0)
			if paused {
				assert.Empty(t, state.Running, "no worker starts while paused")
				require.Contains(t, state.RetryAttempts, issue.ID, "the dispatch is held as a retry")
				assert.Equal(t, "dispatch paused", *state.RetryAttempts[issue.ID].Error)
				assert.Contains(t, state.Claimed, issue.ID, "the claim is kept")
			} else {
				assert.Len(t, state.Running, 1, "control: the worker starts")
			}
		})

		t.Run("retry/"+name, func(t *testing.T) {
			o, _, issue, ctx := pauseTestOrch(t)
			state := pausedState(o, paused)
			state = ScheduleRetry(state, issue.ID, 2, issue.Identifier, "boom", time.Now().Add(-time.Second), 0)
			due := state.RetryAttempts[issue.ID].DueAt
			state = o.fireRetries(ctx, state, time.Now())
			if paused {
				assert.Empty(t, state.Running, "no retry starts while paused")
				require.Contains(t, state.RetryAttempts, issue.ID, "the retry stays due")
				r := state.RetryAttempts[issue.ID]
				assert.Equal(t, 2, r.Attempt, "no attempt is spent")
				// Untouched: no tracker read was made and nothing was rescheduled.
				assert.Equal(t, "boom", *r.Error)
				assert.True(t, r.DueAt.Equal(due), "still due, fires on the first tick after the resume")
			} else {
				assert.Len(t, state.Running, 1, "control: the retry starts")
			}
		})

		t.Run("reviewer/"+name, func(t *testing.T) {
			o, _, issue, ctx := pauseTestOrch(t)
			state := pausedState(o, paused)
			o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
			if !paused {
				assert.Len(t, state.Running, 1, "control: the reviewer starts")
				return
			}
			assert.Empty(t, state.Running, "no reviewer starts while paused")
			require.Contains(t, state.PendingReviews, issue.Identifier, "the review is kept")
			assert.Equal(t, IneligibleDispatchPaused, state.PendingReviews[issue.Identifier].Reason)
			o.resumePendingReviews(ctx, &state, time.Now())
			assert.Empty(t, state.Running, "the pending review waits while paused")
			// Resumed: the pending review is dispatched.
			state.DispatchPaused = DispatchPause{}
			o.resumePendingReviews(ctx, &state, time.Now())
			require.Len(t, state.Running, 1, "the review starts once the pause ends")
			assert.Equal(t, "reviewer", state.Running[issue.ID].Kind)
		})

		t.Run("automation/"+name, func(t *testing.T) {
			o, _, issue, ctx := pauseTestOrch(t)
			state := pausedState(o, paused)
			dispatch := AutomationDispatch{AutomationID: "a1", ProfileName: "responder", Trigger: AutomationTriggerContext{Type: "manual"}}
			started := o.startAutomationRun(ctx, &state, issue, time.Now(), dispatch)
			if !paused {
				require.True(t, started, "control: the automation starts")
				return
			}
			assert.False(t, started, "no automation starts while paused")
			assert.Empty(t, state.Running)
			accepted := o.dispatchOrQueueAutomation(ctx, &state, issue, dispatch, time.Now())
			assert.True(t, accepted, "the automation is queued, not dropped")
			require.Len(t, state.AutomationQueue, 1)
			for _, e := range state.AutomationQueue {
				assert.Equal(t, AutomationQueueReasonDispatchPaused, e.Reason)
			}
			assert.Empty(t, state.Running)
		})

		t.Run("input_resume/"+name, func(t *testing.T) {
			o, _, _, ctx := pauseTestOrch(t)
			state := pausedState(o, paused)
			state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
				IssueID: "id1", Identifier: "ENG-1", Context: "Need approval",
				UserMessage: "Approved.", QueuedAt: time.Now(),
			}
			state = o.processPendingInputResumes(ctx, state, time.Now())
			if paused {
				assert.Empty(t, state.Running, "no pending-input resume starts while paused")
				assert.Contains(t, state.PendingInputResumes, "ENG-1", "the reply stays queued")
			} else {
				require.Len(t, state.Running, 1, "control: the resume starts")
			}
		})
	}

	// Running agents continue: a pause applied while a worker runs neither
	// stops it nor prevents its exit from being handled.
	t.Run("running_agents_continue", func(t *testing.T) {
		o, rr, issue, ctx := pauseTestOrch(t)
		state := pausedState(o, false)
		state = o.dispatch(ctx, state, issue, 0)
		require.Len(t, state.Running, 1)
		o.pauseDispatch(&state, "incident", time.Time{}, time.Now())
		require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond)
		assert.Len(t, state.Running, 1, "the pause does not touch the running worker")
		deadline := time.After(150 * time.Millisecond)
		for {
			select {
			case ev := <-o.events:
				assert.NotEqual(t, EventWorkerExited, ev.Type, "the pause must not stop the running agent")
				continue
			case <-deadline:
			}
			break
		}
	})
}

// TestPauseSurvivesRestartAndExpiresAtUntil (#213): a pause set through
// the event loop persists to dispatch_paused.json; a restarted daemon comes
// back paused and dispatches nothing, and the pause ends by itself at its
// until — with a dispatch_resumed feed event — after which the waiting issue
// starts.
func TestPauseSurvivesRestartAndExpiresAtUntil(t *testing.T) {
	dir := t.TempDir()
	pausePath := filepath.Join(dir, "dispatch_paused.json")
	feedPath := filepath.Join(dir, "feed.jsonl")
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}

	// First daemon: no issues, pause through the loop, shut down.
	empty := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	first := New(cfg, empty, nil, nil)
	first.SetDispatchPauseFile(pausePath)
	first.SetFeedFile(feedPath)
	_, err := first.PauseDispatch(context.Background(), "x", time.Time{})
	require.ErrorIs(t, err, errLoopNotRunning, "only the loop applies a pause")
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { defer close(done1); _ = first.Run(ctx1) }()
	require.Eventually(t, func() bool { return first.loopExitedCh() != nil }, 5*time.Second, time.Millisecond)
	_, err = first.PauseDispatch(ctx1, "deploy", time.Now().Add(-time.Minute))
	require.ErrorIs(t, err, ErrPauseUntilPast)
	until := time.Now().Add(700 * time.Millisecond)
	applied, err := first.PauseDispatch(ctx1, "deploy freeze", until)
	require.NoError(t, err)
	assert.True(t, applied.Active)
	assert.Equal(t, "deploy freeze", applied.Reason)
	assert.True(t, applied.Until.Equal(until))
	cancel1()
	<-done1
	var onDisk dispatchPauseEnvelope
	data, err := os.ReadFile(pausePath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.True(t, onDisk.Active, "the pause is on disk")
	assert.Equal(t, "deploy freeze", onDisk.Reason)

	// Second daemon: an eligible issue waits, the pause is restored.
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})}
	second := New(cfg, mt, rr, nil)
	second.SetDispatchPauseFile(pausePath)
	second.SetFeedFile(feedPath)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = second.Run(ctx2) }()
	defer func() {
		cancel2()
		close(rr.release)
		<-done2
	}()
	require.Eventually(t, func() bool { return second.Snapshot().DispatchPaused.Active }, 5*time.Second, 5*time.Millisecond,
		"the restarted daemon is paused")
	time.Sleep(150 * time.Millisecond) // several ticks
	if time.Now().Before(until) {
		assert.Equal(t, 0, rr.count(), "nothing dispatched before until")
	}

	require.Eventually(t, func() bool { return !second.Snapshot().DispatchPaused.Active }, 5*time.Second, 5*time.Millisecond,
		"the pause ends at its until")
	assert.False(t, time.Now().Before(until), "not before until")
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond,
		"the waiting issue starts once the pause ended")
	page := second.Feed(0, 0)
	kinds := make([]string, 0, len(page.Events))
	for _, e := range page.Events {
		kinds = append(kinds, e.Kind)
		if e.Kind == FeedDispatchResumed {
			assert.Equal(t, "expired", e.Payload.Via)
		}
	}
	assert.Equal(t, []string{FeedDispatchPaused, FeedDispatchResumed, FeedRunStarted}, kinds)

	// Resume through the loop is idempotent; a new pause applies at once.
	p, err := second.ResumeDispatch(ctx2)
	require.NoError(t, err)
	assert.False(t, p.Active)
	p, err = second.PauseDispatch(ctx2, "", time.Time{})
	require.NoError(t, err)
	assert.True(t, p.Active)
	assert.True(t, p.Until.IsZero(), "no until: paused until resumed")
}
