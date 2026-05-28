package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

func singleIssueTracker(t *testing.T, state string) *tracker.MemoryTracker {
	t.Helper()
	cfg := baseConfig()
	return tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", state, nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
}

func TestOrchestratorDispatchesOnTick(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	mt := singleIssueTracker(t, "In Progress")
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})

	orch := orchestrator.New(cfg, mt, fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dispatched := make(chan string, 1)
	orch.OnDispatch = func(issueID string) {
		select {
		case dispatched <- issueID:
		default: // ignore subsequent dispatches
		}
	}

	go orch.Run(ctx) //nolint:errcheck

	select {
	case id := <-dispatched:
		assert.Equal(t, "id1", id)
	case <-time.After(2 * time.Second):
		t.Fatal("expected dispatch within 2s")
	}
}

func TestOrchestratorNoDuplicateDispatch(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	// Stall = true keeps the worker goroutine blocked, so the issue stays in Running state.
	fake := &agenttest.FakeRunner{Stall: true}

	orch := orchestrator.New(cfg, mt, fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var count int
	orch.OnDispatch = func(_ string) { count++ }
	_ = orch.Run(ctx)

	require.Equal(t, 1, count, "issue must not be dispatched twice while it is running")
}

// TestCancelResumeRace detects the savePausedToDisk map-reference data race.
// Run with: go test -race ./internal/orchestrator/...
func TestCancelResumeRace(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}

	dir := t.TempDir()
	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.SetPausedFile(filepath.Join(dir, "paused.json"))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = orch.Run(ctx)
	}()

	// Let the orchestrator start.
	time.Sleep(30 * time.Millisecond)

	// Concurrently call Resume + Cancel many times to trigger any map race.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			orch.CancelIssue("ENG-1")
		}()
		go func() {
			defer wg.Done()
			orch.ResumeIssue("ENG-1")
		}()
	}
	wg.Wait()
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not stop after cancel")
	}
}

// TestReviewerRespectsCancellation verifies that a reviewer goroutine exits
// when the orchestrator context is cancelled.
func TestReviewerRespectsCancellation(t *testing.T) {
	cfg := baseConfig()
	mt := singleIssueTracker(t, "In Review")
	fake := &agenttest.FakeRunner{Stall: true}

	orch := orchestrator.New(cfg, mt, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		orch.Run(ctx) //nolint:errcheck
	}()

	time.Sleep(30 * time.Millisecond)
	_ = orch.DispatchReviewer("ENG-1") // may return "not found" — that's fine

	cancel()

	select {
	case <-runDone:
		// Good — orchestrator exited promptly.
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not exit within 2s after context cancellation")
	}
}

// TestReanalyzeIssueRace detects the ForceReanalyze map data race.
func TestReanalyzeIssueRace(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}

	dir := t.TempDir()
	orch := orchestrator.New(cfg, mt, fake, nil)
	orch.SetPausedFile(filepath.Join(dir, "paused.json"))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Track the orchestrator goroutine so we can wait for it to fully exit
	// before t.TempDir's cleanup runs. Without this, orch.Run may still be
	// writing paused.json into dir when os.RemoveAll fires from t.Cleanup,
	// producing "directory not empty" flakes (see CI failure on PR merge).
	runDone := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()

	// First pause the issue so ReanalyzeIssue has something to operate on.
	time.Sleep(30 * time.Millisecond)
	orch.CancelIssue("ENG-1")
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			orch.ReanalyzeIssue("ENG-1")
		}()
	}
	wg.Wait()

	// Stop the orchestrator and wait for it to fully exit before the temp
	// dir cleanup runs. cancel() only signals — we need to observe exit.
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not exit within 2s of cancel")
	}
}

func TestCancelIssue_NotRunning(t *testing.T) {
	cfg := baseConfig()
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}
	orch := orchestrator.New(cfg, mt, fake, nil)
	// Do not call Run — no workers are dispatched.

	ok := orch.CancelIssue("ENG-99")
	require.False(t, ok, "cancel should return false when no worker is running")

	// Marker should be cleaned up — second call also returns false.
	ok = orch.CancelIssue("ENG-99")
	require.False(t, ok)
}

func TestCancelIssue_Running(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	mt := singleIssueTracker(t, "In Progress")
	fake := &agenttest.FakeRunner{Stall: true}

	orch := orchestrator.New(cfg, mt, fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Wait for the worker to appear in lastSnap.Running (set by storeSnap,
	// which fires OnStateChange after OnDispatch). Using a channel avoids
	// the race between OnDispatch and the subsequent storeSnap call.
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
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not appear in snapshot within 1s")
	}

	ok := orch.CancelIssue("ENG-1")
	require.True(t, ok, "cancel should return true for a running worker")
}

func TestDispatchReviewer_NoProfileConfigured(t *testing.T) {
	cfg := baseConfig()
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	fake := agenttest.NewFakeRunner(nil)
	orch := orchestrator.New(cfg, mt, fake, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	err := orch.DispatchReviewer("NONEXISTENT-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reviewer_profile configured")
}

// trackingRunner wraps a Runner and signals done on the first RunTurn call.
type trackingRunner struct {
	agent.Runner
	once sync.Once
	done chan struct{}
}

func (r *trackingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost string, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.once.Do(func() { close(r.done) })
	return r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
}

type capturingRunner struct {
	agent.Runner
	mu      sync.Mutex
	once    sync.Once
	done    chan struct{}
	command string
	prompt  string
}

func (r *capturingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost string, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.command = command
	r.prompt = prompt
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
	return r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
}

func (r *capturingRunner) LastCommand() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.command
}

func (r *capturingRunner) LastPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompt
}

func TestDispatchReviewer_Success(t *testing.T) {
	cfg := baseConfig()
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "You are a code reviewer."},
	}
	issue := makeIssue("id1", "ENG-1", "In Review", nil, nil)
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
	time.Sleep(20 * time.Millisecond)

	err := orch.DispatchReviewer("ENG-1")
	require.NoError(t, err)

	select {
	case <-done:
		// reviewer RunTurn completed through the regular worker queue
	case <-ctx.Done():
		t.Fatal("reviewer did not complete within 3s")
	}
}

func TestWorkerPromptAppendsSoulInstructionsAutomationAndActionContextInOrder(t *testing.T) {
	cfg := baseConfig()
	cfg.PromptTemplate = "Main {{ issue.identifier }}"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {
			Command:        "claude",
			Soul:           "Soul {{ issue.identifier }}",
			Instructions:   "Instructions {{ issue.title }}",
			AllowedActions: []string{config.AgentActionComment},
		},
	}
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	mt := &noCandidateTracker{base: tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	wrapped := &capturingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}),
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)
	orch.SetAgentActionBaseURL("http://127.0.0.1:8090")
	orch.SetAgentActionTokens(agentactions.NewStore())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	require.True(t, orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
		AutomationID:      "auto",
		ProfileName:       "implementer",
		Instructions:      "Automation {{ issue.identifier }}",
		Trigger:           orchestrator.AutomationTriggerContext{Type: config.AutomationTriggerCron, AutomationID: "auto", FiredAt: time.Now()},
		AutoResume:        true,
		UseIssueLifecycle: true,
	}))

	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("worker did not run")
	}

	prompt := wrapped.LastPrompt()
	mainIdx := strings.Index(prompt, "Main ENG-1")
	soulIdx := strings.Index(prompt, "Soul ENG-1")
	instructionsIdx := strings.Index(prompt, "Instructions T")
	automationIdx := strings.Index(prompt, "Automation ENG-1")
	actionIdx := strings.Index(prompt, "Itervox daemon actions are available for this profile")
	require.NotEqual(t, -1, mainIdx, prompt)
	require.NotEqual(t, -1, soulIdx, prompt)
	require.NotEqual(t, -1, instructionsIdx, prompt)
	require.NotEqual(t, -1, automationIdx, prompt)
	require.NotEqual(t, -1, actionIdx, prompt)
	assert.Less(t, mainIdx, soulIdx)
	assert.Less(t, soulIdx, instructionsIdx)
	assert.Less(t, instructionsIdx, automationIdx)
	assert.Less(t, automationIdx, actionIdx)
}

func TestDispatchReviewer_AppendsFileBackedReviewerProfile(t *testing.T) {
	cfg := baseConfig()
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.ReviewerPrompt = "Reviewer base {{ issue.identifier }}"
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {
			Command:      "claude",
			Soul:         "Reviewer soul {{ issue.title }}",
			Instructions: "Reviewer instructions {{ issue.identifier }}",
		},
	}
	issue := makeIssue("id1", "ENG-1", "In Review", nil, nil)
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	wrapped := &capturingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}),
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, orch.DispatchReviewer("ENG-1"))

	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("reviewer did not run")
	}

	prompt := wrapped.LastPrompt()
	baseIdx := strings.Index(prompt, "Reviewer base ENG-1")
	soulIdx := strings.Index(prompt, "Reviewer soul T")
	instructionsIdx := strings.Index(prompt, "Reviewer instructions ENG-1")
	require.NotEqual(t, -1, baseIdx, prompt)
	require.NotEqual(t, -1, soulIdx, prompt)
	require.NotEqual(t, -1, instructionsIdx, prompt)
	assert.Less(t, baseIdx, soulIdx)
	assert.Less(t, soulIdx, instructionsIdx)
}

func TestDispatchUsesProfileBackendOverride(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "claude"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"codex-fast": {
			Command: "run-codex-wrapper",
			Backend: "codex",
		},
	}
	mt := singleIssueTracker(t, "In Progress")
	wrapped := &capturingRunner{
		Runner: &agenttest.FakeRunner{Stall: true},
		done:   make(chan struct{}),
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)
	orch.SetIssueProfile("ENG-1", "codex-fast")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		if len(orch.Snapshot().Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-ctx.Done():
		t.Fatal("worker did not appear in snapshot within 2s")
	}
	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("runner was not invoked within 2s")
	}

	snap := orch.Snapshot()
	require.Len(t, snap.Running, 1)
	for _, entry := range snap.Running {
		assert.Equal(t, "codex", entry.Backend)
	}
	assert.Equal(t, agent.CommandWithBackendHint("run-codex-wrapper", "codex"), wrapped.LastCommand())
}

func TestDispatchUsesDefaultBackendOverride(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "run-codex-wrapper"
	cfg.Agent.Backend = "codex"
	mt := singleIssueTracker(t, "In Progress")
	wrapped := &capturingRunner{
		Runner: &agenttest.FakeRunner{Stall: true},
		done:   make(chan struct{}),
	}
	orch := orchestrator.New(cfg, mt, wrapped, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	workerVisible := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		if len(orch.Snapshot().Running) > 0 {
			select {
			case workerVisible <- struct{}{}:
			default:
			}
		}
	}

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-workerVisible:
	case <-ctx.Done():
		t.Fatal("worker did not appear in snapshot within 2s")
	}
	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("runner was not invoked within 2s")
	}

	snap := orch.Snapshot()
	require.Len(t, snap.Running, 1)
	for _, entry := range snap.Running {
		assert.Equal(t, "codex", entry.Backend)
	}
	assert.Equal(t, agent.CommandWithBackendHint("run-codex-wrapper", "codex"), wrapped.LastCommand())
}

func TestRemoteWorkerAllowedActionsDoNotInjectDaemonActionEnv(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "claude"
	cfg.Agent.SSHHosts = []string{"ssh-runner"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"remote-profile": {
			Command:        "claude",
			AllowedActions: []string{config.AgentActionComment, config.AgentActionMoveState},
		},
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
	orch.SetIssueProfile("ENG-1", "remote-profile")
	orch.SetAgentActionBaseURL("http://127.0.0.1:9999")
	orch.SetAgentActionTokens(agentactions.NewStore())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = orch.Run(ctx)
	}()

	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("runner was not invoked within 2s")
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrator did not stop after cancel")
	}

	command := wrapped.LastCommand()
	assert.False(t, strings.Contains(command, "ITERVOX_ACTION_TOKEN="), "remote workers must not receive local daemon action tokens")
	assert.False(t, strings.Contains(command, "ITERVOX_DAEMON_URL="), "remote workers must not receive local daemon action base URL")
	assert.Contains(t, wrapped.LastPrompt(), "not available on remote SSH workers")
}

// TestTeamsModeUsesResolvedProfileBackendForSubagentContext was authored
// when the (now-removed) `agent_mode == "teams"` gate controlled subagent
// roster injection. The roster now always injects when there's more than one
// profile, so this test exercises the same path without the gate.
func TestTeamsModeUsesResolvedProfileBackendForSubagentContext(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "claude"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"codex-fast": {
			Command: "run-codex-wrapper",
			Backend: "codex",
		},
		"research": {
			Command: "claude --model claude-sonnet-4-6",
			Prompt:  "Deep research support.",
		},
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
	orch.SetIssueProfile("ENG-1", "codex-fast")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-wrapped.done:
	case <-ctx.Done():
		t.Fatal("runner was not invoked within 2s")
	}

	assert.Contains(t, wrapped.LastPrompt(), "spawn_agent tool")
	assert.NotContains(t, wrapped.LastPrompt(), "Task tool")
}

// --- Getter/setter unit tests ---

func newOrch() *orchestrator.Orchestrator {
	cfg := baseConfig()
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	return orchestrator.New(cfg, mt, nil, nil)
}

func TestMaxWorkersClamped(t *testing.T) {
	o := newOrch()
	o.SetMaxWorkers(0)
	assert.Equal(t, 1, o.MaxWorkers())

	o.SetMaxWorkers(100)
	assert.Equal(t, 50, o.MaxWorkers())

	o.SetMaxWorkers(5)
	assert.Equal(t, 5, o.MaxWorkers())
}

func TestProfilesCfgRoundtrip(t *testing.T) {
	o := newOrch()
	profiles := map[string]config.AgentProfile{
		"fast": {Command: "codex", Backend: "codex"},
	}
	o.SetProfilesCfg(profiles)
	got := o.ProfilesCfg()
	require.Contains(t, got, "fast")
	assert.Equal(t, "codex", got["fast"].Command)
}

func TestTrackerStatesCfgRoundtrip(t *testing.T) {
	o := newOrch()
	o.SetTrackerStatesCfg([]string{"Todo"}, []string{"Done"}, "Done")
	active, terminal, completion := o.TrackerStatesCfg()
	assert.Equal(t, []string{"Todo"}, active)
	assert.Equal(t, []string{"Done"}, terminal)
	assert.Equal(t, "Done", completion)
}

// CRIT-1 regression guard: SetAutomationsCfg must be visible to AutomationsCfg()
// immediately, with field-by-field deep copy of slice-valued filter fields.
func TestAutomationsCfgRoundtrip(t *testing.T) {
	o := newOrch()
	assert.Nil(t, o.AutomationsCfg(), "empty orchestrator returns nil automations")

	o.SetAutomationsCfg([]config.AutomationConfig{
		{
			ID:      "qa",
			Enabled: true,
			Profile: "qa",
			Trigger: config.AutomationTriggerConfig{Type: "cron", Cron: "0 */2 * * *", Timezone: "UTC"},
			Filter:  config.AutomationFilterConfig{States: []string{"Ready for QA"}, LabelsAny: []string{"qa"}},
		},
	})
	got := o.AutomationsCfg()
	assert.Len(t, got, 1)
	assert.Equal(t, "qa", got[0].ID)
	assert.Equal(t, []string{"Ready for QA"}, got[0].Filter.States)

	// Mutate the returned copy and confirm the orchestrator's copy is untouched.
	got[0].Filter.States[0] = "TAMPERED"
	got[0].Filter.LabelsAny[0] = "TAMPERED"
	after := o.AutomationsCfg()
	assert.Equal(t, "Ready for QA", after[0].Filter.States[0])
	assert.Equal(t, "qa", after[0].Filter.LabelsAny[0])
}

func TestGetRunningIssueNotFound(t *testing.T) {
	o := newOrch()
	result := o.GetRunningIssue("ENG-99")
	assert.Nil(t, result)
}

func TestRunHistoryEmpty(t *testing.T) {
	o := newOrch()
	assert.Empty(t, o.RunHistory())
}

func TestSetHistoryFile(t *testing.T) {
	o := newOrch()
	// Just verify no panic; the orchestrator isn't running.
	o.SetHistoryFile(filepath.Join(t.TempDir(), "history.json"))
}

func TestRefreshNonBlocking(t *testing.T) {
	o := newOrch()
	// Refresh is non-blocking; calling it multiple times without a reader must not deadlock.
	o.Refresh()
	o.Refresh()
}

func TestSetLogBuffer(t *testing.T) {
	o := newOrch()
	// Just verify it doesn't panic when a nil buffer is passed.
	o.SetLogBuffer(nil)
}

func TestTerminateIssue_NotRunning(t *testing.T) {
	o := newOrch()
	// Neither running nor paused — should return false.
	assert.False(t, o.TerminateIssue("ENG-99"))
}

func TestGetPausedOpenPRs_Empty(t *testing.T) {
	o := newOrch()
	result := o.GetPausedOpenPRs()
	assert.NotNil(t, result)
	assert.Empty(t, result)
}
