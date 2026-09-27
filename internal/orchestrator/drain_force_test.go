package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// M4-close D1 — a forced stop (loop ctx cancelled while a turn is running)
// must still record the killed turn's exit: the loop outlives its workers
// long enough (bounded) to consume EventWorkerExited, so the run lands in
// history and its retry/partial-handoff bookkeeping is applied and persisted.
func TestForcedStopRecordsKilledTurnExit(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	mt := tracker.NewMemoryTracker([]domain.Issue{breakerIssue("1")}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})} // never released: the turn is killed
	o := New(cfg, mt, rr, nil)
	o.SetWorkerJoinGrace(10 * time.Second)
	historyPath := filepath.Join(t.TempDir(), "history.json")
	o.SetHistoryFile(historyPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()

	require.Eventually(t, func() bool { return rr.count() == 1 && len(o.Snapshot().Running) == 1 },
		5*time.Second, 5*time.Millisecond, "the turn starts")
	require.NoError(t, o.RequestDrain())
	require.Eventually(t, func() bool { return o.Snapshot().Draining }, 5*time.Second, 5*time.Millisecond)

	cancel() // grace expired / second signal: force the stop
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the forced stop")
	}

	snap := o.Snapshot()
	assert.Empty(t, snap.Running, "the killed turn's exit was consumed by the loop")
	found := false
	for _, r := range o.RunHistory() {
		if r.Identifier == "ENG-1" {
			found = true
		}
	}
	assert.True(t, found, "the killed turn is recorded in run history")
	data, err := os.ReadFile(historyPath)
	require.NoError(t, err, "the shutdown flush persisted history")
	assert.Contains(t, string(data), `"ENG-1"`)
	assert.Contains(t, string(data), `"cancelled"`)
	assert.Equal(t, 1, rr.count(), "nothing new started while collecting exits")
}

// M4-close D2 — a drain requested before the loop's first iteration must win
// over the immediate first tick: no turn is ever admitted after a drain
// request, regardless of timing.
func TestDrainBeforeFirstTickAdmitsNothing(t *testing.T) {
	const iterations = 200
	admitted := 0
	for i := 0; i < iterations; i++ {
		cfg := automationBaseCfg()
		cfg.Agent.MaxConcurrentAgents = 1
		cfg.Agent.MaxTurns = 1
		cfg.Polling.IntervalMs = 5
		mt := tracker.NewMemoryTracker([]domain.Issue{breakerIssue("1")}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
		rr := &gatedRunner{release: make(chan struct{})}
		o := New(cfg, mt, rr, nil)
		o.SetWorkerJoinGrace(time.Second)
		require.NoError(t, o.RequestDrain()) // queued before Run
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = o.Run(ctx) }()
		require.Eventually(t, func() bool { return o.Snapshot().Draining }, 5*time.Second, time.Millisecond,
			"the loop applies the drain")
		time.Sleep(15 * time.Millisecond) // several poll intervals
		if rr.count() > 0 {
			admitted++
		}
		close(rr.release)
		cancel()
		<-done
	}
	assert.Equal(t, 0, admitted, "turns admitted after a drain was requested before the first tick: %d/%d", admitted, iterations)
}
