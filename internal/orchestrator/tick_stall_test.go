package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// #187 — a slow or hanging tracker must not block the event loop.

// hangingTracker wraps a MemoryTracker. While hang is set, FetchCandidateIssues
// blocks until its context is done (a tracker that never answers), and
// signals entered once per blocked call.
type hangingTracker struct {
	*tracker.MemoryTracker
	hang    atomic.Bool
	entered chan struct{}
	calls   atomic.Int64
}

func newHangingTracker(issues []domain.Issue, cfg *config.Config) *hangingTracker {
	return &hangingTracker{
		MemoryTracker: tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		entered:       make(chan struct{}, 64),
	}
}

func (h *hangingTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	h.calls.Add(1)
	if h.hang.Load() {
		select {
		case h.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.MemoryTracker.FetchCandidateIssues(ctx)
}

func stallTestCfg() *config.Config {
	cfg := refreshTestCfg()
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Agent.MaxTurns = 5
	cfg.Polling.IntervalMs = 50
	return cfg
}

// TestTickReturnsWithinDeadlineWhenTrackerHangs: a candidate fetch that never
// answers costs the tick at most its read deadline, and the failure is
// recorded as a poll outage.
func TestTickReturnsWithinDeadlineWhenTrackerHangs(t *testing.T) {
	cfg := stallTestCfg()
	tr := newHangingTracker(nil, cfg)
	tr.hang.Store(true)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan State, 1)
	start := time.Now()
	go func() { done <- o.onTick(ctx, NewState(cfg)) }()
	select {
	case state := <-done:
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 2*time.Second, "the tick must end at its read deadline")
		assert.Equal(t, 1, state.ConsecutivePollFailures, "the timed-out poll is a failure")
		assert.Equal(t, TrackerErrorOpPoll, state.LastTrackerError.Op)
	case <-time.After(5 * time.Second):
		cancel() // release the hung call so the goroutine exits
		t.Fatal("onTick did not return within its deadline while the tracker hangs")
	}
}

// TestWorkerExitHandledWhileTrackerStalls: while a poll is stuck in the
// tracker, the event loop keeps handling events — a worker's exit is applied
// (its Running entry removed, visible on the snapshot) within seconds.
func TestWorkerExitHandledWhileTrackerStalls(t *testing.T) {
	cfg := stallTestCfg()
	tr := newHangingTracker([]domain.Issue{
		{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"},
	}, cfg)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	// A deadline far longer than the test: only an off-loop poll can pass.
	o.trackerReadBudgetOverride = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = o.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()

	require.Eventually(t, func() bool {
		_, ok := o.Snapshot().Running["id1"]
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the issue is dispatched first")

	tr.hang.Store(true)
	select {
	case <-tr.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never reached the hanging tracker")
	}

	// The worker exits while the poll is stuck.
	require.NoError(t, o.CancelIssue("ENG-1"))
	require.Eventually(t, func() bool {
		_, ok := o.Snapshot().Running["id1"]
		return !ok
	}, 3*time.Second, 10*time.Millisecond,
		"the worker's exit must be handled while the tracker poll is stalled")
}

// TestRateLimitWaitFailsFastUnderTickDeadline: a candidate fetch that hits a
// 429 whose Retry-After (50 s) outlasts the poll's read deadline fails fast
// with *tracker.RateLimitedError (exceedsBudget sees the deadline) instead of
// waiting it out, and the tick records it as a rate limit.
func TestRateLimitWaitFailsFastUnderTickDeadline(t *testing.T) {
	const adapter = "tick-deadline-187"
	defer tracker.SharedRateLimitGate().Clear(adapter)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "50")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := stallTestCfg()
	tr := &rateLimitedPollTracker{
		MemoryTracker: tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		url:           srv.URL, client: srv.Client(), adapter: adapter,
	}
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan State, 1)
	start := time.Now()
	go func() { done <- o.onTick(ctx, NewState(cfg)) }()
	select {
	case state := <-done:
		assert.Less(t, time.Since(start), 2*time.Second, "the wait must not even start: it cannot fit the deadline")
		assert.Equal(t, TrackerErrorKindRateLimited, state.LastTrackerError.Kind)
		assert.Equal(t, int64(1), hits.Load(), "no request is re-sent into the published window")
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the tick waited out the rate limit instead of failing fast under its deadline")
	}
}

type rateLimitedPollTracker struct {
	*tracker.MemoryTracker
	url     string
	client  *http.Client
	adapter string
}

func (r *rateLimitedPollTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := tracker.DoWithRateLimitRetry(ctx, r.client, req, r.adapter, func(resp *http.Response) (bool, time.Time) {
		return resp.StatusCode == http.StatusTooManyRequests, time.Time{}
	})
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return nil, nil
}

// breakerTestOrchestrator returns an orchestrator over a hanging tracker
// whose every poll times out after 20 ms.
func breakerTestOrchestrator(t *testing.T) (*Orchestrator, *hangingTracker) {
	t.Helper()
	cfg := stallTestCfg()
	tr := newHangingTracker(nil, cfg)
	tr.hang.Store(true)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = 20 * time.Millisecond
	return o, tr
}

// TestTrackerBreakerSkipsReadsAfterTimeouts: TrackerBreakerThreshold polls in
// a row that time out open the breaker; the next tick reads nothing from the
// tracker (no poll, and a due retry stays due without a read), and the poll
// is reported degraded.
func TestTrackerBreakerSkipsReadsAfterTimeouts(t *testing.T) {
	o, tr := breakerTestOrchestrator(t)
	ctx := context.Background()
	state := NewState(o.cfg)
	for range TrackerBreakerThreshold {
		state = o.onTick(ctx, state)
	}
	require.True(t, state.TrackerBreaker.Open(time.Now()), "the breaker opens after %d timeouts", TrackerBreakerThreshold)
	assert.True(t, o.Readiness().PollDegraded)
	assert.Equal(t, int64(TrackerBreakerThreshold), tr.calls.Load())

	state = ScheduleRetry(state, "id9", 1, "ENG-9", "boom", time.Now().Add(-time.Second), 0)
	state = o.onTick(ctx, state)
	assert.Equal(t, int64(TrackerBreakerThreshold), tr.calls.Load(), "an open breaker skips the poll")
	retry := state.RetryAttempts["id9"]
	require.NotNil(t, retry, "a due retry stays due while the breaker is open")
	assert.Equal(t, 1, retry.Attempt, "and spends no attempt")
	assert.True(t, o.Readiness().PollDegraded)
	assert.False(t, o.Readiness().LastPollOK)
}

// TestTrackerBreakerRecovers: after the cooldown the next poll probes the
// tracker; a probe that fails re-opens the breaker, one that succeeds closes
// it and clears the degraded flag.
func TestTrackerBreakerRecovers(t *testing.T) {
	o, tr := breakerTestOrchestrator(t)
	o.trackerBreakerCooldownOverride = 50 * time.Millisecond
	ctx := context.Background()
	state := NewState(o.cfg)
	for range TrackerBreakerThreshold {
		state = o.onTick(ctx, state)
	}
	require.True(t, state.TrackerBreaker.Open(time.Now()))
	firstOpenUntil := state.TrackerBreaker.OpenUntil

	time.Sleep(60 * time.Millisecond)
	state = o.onTick(ctx, state) // probe, still timing out
	assert.Equal(t, int64(TrackerBreakerThreshold+1), tr.calls.Load(), "the cooldown's end lets one probe through")
	assert.True(t, state.TrackerBreaker.OpenUntil.After(firstOpenUntil), "a failed probe re-opens the breaker")
	assert.True(t, state.TrackerBreaker.Degraded())

	time.Sleep(60 * time.Millisecond)
	tr.hang.Store(false)
	state = o.onTick(ctx, state) // probe, tracker healthy
	assert.Equal(t, int64(TrackerBreakerThreshold+2), tr.calls.Load())
	assert.False(t, state.TrackerBreaker.Degraded(), "a successful probe closes the breaker")
	assert.Zero(t, state.TrackerBreaker.ConsecutiveTransportFailures)
	r := o.Readiness()
	assert.False(t, r.PollDegraded)
	assert.True(t, r.LastPollOK)
}

// TestStaleCandidateResultDoesNotRedispatch: a run that ends while the poll
// is in flight is not dispatched again from that poll's list, even when the
// list (and an eventually-consistent claim read) still shows the issue
// active — the list may predate the state move the run made.
func TestStaleCandidateResultDoesNotRedispatch(t *testing.T) {
	cfg := stallTestCfg()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	tr := newHangingTracker([]domain.Issue{issue}, cfg)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	ctx := context.Background()
	state := NewState(cfg)
	state.Running["id1"] = &RunEntry{Issue: issue, RunID: "r1", StartedAt: time.Now()}

	now := time.Now()
	req := o.planTrackerPoll(&state, now)
	require.NotNil(t, req)
	o.armTrackerPoll(&state, req)
	res := o.runTrackerPoll(ctx, req)
	require.NoError(t, res.FetchErr)
	require.Len(t, res.Issues, 1, "the poll's list still shows the issue active")

	// The run ends while the poll is in flight.
	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: "id1", RunID: "r1"})
	require.NotContains(t, state.Running, "id1")

	state = o.applyTrackerPoll(ctx, state, res, claimsOffLoop)
	o.trackerReadsWg.Wait()
	assert.NotContains(t, state.Running, "id1", "the stale list must not restart the issue")
	assert.NotContains(t, state.ClaimChecks, "id1", "nor pick it for a claim read")
	assert.False(t, state.TrackerPoll.InFlight)
	assert.Empty(t, state.TrackerPoll.EndedDuringPoll, "the set is per poll")
}

// TestStaleReconcileResultSkipsReplacedRun: the poll's state read for a run
// that was replaced while the read was in flight does not stop the new run.
func TestStaleReconcileResultSkipsReplacedRun(t *testing.T) {
	cfg := stallTestCfg()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	tr := newHangingTracker([]domain.Issue{issue}, cfg)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	ctx := context.Background()
	state := NewState(cfg)
	state.Running["id1"] = &RunEntry{Issue: issue, RunID: "r1", StartedAt: time.Now()}

	req := o.planTrackerPoll(&state, time.Now())
	require.NotNil(t, req)
	o.armTrackerPoll(&state, req)
	tr.SetIssueState("id1", "Done")
	res := o.runTrackerPoll(ctx, req)
	require.NoError(t, res.RefreshErr)

	// r1 ended and r2 started while the read was in flight.
	state.Running["id1"] = &RunEntry{Issue: issue, RunID: "r2", StartedAt: time.Now()}
	state = o.applyTrackerPoll(ctx, state, res, claimsOffLoop)
	o.trackerReadsWg.Wait()
	require.Contains(t, state.Running, "id1", "a read taken for r1 must not stop r2")
	assert.Equal(t, "r2", state.Running["id1"].RunID)
}

// TestTrackerPollWatchdogDropsLateResult: a poll unanswered past its
// deadline plus grace has its latch released (counted as a poll failure),
// and its late result is dropped.
func TestTrackerPollWatchdogDropsLateResult(t *testing.T) {
	cfg := stallTestCfg()
	tr := newHangingTracker(nil, cfg)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = time.Second
	state := NewState(cfg)
	req := o.planTrackerPoll(&state, time.Now())
	require.NotNil(t, req)
	o.armTrackerPoll(&state, req)
	res := o.runTrackerPoll(context.Background(), req)

	o.reclaimStuckTrackerReads(&state, time.Now().Add(time.Second+trackerReadWatchdogGrace+time.Second))
	assert.False(t, state.TrackerPoll.InFlight, "the watchdog releases the latch")
	assert.Equal(t, 1, state.ConsecutivePollFailures)

	// The next tick's poll is in flight when the abandoned one answers.
	req2 := o.planTrackerPoll(&state, time.Now())
	require.NotNil(t, req2)
	o.armTrackerPoll(&state, req2)
	before := state.TrackerPoll
	state = o.applyTrackerPoll(context.Background(), state, res, claimsOffLoop)
	assert.True(t, state.TrackerPoll.InFlight, "the late result must not clear the new poll's latch")
	assert.Equal(t, req2.Generation, state.TrackerPoll.Generation)
	assert.Equal(t, before.LastFinishedAt, state.TrackerPoll.LastFinishedAt, "the late result is dropped")
	assert.Equal(t, 1, state.ConsecutivePollFailures)
}

// claimHangTracker's FetchIssueDetail blocks until release is closed (or its
// context is done); it can also report a state other than the list's.
type claimHangTracker struct {
	*tracker.MemoryTracker
	entered     chan struct{}
	release     chan struct{}
	detailState string
	details     atomic.Int64
}

func (c *claimHangTracker) FetchIssueDetail(ctx context.Context, id string) (*domain.Issue, error) {
	c.details.Add(1)
	select {
	case c.entered <- struct{}{}:
	default:
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	issue, err := c.MemoryTracker.FetchIssueDetail(ctx, id)
	if err == nil && issue != nil && c.detailState != "" {
		cp := *issue
		cp.State = c.detailState
		return &cp, nil
	}
	return issue, err
}

func runOrchestrator(t *testing.T, o *Orchestrator) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = o.Run(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
}

// TestClaimCheckDoesNotBlockLoop: a dispatch pick's claim read runs off the
// loop. While it hangs the loop handles events, the pick holds its slot, and
// no worker starts; once the read answers, the issue starts.
func TestClaimCheckDoesNotBlockLoop(t *testing.T) {
	cfg := stallTestCfg()
	tr := &claimHangTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
			cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = time.Hour
	runOrchestrator(t, o)

	select {
	case <-tr.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the claim read never started")
	}
	require.True(t, o.RecordFailure(FailureRecord{Kind: FailureKindClient, Source: "claim-probe-187", Message: "probe"}))
	require.Eventually(t, func() bool {
		for _, f := range o.Snapshot().RecentFailures {
			if f.Source == "claim-probe-187" {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "the loop must handle events while the claim read hangs")
	snap := o.Snapshot()
	assert.Contains(t, snap.ClaimChecks, "id1", "the pick holds its slot while its read is in flight")
	assert.NotContains(t, snap.Running, "id1", "no worker starts before its claim read")

	close(tr.release)
	require.Eventually(t, func() bool {
		s := o.Snapshot()
		_, running := s.Running["id1"]
		_, pending := s.ClaimChecks["id1"]
		return running && !pending
	}, 5*time.Second, 10*time.Millisecond, "the claim read admits the issue and it starts")
}

// TestClaimCheckOffLoopRefusesIssueMovedInTracker: #116 holds off the loop —
// an issue the list shows active but the claim read shows done is not
// started, and its reservation is released.
func TestClaimCheckOffLoopRefusesIssueMovedInTracker(t *testing.T) {
	cfg := stallTestCfg()
	tr := &claimHangTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
			cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		entered:     make(chan struct{}, 8),
		detailState: "Done",
	}
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	runOrchestrator(t, o)

	require.Eventually(t, func() bool { return tr.details.Load() >= 3 }, 5*time.Second, 10*time.Millisecond,
		"each poll picks the issue and reads it")
	require.Eventually(t, func() bool { return len(o.Snapshot().ClaimChecks) == 0 }, 3*time.Second, 10*time.Millisecond)
	assert.NotContains(t, o.Snapshot().Running, "id1", "an issue done in the tracker is never started")
}

// TestSlowTickIsReported: a poll that runs past SlowPollThreshold is logged
// at Warn while it is still running, and the snapshot shows it in flight.
func TestSlowTickIsReported(t *testing.T) {
	cfg := stallTestCfg()
	tr := newHangingTracker(nil, cfg)
	tr.hang.Store(true)
	o := New(cfg, tr, &agenttest.FakeRunner{Stall: true}, nil)
	o.trackerReadBudgetOverride = time.Hour
	buf := &syncBuffer{}
	o.Logger = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runOrchestrator(t, o)

	threshold := SlowPollThreshold(time.Duration(cfg.Polling.IntervalMs) * time.Millisecond)
	require.Eventually(t, func() bool {
		return len(recordsMatching(t, buf, "tracker poll is running longer than the poll interval")) > 0
	}, threshold+5*time.Second, 20*time.Millisecond, "a slow poll is reported while it runs")
	recs := recordsMatching(t, buf, "tracker poll is running longer than the poll interval")
	assert.Len(t, recs, 1, "once per poll")
	assert.Equal(t, "WARN", recs[0].Level)
	snap := o.Snapshot()
	assert.True(t, snap.TrackerPoll.InFlight)
	assert.Greater(t, time.Since(snap.TrackerPoll.StartedAt), threshold)
	assert.False(t, o.Readiness().PollStarted.IsZero())
}

// TestClaimChecksHoldDispatchSlots: a pick whose claim read is in flight
// holds its slot and is itself ineligible, so a poll never picks more issues
// than there are slots and never picks one twice.
func TestClaimChecksHoldDispatchSlots(t *testing.T) {
	cfg := stallTestCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	state := NewState(cfg)
	one := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	two := domain.Issue{ID: "id2", Identifier: "ENG-2", Title: "T", State: "Todo"}
	require.Equal(t, 1, AvailableSlots(state))
	reserveClaimCheck(&state, one, 1, time.Now())
	assert.Equal(t, 0, AvailableSlots(state), "the reservation takes the only slot")
	assert.Equal(t, IneligibleClaimCheckPending, IneligibleReason(one, state, cfg))
	assert.Equal(t, "no_slots", IneligibleReason(two, state, cfg))
}
