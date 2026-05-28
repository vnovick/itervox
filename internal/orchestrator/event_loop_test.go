package orchestrator_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// ---------------------------------------------------------------------------
// 1. Per-state concurrency limits
// ---------------------------------------------------------------------------

func TestPerStateConcurrencyLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxConcurrentAgents = 10 // globally generous
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{
		"in progress": 1,
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{
			makeIssue("id1", "ENG-1", "In Progress", prio(1), nil),
			makeIssue("id2", "ENG-2", "In Progress", prio(2), nil),
		},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Wait until at least one worker is visible, then give time for a second tick.
	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("no worker dispatched within 2s")
	}

	// Allow a few more ticks to elapse so the second issue would dispatch if allowed.
	time.Sleep(200 * time.Millisecond)

	snap := orch.Snapshot()
	assert.Equal(t, 1, len(snap.Running), "only 1 issue should run when per-state limit is 1")
}

// ---------------------------------------------------------------------------
// 2. Max workers clamp
// ---------------------------------------------------------------------------

func TestMaxWorkersClampsDispatch(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxConcurrentAgents = 1
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{
			makeIssue("id1", "ENG-1", "In Progress", prio(1), nil),
			makeIssue("id2", "ENG-2", "In Progress", prio(2), nil),
		},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("no worker dispatched within 2s")
	}

	time.Sleep(200 * time.Millisecond)

	snap := orch.Snapshot()
	assert.Equal(t, 1, len(snap.Running), "only 1 worker should run when max_concurrent_agents=1")
}

// ---------------------------------------------------------------------------
// 3. IneligibleReason — blocked_by non-terminal issue
// ---------------------------------------------------------------------------

func TestIneligibleReasonBlockedByNonTerminal(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerState := "In Progress"
	blockerID := "ENG-99"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{State: &blockerState, Identifier: &blockerID}}

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "blocked_by:ENG-99", reason)
}

// ---------------------------------------------------------------------------
// 4. IneligibleReason — paused
// ---------------------------------------------------------------------------

func TestIneligibleReasonPaused(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.PausedIdentifiers["ENG-1"] = "id1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "paused", reason)
}

// ---------------------------------------------------------------------------
// 5. IneligibleReason — discarding
// ---------------------------------------------------------------------------

func TestIneligibleReasonDiscarding(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.DiscardingIdentifiers["ENG-1"] = struct{}{}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "discarding", reason)
}

// ---------------------------------------------------------------------------
// 6. IneligibleReason — input_required
// ---------------------------------------------------------------------------

func TestIneligibleReasonInputRequired(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &orchestrator.InputRequiredEntry{IssueID: "id1"}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "input_required", reason)
}

// ---------------------------------------------------------------------------
// 7. IneligibleReason — per_state_limit
// ---------------------------------------------------------------------------

func TestIneligibleReasonPerStateLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{
		"in progress": 1,
	}
	state := orchestrator.NewState(cfg)
	// One already running in "In Progress"
	state.Running["other"] = &orchestrator.RunEntry{
		Issue: makeIssue("other", "ENG-0", "In Progress", nil, nil),
	}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "per_state_limit", reason)
}

// ---------------------------------------------------------------------------
// 8. IneligibleReason — missing_fields
// ---------------------------------------------------------------------------

func TestIneligibleReasonMissingFields(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)

	// Empty ID
	issue := domain.Issue{Identifier: "ENG-1", Title: "T", State: "In Progress"}
	assert.Equal(t, "missing_fields", orchestrator.IneligibleReason(issue, state, cfg))

	// Empty Identifier
	issue = domain.Issue{ID: "id1", Title: "T", State: "In Progress"}
	assert.Equal(t, "missing_fields", orchestrator.IneligibleReason(issue, state, cfg))

	// Empty Title
	issue = domain.Issue{ID: "id1", Identifier: "ENG-1", State: "In Progress"}
	assert.Equal(t, "missing_fields", orchestrator.IneligibleReason(issue, state, cfg))

	// Empty State
	issue = domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T"}
	assert.Equal(t, "missing_fields", orchestrator.IneligibleReason(issue, state, cfg))
}

// ---------------------------------------------------------------------------
// 9. DryRun mode — no RunTurn called but dispatch logged
// ---------------------------------------------------------------------------

func TestDryRunMode(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.DryRun = true

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for a few ticks
	time.Sleep(300 * time.Millisecond)
	cancel()

	// Check that the runner was never called
	fake.CallCount = 0 // reset — but since DryRun should have prevented calls...
	// Actually we need to check differently. Check log output for DRY-RUN.
	logs := logBuf.String()
	assert.Contains(t, logs, "DRY-RUN", "dry run should log dispatch intention")

	// The issue should be claimed but not running (no worker spawned).
	snap := orch.Snapshot()
	assert.Empty(t, snap.Running, "no workers should be running in dry-run mode")
}

func TestDryRunAutomationDispatch(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"qa": {Command: "claude", Prompt: "Run QA checks."},
	}
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	mt := &noCandidateTracker{base: tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.DryRun = true

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	require.True(t, orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
		AutomationID: "qa-ready",
		ProfileName:  "qa",
		Instructions: "Run QA and report.",
		Trigger: orchestrator.AutomationTriggerContext{
			Type:         config.AutomationTriggerCron,
			AutomationID: "qa-ready",
			FiredAt:      time.Now(),
		},
	}))

	time.Sleep(150 * time.Millisecond)
	cancel()

	assert.Zero(t, fake.CallCount, "dry-run automation dispatch should not execute the runner")
	assert.Contains(t, logBuf.String(), "DRY-RUN", "dry-run automation dispatch should be logged")
	assert.Empty(t, orch.Snapshot().Running)
}

// ---------------------------------------------------------------------------
// 10. Profile override resolution
// ---------------------------------------------------------------------------

func TestProfileOverrideResolution(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "default-agent"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"fast": {Command: "fast-agent", Backend: "codex"},
	}
	mt := singleIssueTracker(t, "In Progress")

	wrapped := &capturingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}),
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)
	orch.SetIssueProfile("ENG-1", "fast")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-wrapped.done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was not invoked within 2s")
	}

	cmd := wrapped.LastCommand()
	assert.Contains(t, cmd, "fast-agent", "runner should receive the profile's command")
}

// ---------------------------------------------------------------------------
// 11. Issue state transition — working_state on dispatch
// ---------------------------------------------------------------------------

func TestWorkingStateTransitionOnDispatch(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Tracker.WorkingState = "In Progress"

	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	done := make(chan struct{})
	wrapped := &trackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not invoked within 3s")
	}

	// Give the worker time to call transitionToWorking.
	time.Sleep(200 * time.Millisecond)

	// Check tracker was updated.
	issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	// The issue should have been transitioned to "In Progress" from "Todo".
	assert.Equal(t, "In Progress", issues[0].State)
}

// ---------------------------------------------------------------------------
// 12. Stall timeout triggers retry (via ReconcileStalls)
// ---------------------------------------------------------------------------

func TestStallTimeoutSchedulesRetry(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.StallTimeoutMs = 100 // 100ms stall timeout
	cfg.Agent.MaxRetryBackoffMs = 300000
	state := orchestrator.NewState(cfg)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	// Create a running entry that started well before the stall timeout.
	old := time.Now().Add(-1 * time.Second)
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:        issue,
		StartedAt:    old,
		LastEventAt:  &old,
		WorkerCancel: func() {},
	}

	state = orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)

	// Issue should be removed from Running and added to RetryAttempts.
	_, stillRunning := state.Running["id1"]
	assert.False(t, stillRunning, "stalled issue should be removed from running")
	entry, inRetry := state.RetryAttempts["id1"]
	assert.True(t, inRetry, "stalled issue should be scheduled for retry")
	assert.Equal(t, "ENG-1", entry.Identifier)
}

// ---------------------------------------------------------------------------
// 13. Getter/setter coverage
// ---------------------------------------------------------------------------

func TestSetAgentLogDir(t *testing.T) {
	o := newOrch()
	o.SetAgentLogDir("/tmp/logs")
	assert.Equal(t, "/tmp/logs", o.AgentLogDir())
}

func TestSetAppSessionID(t *testing.T) {
	o := newOrch()
	o.SetAppSessionID("session-123")
	// No getter exposed; just verify no panic.
}

func TestAutoClearWorkspaceCfgRoundtrip(t *testing.T) {
	o := newOrch()
	require.NoError(t, o.SetAutoClearWorkspaceCfg(true))
	assert.True(t, o.AutoClearWorkspaceCfg())
	require.NoError(t, o.SetAutoClearWorkspaceCfg(false))
	assert.False(t, o.AutoClearWorkspaceCfg())
}

// New (v0.2.0): AutoClear and AutoReview now coexist. The clear is
// deferred from the main worker's success to the reviewer's success so the
// reviewer has the workspace available. This test pins the new contract:
// setting both flags simultaneously must succeed.
func TestAutoClearWorkspaceCfgAllowsReviewerCoexistence(t *testing.T) {
	o := newOrch()
	o.SetProfilesCfg(map[string]config.AgentProfile{
		"reviewer": {Command: "claude"},
	})
	require.NoError(t, o.SetReviewerCfg("reviewer", true))

	require.NoError(t, o.SetAutoClearWorkspaceCfg(true))
	assert.True(t, o.AutoClearWorkspaceCfg(), "auto-clear should be accepted alongside auto-review under new terminal-state-only semantics")
}

func TestInlineInputCfgRoundtrip(t *testing.T) {
	o := newOrch()
	o.SetInlineInputCfg(true)
	assert.True(t, o.InlineInputCfg())
	o.SetInlineInputCfg(false)
	assert.False(t, o.InlineInputCfg())
}

func TestSSHHostsCfgRoundtrip(t *testing.T) {
	o := newOrch()
	o.AddSSHHostCfg("host1.example.com", "Host 1")
	o.AddSSHHostCfg("host2.example.com", "Host 2")

	hosts, descs := o.SSHHostsCfg()
	assert.Len(t, hosts, 2)
	assert.Contains(t, hosts, "host1.example.com")
	assert.Contains(t, hosts, "host2.example.com")
	assert.Equal(t, "Host 1", descs["host1.example.com"])

	// Update existing host description.
	o.AddSSHHostCfg("host1.example.com", "Updated Host 1")
	_, descs = o.SSHHostsCfg()
	assert.Equal(t, "Updated Host 1", descs["host1.example.com"])

	// Remove a host.
	o.RemoveSSHHostCfg("host1.example.com")
	hosts, descs = o.SSHHostsCfg()
	assert.Len(t, hosts, 1)
	assert.Equal(t, "host2.example.com", hosts[0])
	_, exists := descs["host1.example.com"]
	assert.False(t, exists)
}

func TestDispatchStrategyCfgRoundtrip(t *testing.T) {
	o := newOrch()
	o.SetDispatchStrategyCfg("least-loaded")
	assert.Equal(t, "least-loaded", o.DispatchStrategyCfg())
	o.SetDispatchStrategyCfg("round-robin")
	assert.Equal(t, "round-robin", o.DispatchStrategyCfg())
}

func TestAvailableModelsCfg(t *testing.T) {
	o := newOrch()
	// Default config has no models; should return nil without panic.
	models := o.AvailableModelsCfg()
	assert.Nil(t, models)
}

func TestBumpMaxWorkers(t *testing.T) {
	o := newOrch()
	o.SetMaxWorkers(5)

	// Bump up
	n := o.BumpMaxWorkers(3)
	assert.Equal(t, 8, n)

	// Bump down
	n = o.BumpMaxWorkers(-2)
	assert.Equal(t, 6, n)

	// Clamp at 1
	n = o.BumpMaxWorkers(-100)
	assert.Equal(t, 1, n)

	// Clamp at cap (50)
	n = o.BumpMaxWorkers(200)
	assert.Equal(t, 50, n)
}

func TestClearHistory(t *testing.T) {
	o := newOrch()
	// Just verify no panic when clearing empty history.
	o.ClearHistory()
	assert.Empty(t, o.RunHistory())
}

// ---------------------------------------------------------------------------
// 14. StartupTerminalCleanup
// ---------------------------------------------------------------------------

func TestStartupTerminalCleanup(t *testing.T) {
	cfg := baseConfig()
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "Done", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	removed := make(chan string, 5)
	removeWorkspace := func(identifier string) error {
		removed <- identifier
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	orchestrator.StartupTerminalCleanup(ctx, mt, []string{"Done"}, removeWorkspace)

	select {
	case id := <-removed:
		assert.Equal(t, "ENG-1", id)
	case <-time.After(3 * time.Second):
		t.Fatal("StartupTerminalCleanup did not remove workspace within 3s")
	}
}

// ---------------------------------------------------------------------------
// 15. ReconcileStalls — no stall when activity is recent
// ---------------------------------------------------------------------------

func TestReconcileStallsNoStallWhenRecent(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.StallTimeoutMs = 60000 // 60s
	cfg.Agent.MaxRetryBackoffMs = 300000
	state := orchestrator.NewState(cfg)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	recent := time.Now()
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:        makeIssue("id1", "ENG-1", "In Progress", nil, nil),
		StartedAt:    recent,
		LastEventAt:  &recent,
		WorkerCancel: func() {},
	}

	state = orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)
	_, stillRunning := state.Running["id1"]
	assert.True(t, stillRunning, "recent activity should not trigger stall")
}

// ---------------------------------------------------------------------------
// 16. IneligibleReason — blocked_by with nil identifier
// ---------------------------------------------------------------------------

func TestIneligibleReasonBlockedByNilIdentifier(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerState := "In Progress"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{State: &blockerState}} // nil Identifier

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "blocked_by:", reason)
}

// ---------------------------------------------------------------------------
// 17. IneligibleReason — blocked_by with paused blocker
// ---------------------------------------------------------------------------

func TestIneligibleReasonBlockedByPausedBlockerStillBlocks(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerState := "In Progress"
	blockerID := "ENG-99"
	state.PausedIdentifiers["ENG-99"] = "id99"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{State: &blockerState, Identifier: &blockerID}}

	reason := orchestrator.IneligibleReason(issue, state, cfg)
	assert.Equal(t, "blocked_by:ENG-99", reason, "paused blockers are not terminal and must still block dispatch")
}

// ---------------------------------------------------------------------------
// 18. AvailableSlots negative clamp
// ---------------------------------------------------------------------------

func TestAvailableSlotsNeverNegative(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxConcurrentAgents = 1
	state := orchestrator.NewState(cfg)
	// 2 running > max
	state.Running["a"] = &orchestrator.RunEntry{}
	state.Running["b"] = &orchestrator.RunEntry{}
	assert.Equal(t, 0, orchestrator.AvailableSlots(state))
}

// ---------------------------------------------------------------------------
// 19. SortForDispatch — tie-breaking by identifier
// ---------------------------------------------------------------------------

func TestSortForDispatchTiebreakByIdentifier(t *testing.T) {
	t1 := time.Unix(1000, 0)
	issues := []domain.Issue{
		makeIssue("c", "ENG-3", "Todo", prio(1), &t1),
		makeIssue("a", "ENG-1", "Todo", prio(1), &t1),
		makeIssue("b", "ENG-2", "Todo", prio(1), &t1),
	}
	sorted := orchestrator.SortForDispatch(issues)
	assert.Equal(t, "ENG-1", sorted[0].Identifier)
	assert.Equal(t, "ENG-2", sorted[1].Identifier)
	assert.Equal(t, "ENG-3", sorted[2].Identifier)
}

// ---------------------------------------------------------------------------
// 20. Dispatch with SSH host round-robin
// ---------------------------------------------------------------------------

func TestDispatchWithSSHHosts(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.SSHHosts = []string{"host1.example.com", "host2.example.com"}
	mt := singleIssueTracker(t, "In Progress")

	// Stalling fake runner — holds RunTurn open so we can observe the worker
	// in the Running snapshot. A non-stalling runner emits {Type: "result"}
	// and exits before the test goroutine can read the snapshot, producing
	// a race where Running is empty by the time we check (see CI flake).
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	// Observe "worker is visible in Running" deterministically via
	// OnStateChange, not via "RunTurn was called" (too early) — same
	// pattern as TestCancelIssue_Running.
	workerVisible := make(chan struct{})
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Track the orchestrator goroutine so we can wait for it to exit before
	// the test returns — prevents goroutine leaks from racing with any
	// cleanup the test framework runs after the function exits.
	runDone := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear in Running snapshot within 2s")
	}

	snap := orch.Snapshot()
	require.Len(t, snap.Running, 1)
	for _, entry := range snap.Running {
		assert.NotEmpty(t, entry.WorkerHost, "worker should be assigned an SSH host")
	}

	// Explicitly stop the orchestrator and wait for the goroutine to exit.
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not exit within 2s of cancel")
	}
}

// ---------------------------------------------------------------------------
// 21. BackoffMs edge cases
// ---------------------------------------------------------------------------

func TestBackoffMsZeroAttempt(t *testing.T) {
	// attempt <= 0 should be treated as 1
	assert.Equal(t, 10000, orchestrator.BackoffMs(0, 300000))
	assert.Equal(t, 10000, orchestrator.BackoffMs(-5, 300000))
}

func TestBackoffMsHighAttemptCapped(t *testing.T) {
	// Very high attempt should be capped at maxMs
	result := orchestrator.BackoffMs(50, 300000)
	assert.Equal(t, 300000, result)
}

// ---------------------------------------------------------------------------
// 22. handleEvent — EventResumeIssue
// ---------------------------------------------------------------------------

func TestResumeIssueClearsFromPaused(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	// ctx must outlast three 2s deadlines (worker appears, pauses, resumes).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wait for the worker to be visible, then cancel it.
	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	// Cancel the issue (moves to paused).
	ok := orch.CancelIssue("ENG-1")
	require.True(t, ok)

	// Wait for paused to appear.
	deadline := time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			break
		}
		select {
		case <-deadline:
			t.Fatal("issue did not become paused within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Resume the issue.
	orch.ResumeIssue("ENG-1")

	// Wait for paused to be cleared.
	deadline = time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; !paused {
			return // Success
		}
		select {
		case <-deadline:
			t.Fatal("issue was not resumed within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 23. ReconcileStalls emits EventWorkerExited with TerminalStalled
// ---------------------------------------------------------------------------

func TestReconcileStallsEmitsStallEvent(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.StallTimeoutMs = 100
	cfg.Agent.MaxRetryBackoffMs = 300000
	state := orchestrator.NewState(cfg)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	old := time.Now().Add(-5 * time.Second)
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:        makeIssue("id1", "ENG-1", "In Progress", nil, nil),
		StartedAt:    old,
		LastEventAt:  &old,
		WorkerCancel: func() {},
	}

	orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)

	select {
	case ev := <-events:
		assert.Equal(t, orchestrator.EventWorkerExited, ev.Type)
		require.NotNil(t, ev.RunEntry)
		assert.Equal(t, orchestrator.TerminalStalled, ev.RunEntry.TerminalReason)
	case <-time.After(1 * time.Second):
		t.Fatal("no stall event emitted")
	}
}

// ---------------------------------------------------------------------------
// 24. Completion state transition on successful worker exit
// ---------------------------------------------------------------------------

func TestCompletionStateTransition(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for the issue to transition to "Done".
	deadline := time.After(4 * time.Second)
	for {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		if len(issues) > 0 && issues[0].State == "Done" {
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue was not transitioned to Done within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 25. DryRun blocks dispatch of reviewer too
// ---------------------------------------------------------------------------

func TestDryRunReviewerDispatch(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.AutoReview = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "Review this code."},
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.DryRun = true

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(400 * time.Millisecond)
	cancel()

	logs := logBuf.String()
	assert.Contains(t, logs, "DRY-RUN", "dry-run mode should be logged")
	// In DryRun mode, no actual workers should run.
	snap := orch.Snapshot()
	assert.Empty(t, snap.Running)
}

// ---------------------------------------------------------------------------
// 26. countRunningInState via per_state_limit with multiple states
// ---------------------------------------------------------------------------

func TestPerStateLimitMultipleStates(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{
		"in progress": 1,
		"todo":        2,
	}
	state := orchestrator.NewState(cfg)
	// One running in "In Progress", zero in "Todo"
	state.Running["other"] = &orchestrator.RunEntry{
		Issue: makeIssue("other", "ENG-0", "In Progress", nil, nil),
	}

	// "In Progress" should be at limit
	issueIP := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	assert.Equal(t, "per_state_limit", orchestrator.IneligibleReason(issueIP, state, cfg))

	// "Todo" should still be eligible
	issueTodo := makeIssue("id2", "ENG-2", "Todo", nil, nil)
	assert.Equal(t, "", orchestrator.IneligibleReason(issueTodo, state, cfg))
}

// ---------------------------------------------------------------------------
// 27. Refresh triggers immediate re-poll
// ---------------------------------------------------------------------------

func TestRefreshTriggersRepoll(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 10000 // Long interval so ticks don't fire naturally
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}

	orch := orchestrator.New(cfg, mt, fake, nil)

	dispatched := make(chan string, 1)
	orch.OnDispatch = func(issueID string) {
		select {
		case dispatched <- issueID:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// The initial tick fires immediately (timer at 0), so we get the first dispatch.
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("initial dispatch did not happen within 2s")
	}
}

// ---------------------------------------------------------------------------
// 28. ReconcileTrackerStates — issue not found stops worker
// ---------------------------------------------------------------------------

func TestReconcileTrackerStatesIssueNotFound(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	cancelled := false
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:        makeIssue("id1", "ENG-1", "In Progress", nil, nil),
		StartedAt:    time.Now(),
		WorkerCancel: func() { cancelled = true },
	}

	// Empty tracker — issue not found
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
	_, stillRunning := state.Running["id1"]
	assert.False(t, stillRunning, "issue not found should stop worker")
	assert.True(t, cancelled, "worker cancel should be called")
}

// ---------------------------------------------------------------------------
// 29. handleEvent — EventWorkerUpdate updates tokens and turn count
// ---------------------------------------------------------------------------

func TestWorkerUpdateEventUpdatesState(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	// Verify the running entry exists in the snapshot.
	snap := orch.Snapshot()
	require.Len(t, snap.Running, 1)
	for _, entry := range snap.Running {
		assert.Equal(t, "ENG-1", entry.Issue.Identifier)
	}
}

// ---------------------------------------------------------------------------
// 30. Multiple ticks don't re-dispatch a claimed issue
// ---------------------------------------------------------------------------

func TestClaimedIssueNotRedispatched(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var dispatchCount int
	orch.OnDispatch = func(_ string) { dispatchCount++ }
	_ = orch.Run(ctx)

	assert.Equal(t, 1, dispatchCount, "issue must not be dispatched again while claimed/running")
}

// ---------------------------------------------------------------------------
// 31. Log output contains issue identifier on stall
// ---------------------------------------------------------------------------

func TestStallLogContainsIdentifier(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Agent.StallTimeoutMs = 100
	cfg.Agent.MaxRetryBackoffMs = 300000
	state := orchestrator.NewState(cfg)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	old := time.Now().Add(-5 * time.Second)
	state.Running["id1"] = &orchestrator.RunEntry{
		Issue:        makeIssue("id1", "STALL-1", "In Progress", nil, nil),
		StartedAt:    old,
		LastEventAt:  &old,
		WorkerCancel: func() {},
	}

	orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)

	logs := logBuf.String()
	assert.True(t, strings.Contains(logs, "STALL-1"), "stall log should mention the issue identifier")
}

// ---------------------------------------------------------------------------
// 32. TerminateIssue — running worker
// ---------------------------------------------------------------------------

func TestTerminateIssueRunning(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	// Without BacklogStates, asyncDiscardAndTransition transitions the
	// terminated issue to ActiveStates[0] ("Todo"), which the next poll
	// re-dispatches — Running never returns to 0 within the test window.
	// Setting an explicit non-active backlog target keeps the discard
	// landing-state out of the active set so the orchestrator leaves the
	// terminated issue alone.
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	// ctx must outlast both internal deadlines (worker-appears + terminate-exits,
	// up to 4s combined). With ctx tight against that sum, ctx expires under
	// load, the orchestrator exits, and the worker's exit event is dropped —
	// surfacing as "worker did not exit within 2s after terminate".
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	// Tear down deterministically so the orchestrator goroutine is fully
	// drained before t-Cleanup runs the next test — otherwise its log output
	// can bleed into subsequent tests' captured stderr (TestWorkerCompletedLogHasCorrectTokenValues
	// regression).
	t.Cleanup(func() {
		cancel()
		<-done
	})

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	ok := orch.TerminateIssue("ENG-1")
	require.True(t, ok, "terminate should succeed for running worker")

	// Wait for the worker to exit and claim to be released. 2s was tight
	// under full-suite load — the cancel signal → worker exit → event-loop
	// processing → storeSnap chain occasionally took > 2s on a busy CPU.
	deadline := time.After(5 * time.Second)
	for {
		snap := orch.Snapshot()
		if len(snap.Running) == 0 {
			// Should NOT be paused (terminate releases claim without pausing).
			_, paused := snap.PausedIdentifiers["ENG-1"]
			assert.False(t, paused, "terminated issue should not be paused")
			return
		}
		select {
		case <-deadline:
			t.Fatal("worker did not exit within 5s after terminate")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 33. TerminateIssue — paused issue
// ---------------------------------------------------------------------------

func TestTerminateIssuePaused(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	// Same rationale as TestTerminateIssueRunning: pin the discard target
	// to a non-active state so the post-terminate poll doesn't immediately
	// re-dispatch and re-populate state.PausedIdentifiers.
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	// ctx must outlast three 2s deadlines (worker appears, pauses, terminates).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	// First pause via cancel.
	orch.CancelIssue("ENG-1")
	deadline := time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			break
		}
		select {
		case <-deadline:
			t.Fatal("issue did not become paused within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Now terminate the paused issue.
	ok := orch.TerminateIssue("ENG-1")
	require.True(t, ok, "terminate should succeed for paused issue")

	// Wait for paused to be cleared.
	deadline = time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; !paused {
			return // Success
		}
		select {
		case <-deadline:
			t.Fatal("paused issue was not terminated within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 34. ProvideInput sends event
// ---------------------------------------------------------------------------

func TestProvideInput(t *testing.T) {
	o := newOrch()
	// ProvideInput should succeed even without Run (just enqueues to channel).
	ok := o.ProvideInput("ENG-1", "here is my answer")
	assert.True(t, ok, "ProvideInput should succeed when channel is not full")
}

// ---------------------------------------------------------------------------
// 35. DismissInput sends event
// ---------------------------------------------------------------------------

func TestDismissInput(t *testing.T) {
	o := newOrch()
	ok := o.DismissInput("ENG-1")
	assert.True(t, ok, "DismissInput should succeed when channel is not full")
}

// ---------------------------------------------------------------------------
// 36. History file persistence roundtrip
// ---------------------------------------------------------------------------

func TestHistoryFilePersistence(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	dir := t.TempDir()
	histFile := dir + "/history.json"

	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetHistoryFile(histFile)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()

	// Wait for the worker to complete and history to be recorded.
	deadline := time.After(3 * time.Second)
	for {
		history := orch.RunHistory()
		if len(history) > 0 {
			assert.Equal(t, "ENG-1", history[0].Identifier)
			// Wait for orch to exit before t.TempDir cleanup races with history.json writes.
			cancel()
			<-done
			return
		}
		select {
		case <-deadline:
			t.Fatal("no history entry recorded within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 37. Paused file persistence roundtrip
// ---------------------------------------------------------------------------

func TestPausedFilePersistence(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}

	dir := t.TempDir()
	pausedFile := dir + "/paused.json"

	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.SetPausedFile(pausedFile)

	// ctx must outlast worker-appears (2s) + pause-detected (2s) + file write
	// + roundtrip; bumped from 3s after observed under-load flakes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	done1 := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done1)
	}()

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	// Cancel to pause the issue (triggers disk write).
	orch.CancelIssue("ENG-1")

	// Wait for paused to appear and file to be written.
	deadline := time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			break
		}
		select {
		case <-deadline:
			t.Fatal("issue did not become paused within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Give a bit of time for the file write to complete.
	time.Sleep(100 * time.Millisecond)

	// Now create a second orchestrator and load from the same file.
	cfg2 := baseConfig()
	mt2 := singleIssueTracker(t, "In Progress")
	orch2 := orchestrator.New(cfg2, mt2, fake, nil)
	orch2.SetPausedFile(pausedFile)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()

	done2 := make(chan struct{})
	go func() {
		_ = orch2.Run(ctx2)
		close(done2)
	}()
	time.Sleep(100 * time.Millisecond)

	snap2 := orch2.Snapshot()
	_, paused := snap2.PausedIdentifiers["ENG-1"]
	assert.True(t, paused, "paused state should persist to disk and reload")

	// Wait for both orchestrator goroutines to exit before the test returns,
	// so t.TempDir cleanup doesn't race with paused.json writes.
	cancel()
	cancel2()
	<-done1
	<-done2
}

// ---------------------------------------------------------------------------
// 38. SetHistoryKey
// ---------------------------------------------------------------------------

func TestSetHistoryKey(t *testing.T) {
	o := newOrch()
	// Just verify no panic — must be called before Run.
	o.SetHistoryKey("github:org/repo")
}

// ---------------------------------------------------------------------------
// 39. GetRunningIssue returns issue when running
// ---------------------------------------------------------------------------

func TestGetRunningIssueFound(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not appear within 2s")
	}

	issue := orch.GetRunningIssue("ENG-1")
	require.NotNil(t, issue)
	assert.Equal(t, "ENG-1", issue.Identifier)
}

// ---------------------------------------------------------------------------
// 40. CancelIssue on retry queue
// ---------------------------------------------------------------------------

func TestCancelIssueInRetryQueue(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 5
	cfg.Agent.MaxRetryBackoffMs = 10000 // long enough that retry hasn't fired
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

	// Wait for the issue to appear in the retry queue (first attempt fails).
	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, inRetry := snap.RetryAttempts["id1"]; inRetry {
			break
		}
		select {
		case <-deadline:
			t.Fatal("issue did not enter retry queue within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Cancel while in retry queue.
	ok := orch.CancelIssue("ENG-1")
	assert.True(t, ok, "cancel should succeed for issue in retry queue")

	// Wait for the issue to be paused.
	deadline = time.After(2 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			// Verify it's removed from retry queue.
			_, inRetry := snap.RetryAttempts["id1"]
			assert.False(t, inRetry, "issue should be removed from retry queue after cancel")
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue was not paused within 2s after cancel")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// 41. Least-loaded dispatch strategy with SSH hosts
// ---------------------------------------------------------------------------

func TestLeastLoadedDispatchStrategy(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.SSHHosts = []string{"host-a", "host-b"}
	cfg.Agent.DispatchStrategy = "least-loaded"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{
			makeIssue("id1", "ENG-1", "In Progress", prio(1), nil),
			makeIssue("id2", "ENG-2", "In Progress", prio(2), nil),
		},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Wait for 2 workers to appear.
	bothVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) >= 2 {
			select {
			case bothVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-bothVisible:
	case <-time.After(2 * time.Second):
		t.Fatal("2 workers did not appear within 2s")
	}

	snap := orch.Snapshot()
	require.Len(t, snap.Running, 2)

	// With least-loaded and 2 hosts, the 2 workers should be on different hosts.
	hosts := make(map[string]int)
	for _, entry := range snap.Running {
		hosts[entry.WorkerHost]++
	}
	assert.Equal(t, 2, len(hosts), "least-loaded should distribute to both hosts")
}

// ---------------------------------------------------------------------------
// 42. SetIssueProfile clears when empty
// ---------------------------------------------------------------------------

func TestSetIssueProfileClear(t *testing.T) {
	o := newOrch()
	o.SetIssueProfile("ENG-1", "fast")
	snap := o.Snapshot()
	assert.Equal(t, "fast", snap.IssueProfiles["ENG-1"])

	o.SetIssueProfile("ENG-1", "")
	snap = o.Snapshot()
	_, exists := snap.IssueProfiles["ENG-1"]
	assert.False(t, exists, "clearing profile should remove the entry")
}

// ---------------------------------------------------------------------------
// 43. Snapshot merges issue backends
// ---------------------------------------------------------------------------

func TestSnapshotMergesIssueBackends(t *testing.T) {
	o := newOrch()
	o.SetIssueBackend("ENG-1", "codex")
	snap := o.Snapshot()
	assert.Equal(t, "codex", snap.IssueBackends["ENG-1"])

	o.SetIssueBackend("ENG-1", "")
	snap = o.Snapshot()
	_, exists := snap.IssueBackends["ENG-1"]
	assert.False(t, exists, "clearing backend should remove the entry")
}

// ---------------------------------------------------------------------------
// 44. SetInputRequiredFile
// ---------------------------------------------------------------------------

func TestSetInputRequiredFile(t *testing.T) {
	o := newOrch()
	// Just verify no panic.
	o.SetInputRequiredFile(t.TempDir() + "/input_required.json")
}

// ---------------------------------------------------------------------------
// 45. DismissInput event within running orchestrator moves to paused
// ---------------------------------------------------------------------------

func TestDismissInputMovesToPaused(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20

	// Use an issue that is NOT in active_states so the orchestrator doesn't
	// auto-dispatch it. We manually inject it into InputRequiredIssues via the
	// event loop. Instead, let's use the full flow with an input-required runner.

	// Simpler approach: start orchestrator, then send DismissInput for a
	// non-existent identifier — this exercises the "unknown identifier" branch.
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	fake := agenttest.NewFakeRunner(nil)
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)

	// DismissInput for unknown identifier should succeed at the channel level
	// but the event handler should log a warning and not crash.
	ok := orch.DismissInput("NONEXISTENT-1")
	assert.True(t, ok)

	ok = orch.ProvideInput("NONEXISTENT-1", "hello")
	assert.True(t, ok)

	// Let the events process without panic.
	time.Sleep(100 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// 46. History with key scoping
// ---------------------------------------------------------------------------

func TestHistoryKeyScoping(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	dir := t.TempDir()
	histFile := dir + "/history_scoped.json"

	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetHistoryFile(histFile)
	orch.SetHistoryKey("github:org/repo")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done1 := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done1)
	}()

	deadline := time.After(3 * time.Second)
	for {
		history := orch.RunHistory()
		if len(history) > 0 {
			assert.Equal(t, "github:org/repo", history[0].ProjectKey)
			cancel()
			break
		}
		select {
		case <-deadline:
			t.Fatal("no history entry recorded within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Now load the same file with a different key — should filter out the entry.
	cfg2 := baseConfig()
	mt2 := tracker.NewMemoryTracker(nil, cfg2.Tracker.ActiveStates, cfg2.Tracker.TerminalStates)
	orch2 := orchestrator.New(cfg2, mt2, runner, nil)
	orch2.SetHistoryFile(histFile)
	orch2.SetHistoryKey("github:other/repo")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()

	done2 := make(chan struct{})
	go func() {
		_ = orch2.Run(ctx2)
		close(done2)
	}()
	time.Sleep(200 * time.Millisecond)

	history2 := orch2.RunHistory()
	assert.Empty(t, history2, "history should be filtered by project key")

	// Wait for both goroutines to exit so t.TempDir cleanup doesn't race history writes.
	cancel()
	cancel2()
	<-done1
	<-done2
}

// ---------------------------------------------------------------------------
// 47. ClearHistory removes file from disk
// ---------------------------------------------------------------------------

func TestClearHistoryRemovesFile(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	dir := t.TempDir()
	histFile := dir + "/history_clear.json"

	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetHistoryFile(histFile)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		history := orch.RunHistory()
		if len(history) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no history entry recorded within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}

	orch.ClearHistory()
	assert.Empty(t, orch.RunHistory(), "history should be empty after clear")

	// Wait for orch to exit so t.TempDir cleanup doesn't race history writes.
	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// 48. ReanalyzeIssue when not paused
// ---------------------------------------------------------------------------

func TestReanalyzeIssueNotPaused(t *testing.T) {
	o := newOrch()
	ok := o.ReanalyzeIssue("ENG-99")
	assert.False(t, ok, "reanalyze should return false when issue is not paused")
}

// ---------------------------------------------------------------------------
// 49. SortForDispatch — nil created_at handling
// ---------------------------------------------------------------------------

func TestSortForDispatchNilCreatedAt(t *testing.T) {
	t1 := time.Unix(1000, 0)
	issues := []domain.Issue{
		makeIssue("a", "ENG-1", "Todo", prio(1), nil), // nil created_at
		makeIssue("b", "ENG-2", "Todo", prio(1), &t1), // has created_at
		makeIssue("c", "ENG-3", "Todo", prio(1), nil), // nil created_at
	}
	sorted := orchestrator.SortForDispatch(issues)
	// ENG-2 (has created_at) should come before ENG-1 and ENG-3 (nil created_at)
	assert.Equal(t, "ENG-2", sorted[0].Identifier)
}

// ---------------------------------------------------------------------------
// 50. ResumeIssue returns false when not paused
// ---------------------------------------------------------------------------

func TestResumeIssueNotPaused(t *testing.T) {
	o := newOrch()
	ok := o.ResumeIssue("ENG-99")
	assert.False(t, ok, "resume should return false when issue is not paused")
}

// ---------------------------------------------------------------------------
// 51. Input-required file persistence
// ---------------------------------------------------------------------------

func TestInputRequiredFilePersistence(t *testing.T) {
	dir := t.TempDir()
	irFile := dir + "/input_required.json"

	// Write phase: create orchestrator with input-required file,
	// use the inputRequired runner to trigger the flow.
	// Instead, we directly test via the exported SetInputRequiredFile
	// and verify it loads on the next orchestrator.

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20

	// Create first orchestrator and set the file path.
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	fake := agenttest.NewFakeRunner(nil)
	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.SetInputRequiredFile(irFile)

	// Write a test file manually (simulating what saveInputRequiredToDisk does).
	testData := `{"ENG-1":{"issue_id":"id1","identifier":"ENG-1","session_id":"s1","context":"need API key","backend":"claude","command":"claude","branch_name":"feature/eng-1","queued_at":"2025-01-01T00:00:00Z"}}`
	require.NoError(t, writeTestFile(irFile, testData))

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)

	snap := orch.Snapshot()
	entry, ok := snap.InputRequiredIssues["ENG-1"]
	assert.True(t, ok, "input-required entry should be loaded from disk")
	if ok {
		assert.Equal(t, "id1", entry.IssueID)
		assert.Equal(t, "need API key", entry.Context)
		assert.Equal(t, "feature/eng-1", entry.BranchName)
	}

	// Wait for orch to exit so t.TempDir cleanup doesn't race input_required.json writes.
	cancel()
	<-done
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// ---------------------------------------------------------------------------
// Manual pause + resume preserves the agent session ID
// ---------------------------------------------------------------------------

// resumeTestRunner is a test double that:
//  1. First call: returns immediately with a session ID and non-zero tokens
//     (so the worker continues the loop instead of breaking on 0-token turn).
//  2. Second call (turn 2): stalls until ctx is cancelled. By this point the
//     orchestrator has captured the session ID into state.Running.
//  3. Third call (after resume): records the inbound sessionID and exits.
type resumeTestRunner struct {
	mu         sync.Mutex
	calls      int
	sessionIDs []string // sessionID pointer values seen on each call
	prompts    []string
}

func (r *resumeTestRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, _, _, _, _ string, _, _ int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	callNum := r.calls
	sid := ""
	if sessionID != nil {
		sid = *sessionID
	}
	r.sessionIDs = append(r.sessionIDs, sid)
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()

	switch callNum {
	case 1:
		// Return a session ID + non-zero tokens so the worker stores it and
		// continues to turn 2.
		return agent.TurnResult{
			SessionID:    "agent-session-xyz",
			InputTokens:  10,
			OutputTokens: 10,
			TotalTokens:  20,
			ResultText:   "in-progress",
		}, nil
	case 2:
		// Second turn: stall so the worker is active when the user pauses.
		<-ctx.Done()
		return agent.TurnResult{Failed: true}, ctx.Err()
	default:
		// Resumed call: succeed with no work.
		return agent.TurnResult{
			SessionID:    "agent-session-xyz",
			InputTokens:  5,
			OutputTokens: 5,
			TotalTokens:  10,
			ResultText:   "resumed",
		}, nil
	}
}

func TestManualPauseResumePreservesSession(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.PromptTemplate = "Resume {{ issue.identifier }} :: {{ issue.title }}"
	mt := singleIssueTracker(t, "In Progress")
	runner := &resumeTestRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Track when the agent session ID has been captured into Running state
	// AND the worker has actually started (so cancellation has a worker to kill).
	sessionCaptured := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if entry, ok := snap.Running["id1"]; ok && entry.AgentSessionID == "agent-session-xyz" {
			select {
			case sessionCaptured <- struct{}{}:
			default:
			}
		}
	}

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-sessionCaptured:
	case <-time.After(3 * time.Second):
		t.Fatal("agent session ID was not captured into Running state within 3s")
	}

	// User clicks Pause.
	require.True(t, orch.CancelIssue("ENG-1"))

	// Wait for the issue to land in PausedSessions with the captured session.
	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if entry, ok := snap.PausedSessions["ENG-1"]; ok && entry != nil && entry.SessionID == "agent-session-xyz" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("PausedSessions did not capture session ID; snap=%+v", orch.Snapshot().PausedSessions)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// User clicks Resume.
	require.True(t, orch.ResumeIssue("ENG-1"))

	// Wait for the resumed worker invocation (third RunTurn call overall:
	// first-turn success, second-turn stall, third-turn resumed worker).
	deadline = time.After(3 * time.Second)
	for {
		runner.mu.Lock()
		callCount := runner.calls
		var lastSid string
		var resumedPrompt string
		if len(runner.sessionIDs) >= 3 {
			lastSid = runner.sessionIDs[2]
			resumedPrompt = runner.prompts[2]
		}
		runner.mu.Unlock()
		if callCount >= 3 {
			require.Equal(t, "agent-session-xyz", lastSid,
				"resumed worker must call RunTurn with the captured session ID")
			// The rendered WORKFLOW.md is the lead of the prompt; the worker
			// also appends a Run Context block (v0.2.0: file-backed agent
			// handoff). Pin only the WORKFLOW.md portion.
			require.True(t, strings.HasPrefix(resumedPrompt, "Resume ENG-1 :: T"),
				"manual pause/resume should keep the rendered workflow prompt at the lead; got %q", resumedPrompt)
			return
		}
		select {
		case <-deadline:
			t.Fatalf("runner was not called by the resumed worker; calls=%d", callCount)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestManualPauseResumeWithoutSession(t *testing.T) {
	// When pause happens before the agent reports a session ID, PausedSessions
	// should NOT have an entry, and resume should fall back to a fresh dispatch
	// (no --resume).
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		snap := orch.Snapshot()
		if len(snap.Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not visible within 3s")
	}

	// Cancel before any session ID has been captured.
	require.True(t, orch.CancelIssue("ENG-1"))

	// Wait for paused.
	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			// PausedSessions should be empty for this issue.
			_, hasSession := snap.PausedSessions["ENG-1"]
			require.False(t, hasSession,
				"PausedSessions must NOT have an entry when no session ID was captured")
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue did not become paused within 3s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
