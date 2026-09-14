package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestBackoffMsContinuation(t *testing.T) {
	assert.Equal(t, 10000, orchestrator.BackoffMs(1, 300000))
}

func TestBackoffMsAttempt2(t *testing.T) {
	assert.Equal(t, 20000, orchestrator.BackoffMs(2, 300000))
}

func TestBackoffMsAttempt3(t *testing.T) {
	assert.Equal(t, 40000, orchestrator.BackoffMs(3, 300000))
}

func TestBackoffMsCappedAtMax(t *testing.T) {
	assert.Equal(t, 5000, orchestrator.BackoffMs(10, 5000))
}

func TestScheduleRetry(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	now := time.Now()
	state = orchestrator.ScheduleRetry(state, "id1", 1, "ENG-1", "some error", now, 10000)
	entry, ok := state.RetryAttempts["id1"]
	assert.True(t, ok)
	assert.Equal(t, "id1", entry.IssueID)
	assert.Equal(t, 1, entry.Attempt)
	assert.NotNil(t, entry.Error)
	assert.Contains(t, *entry.Error, "some error")
	_, claimed := state.Claimed["id1"]
	assert.True(t, claimed)
}

func TestCancelRetry(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	now := time.Now()
	state = orchestrator.ScheduleRetry(state, "id1", 1, "ENG-1", "", now, 10000)
	state = orchestrator.CancelRetry(state, "id1")
	_, ok := state.RetryAttempts["id1"]
	assert.False(t, ok)
	_, claimed := state.Claimed["id1"]
	assert.False(t, claimed)
}

// alwaysFailRunner is a test double that always returns a failed result with
// non-zero tokens so the worker treats it as a real failure (not a concluded session).
// callCount is atomic because RunTurn fires from a worker goroutine while
// tests read the count from the test goroutine.
type alwaysFailRunner struct {
	callCount atomic.Int64
}

func (r *alwaysFailRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.callCount.Add(1)
	return agent.TurnResult{
		Failed:       true,
		FailureText:  "simulated failure",
		InputTokens:  100,
		OutputTokens: 50,
		TotalTokens:  150,
	}, nil
}

func TestMaxRetriesExhaustedPausesIssue(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 2
	cfg.Agent.MaxRetryBackoffMs = 10 // minimal backoff so retries fire quickly

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &alwaysFailRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for the issue to end up paused after exhausting retries.
	// MaxRetries=2 means: attempt 0 (initial) fails, retry attempt 1 fires,
	// then attempt 2 fires — at that point nextAttempt=3 > maxRetries=2 => paused.
	deadline := time.After(4 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			// Issue is paused — max retries exhausted as expected.
			// Verify it's not in the retry queue anymore.
			_, inRetry := snap.RetryAttempts["id1"]
			assert.False(t, inRetry, "issue should not be in retry queue after max retries exhausted")
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue was not paused within timeout — max retries may not be working")
		case <-time.After(50 * time.Millisecond):
			// Poll again.
		}
	}
}

func TestMaxRetriesZeroMeansUnlimited(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 0 // unlimited
	cfg.Agent.MaxRetryBackoffMs = 10

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &alwaysFailRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// With unlimited retries, after enough time the runner should have been
	// called multiple times and the issue should still be retrying (not paused).
	time.Sleep(1 * time.Second)

	snap := orch.Snapshot()
	_, paused := snap.PausedIdentifiers["ENG-1"]
	assert.False(t, paused, "issue should NOT be paused when max_retries=0 (unlimited)")
	require.Greater(t, int(runner.callCount.Load()), 2, "runner should have been called multiple times with unlimited retries")
}

func TestMaxRetriesExhaustedWithFailedState(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Tracker.FailedState = "Backlog"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &alwaysFailRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for the issue to be transitioned to the failed state.
	// MaxRetries=1 means: attempt 0 (initial) fails, nextAttempt=1 which is NOT > 1,
	// so retry fires. Then attempt 1 fails, nextAttempt=2 > 1 => max exhausted.
	deadline := time.After(4 * time.Second)
	for {
		// Check if the tracker reflects the state change.
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		if len(issues) > 0 && issues[0].State == "Backlog" {
			// Issue was transitioned to the failed state.
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue was not transitioned to failed state within timeout")
		case <-time.After(50 * time.Millisecond):
			// Poll again.
		}
	}
}
