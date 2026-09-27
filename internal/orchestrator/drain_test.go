package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-057 — graceful drain. RequestDrain only sends EventDrain; the event
// loop alone sets State.Draining, stops every admission path and closes
// Drained() once no worker is running.

// gatedRunner parks every turn until release is closed (or its context
// ends) and counts the turns it was asked to start.
type gatedRunner struct {
	mu      sync.Mutex
	started int
	release chan struct{}
}

func (r *gatedRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.started++
	r.mu.Unlock()
	select {
	case <-r.release:
		return agent.TurnResult{}, nil
	case <-ctx.Done():
		return agent.TurnResult{Failed: true}, ctx.Err()
	}
}

func (r *gatedRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

func TestRequestDrainOnlySendsEvent(t *testing.T) {
	cfg := automationBaseCfg()
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)

	require.NoError(t, o.RequestDrain())

	require.Len(t, o.events, 1, "RequestDrain sends exactly one event")
	ev := <-o.events
	assert.Equal(t, EventDrain, ev.Type)
	// Nothing but the event loop may set the drain state: without a running
	// loop, neither the snapshot nor the lock-free readiness flag moved.
	assert.False(t, o.Snapshot().Draining, "RequestDrain must not touch State")
	assert.False(t, o.Readiness().Draining, "RequestDrain must not publish the flag itself")
	select {
	case <-o.Drained():
		t.Fatal("Drained() closed without the event loop")
	default:
	}

	// The loop applies it: a real State value goes in, Draining comes out.
	state := NewState(cfg)
	state = o.handleEvent(context.Background(), state, ev)
	assert.True(t, state.Draining)
	assert.True(t, o.Readiness().Draining)
	assert.Equal(t, 0, AvailableSlots(state), "a draining loop admits nothing")
}

func TestDrainStopsDispatch(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetryBackoffMs = 50
	cfg.Polling.IntervalMs = 20
	issues := []domain.Issue{breakerIssue("1"), breakerIssue("2"), breakerIssue("3")}
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	defer func() { cancel(); <-done }()

	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond,
		"one worker starts before the drain")

	require.NoError(t, o.RequestDrain())
	require.Eventually(t, func() bool { return o.Readiness().Draining && o.Snapshot().Draining },
		5*time.Second, 5*time.Millisecond, "the loop applies the drain")

	// HTTP admission controls answer ErrDraining instead of queueing work.
	assert.ErrorIs(t, o.ProvideInput("ENG-2", "hi"), ErrDraining)
	assert.ErrorIs(t, o.ResumeIssue("ENG-2"), ErrDraining)
	assert.ErrorIs(t, o.ReanalyzeIssue("ENG-2"), ErrDraining)
	assert.ErrorIs(t, o.DispatchReviewer("ENG-2"), ErrDraining)

	select {
	case <-o.Drained():
		t.Fatal("Drained() closed while a worker is still running")
	case <-time.After(100 * time.Millisecond):
	}

	// The in-flight turn finishes normally (its context was never cancelled).
	close(rr.release)
	select {
	case <-o.Drained():
	case <-time.After(5 * time.Second):
		t.Fatal("Drained() never closed after the last worker exited")
	}

	// Many ticks (and any continuation retry) later, nothing new started,
	// although two eligible issues are waiting and a slot is free.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, 1, rr.count(), "no dispatch, retry or resume after the drain began")
	assert.Empty(t, o.Snapshot().Running)
	assert.False(t, errors.Is(ctx.Err(), context.Canceled), "the drain never cancelled the loop context")
}
