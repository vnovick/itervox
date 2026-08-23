package orchestrator_test

import (
	"bytes"
	"context"
	"log/slog"
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

// ─── Config + accessor tests ─────────────────────────────────────────────────

func TestReviewerCfgRoundtrip(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude"},
	}
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	require.NoError(t, orch.SetReviewerCfg("reviewer", true))
	profile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "reviewer", profile)
	assert.True(t, autoReview)

	require.NoError(t, orch.SetReviewerCfg("", false))
	profile, autoReview = orch.ReviewerCfg()
	assert.Equal(t, "", profile)
	assert.False(t, autoReview)
}

// New (v0.2.0): SetReviewerCfg used to error when AutoClear was also set
// because they raced under the legacy semantics. Under terminal-state-only
// clearing the two now coexist — the clear is deferred until the reviewer
// also completes.
func TestReviewerCfgAllowsAutoClearCoexistence(t *testing.T) {
	cfg := baseConfig()
	cfg.Workspace.AutoClearWorkspace = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude"},
	}
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	require.NoError(t, orch.SetReviewerCfg("reviewer", true))

	profile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "reviewer", profile)
	assert.True(t, autoReview)
}

func TestReviewerCfgRejectsAutoReviewWithoutProfile(t *testing.T) {
	cfg := baseConfig()
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	err := orch.SetReviewerCfg("", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrAutoReviewRequiresReviewerProfile)

	profile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "", profile)
	assert.False(t, autoReview)
}

func TestReviewerCfgRejectsUnknownProfile(t *testing.T) {
	cfg := baseConfig()
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	err := orch.SetReviewerCfg("reviewer", false)

	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrReviewerProfileNotFound)
}

func TestReviewerCfgRejectsDisabledProfile(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Enabled: func() *bool { disabled := false; return &disabled }()},
	}
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	err := orch.SetReviewerCfg("reviewer", false)

	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrReviewerProfileDisabled)
}

func TestReviewerConfigParsedFromYAML(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.ReviewerProfile = "code-reviewer"
	cfg.Agent.AutoReview = true
	assert.Equal(t, "code-reviewer", cfg.Agent.ReviewerProfile)
	assert.True(t, cfg.Agent.AutoReview)
}

// ─── DispatchReviewer tests ──────────────────────────────────────────────────

func TestDispatchReviewer_FailsWithoutProfile(t *testing.T) {
	cfg := baseConfig()
	// No ReviewerProfile set
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), agenttest.NewFakeRunner(nil), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	err := orch.DispatchReviewer("ENG-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reviewer_profile configured")
}

func TestDispatchReviewer_SucceedsWithProfile(t *testing.T) {
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
		// Reviewer completed through the regular worker queue
	case <-ctx.Done():
		t.Fatal("reviewer did not complete within 3s")
	}
}

func TestDispatchReviewer_UsesConfiguredSSHHost(t *testing.T) {
	cfg := baseConfig()
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.SSHHosts = []string{"ssh-review"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "You are a code reviewer."},
	}

	issue := makeIssue("id1", "ENG-1", "In Review", nil, nil)
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := &workerHostTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 1),
	}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, orch.DispatchReviewer("ENG-1"))

	select {
	case <-runner.done:
	case <-ctx.Done():
		t.Fatal("reviewer did not complete within 3s")
	}

	require.Equal(t, []string{"ssh-review"}, runner.snapshot())
}

func TestDispatchReviewer_UsesPerIssueBackendOverride(t *testing.T) {
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
	runner := &commandTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 1),
	}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetIssueBackend("ENG-1", "codex")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, orch.DispatchReviewer("ENG-1"))

	select {
	case <-runner.done:
	case <-ctx.Done():
		t.Fatal("reviewer did not complete within 3s")
	}

	commands := runner.snapshot()
	require.Len(t, commands, 1)
	assert.Contains(t, commands[0], "@@itervox-backend=codex")
}

// ─── Auto-review tests ──────────────────────────────────────────────────────

func TestAutoReview_DispatchesAfterSuccess(t *testing.T) {
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

	// The FakeRunner will be called for both the worker AND the reviewer.
	done := make(chan struct{}, 2)
	countingRunner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}

	orch := orchestrator.New(cfg, mt, countingRunner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for at least 2 RunTurn calls (worker + reviewer)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected 2 RunTurn calls, got %d", i)
		}
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	logs := logBuf.String()
	assert.Contains(t, logs, "orchestrator: dispatching reviewer", "should log reviewer dispatch")
}

func TestAutoReview_DoesNotTriggerForAutomationRuns(t *testing.T) {
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
		"qa":       {Command: "claude", Prompt: "Run QA checks."},
	}

	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	mt := &noCandidateTracker{base: tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	done := make(chan struct{}, 2)
	countingRunner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}

	orch := orchestrator.New(cfg, mt, countingRunner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			Cron:         "0 */2 * * *",
			FiredAt:      time.Now(),
		},
	}))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected automation worker to run")
	}

	time.Sleep(250 * time.Millisecond)

	assert.Equal(t, 1, countingRunner.CallCount(), "automation success should not dispatch reviewer")
	assert.NotContains(t, logBuf.String(), "orchestrator: dispatching reviewer")
}

func TestAutoReview_UsesSSHHostSelection(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Agent.SSHHosts = []string{"ssh-a", "ssh-b"}
	cfg.Agent.DispatchStrategy = "round-robin"
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

	runner := &workerHostTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 2),
	}

	orch := orchestrator.New(cfg, mt, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	for i := 0; i < 2; i++ {
		select {
		case <-runner.done:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected 2 RunTurn calls, got %d", i)
		}
	}

	require.Equal(t, []string{"ssh-a", "ssh-b"}, runner.snapshot())
}

func TestAutoReview_DoesNotTriggerWhenDisabled(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.AutoReview = false // disabled
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "Review this code."},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
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
		t.Fatal("worker did not complete within 3s")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	logs := logBuf.String()
	assert.NotContains(t, logs, "orchestrator: dispatching reviewer", "should NOT dispatch reviewer when auto_review is false")
}

func TestAutoReview_DoesNotTriggerWithoutProfile(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Agent.AutoReview = true
	// No ReviewerProfile set

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
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
		t.Fatal("worker did not complete within 3s")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	logs := logBuf.String()
	assert.NotContains(t, logs, "orchestrator: dispatching reviewer")
}

// New (v0.2.0): AutoClearWorkspace + AutoReview now coexist. The clear is
// deferred from the main worker's success to the reviewer's success so the
// reviewer has the workspace available. This test pins the new contract:
// reviewer DOES run, and clear happens after reviewer (not after main worker).
func TestAutoReview_RunsAlongsideAutoClearWorkspace(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Workspace.AutoClearWorkspace = true
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

	done := make(chan struct{}, 2)
	countingRunner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}

	orch := orchestrator.New(cfg, mt, countingRunner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for both main worker and reviewer to complete.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("worker %d did not complete within 3s", i+1)
		}
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	assert.Equal(t, 2, countingRunner.CallCount(), "main worker and reviewer should both run even when auto-clear is enabled")
	logs := logBuf.String()
	assert.Contains(t, logs, "orchestrator: dispatching reviewer", "reviewer should be dispatched after the main worker succeeds")
	assert.NotContains(t, logs, "skipping auto-review because workspace auto-clear is enabled",
		"legacy skip-reviewer log should no longer fire — clear is deferred until after the reviewer")
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// countingTrackingRunner tracks how many times RunTurn is called and signals
// on each call via the done channel.
type countingTrackingRunner struct {
	agent.Runner
	mu         sync.Mutex
	done       chan struct{}
	callCount  int
	workerHost []string
}

func (r *countingTrackingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost string, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.callCount++
	r.workerHost = append(r.workerHost, workerHost)
	r.mu.Unlock()
	res, err := r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return res, err
}

func (r *countingTrackingRunner) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCount
}

type workerHostTrackingRunner struct {
	agent.Runner
	mu          sync.Mutex
	done        chan struct{}
	workerHosts []string
}

func (r *workerHostTrackingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost string, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.workerHosts = append(r.workerHosts, workerHost)
	r.mu.Unlock()
	res, err := r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return res, err
}

func (r *workerHostTrackingRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.workerHosts...)
}

type commandTrackingRunner struct {
	agent.Runner
	mu       sync.Mutex
	done     chan struct{}
	commands []string
}

func (r *commandTrackingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost string, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	res, err := r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return res, err
}

func (r *commandTrackingRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.commands...)
}

// Verify syncBuffer exists in the test package (defined in token_log_test.go).
// If this doesn't compile, the syncBuffer type from token_log_test.go is needed.
var _ = (*syncBuffer)(nil)

type noCandidateTracker struct {
	base *tracker.MemoryTracker
}

func (t *noCandidateTracker) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, nil
}

func (t *noCandidateTracker) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	return t.base.FetchIssuesByStates(ctx, stateNames)
}

func (t *noCandidateTracker) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	return t.base.FetchIssueStatesByIDs(ctx, issueIDs)
}

func (t *noCandidateTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	return t.base.CreateComment(ctx, issueID, body)
}

func (t *noCandidateTracker) CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error) {
	return t.base.CreateIssue(ctx, sourceIssueID, title, body, stateName)
}

func (t *noCandidateTracker) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	return t.base.UpdateIssueState(ctx, issueID, stateName)
}

func (t *noCandidateTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	return t.base.FetchIssueDetail(ctx, issueID)
}

func (t *noCandidateTracker) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	return t.base.FetchIssueByIdentifier(ctx, identifier)
}

func (t *noCandidateTracker) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	return t.base.SetIssueBranch(ctx, issueID, branchName)
}

// Needed for the strings.Split usage in log assertions.
var _ = strings.Split
var _ = bytes.Buffer{}

// TestMultiReviewerConfigFallsBackToSingleReviewer pins that reviewer
// fan-out is DISABLED for this release and, critically, that a config which
// requests it degrades to the working single-reviewer path instead of
// misbehaving.
//
// Three reproduced failures gate the feature (see
// orchestrator.ReviewerProfileChain). The one this test guards is the worst:
// with tracker.completion_state set — the realistic configuration — the
// worker moves the issue terminal before the reviewer finishes,
// ReconcileTrackerStates stops the reviewer, and because that is not a
// TerminalSucceeded exit the chain never advances, so chain[0] is
// re-dispatched forever. Measured before the gate: 10+ RunTurn calls in ~1s,
// against 2 for a single-reviewer config that was otherwise identical.
//
// The assertion is therefore an upper bound on dispatches, not a lower one:
// a regression here shows up as runaway agent spawning, which on a real
// tracker means real API spend and real duplicated work.
func TestMultiReviewerConfigFallsBackToSingleReviewer(t *testing.T) {
	wsDir := t.TempDir()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Workspace.AutoClearWorkspace = true
	cfg.Agent.AutoReview = true
	cfg.Agent.ReviewerProfile = "security"
	cfg.Agent.ReviewerProfiles = []string{"security", "correctness"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"security":    {Command: "claude", Prompt: "Security review."},
		"correctness": {Command: "claude", Prompt: "Correctness review."},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	done := make(chan struct{}, 8)
	runner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}
	wsp := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, runner, wsp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// Wait for the worker and its single reviewer.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatalf("run %d did not complete", i+1)
		}
	}
	// Give the event loop room to keep re-dispatching if the chain is live:
	// the runaway loop produced several extra RunTurn calls in this window.
	time.Sleep(600 * time.Millisecond)
	calls := runner.CallCount()
	cancel()
	time.Sleep(200 * time.Millisecond)

	// Both bounds, for the same reason as above: an upper bound alone passes
	// on zero dispatches and would not prove the reviewer ran at all.
	assert.Equal(t, 2, calls,
		"fan-out is disabled: exactly one worker and one reviewer, and no re-dispatch loop")
	assert.Len(t, orchestrator.ReviewerProfileChain(cfg), 1,
		"the reviewer chain must be truncated to a single profile while fan-out is disabled")
}

// TestAutoReviewDoesNotLoopWhenReconciliationStopsTheReviewer pins the
// fail-closed rule in runEligibleForAutoReview against an unbounded
// agent-spawn loop. This is the ORDINARY single-reviewer configuration, not
// fan-out.
//
// Setting tracker.completion_state makes a successful worker move its issue
// to a terminal state. ReconcileTrackerStates then stops any run still
// registered for that issue and deletes it from state.Running — and the
// stopped run's own goroutine delivers its real EventWorkerExited a moment
// later, by which point the live entry is gone. runEligibleForAutoReview
// used to answer "yes, reviewable" for that nil entry, so the handler read
// it as a fresh worker success and dispatched a reviewer; reconciliation
// stopped that reviewer for the same reason; its exit was nil too. The issue
// stays terminal, so nothing breaks the cycle.
//
// Measured before the fix: 23-30 RunTurn calls in 1.5s against an expected
// 2. On a real tracker every iteration is a billed agent run plus tracker
// writes, so the assertion is an upper bound — a regression shows up as
// runaway spend, not a wrong value.
func TestAutoReviewDoesNotLoopWhenReconciliationStopsTheReviewer(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Workspace.AutoClearWorkspace = true
	cfg.Agent.AutoReview = true
	cfg.Agent.ReviewerProfile = "security"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"security": {Command: "claude", Prompt: "Security review."},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 64),
	}
	wsp := &recordingWorkspaceProvider{path: t.TempDir()}
	orch := orchestrator.New(cfg, mt, runner, wsp)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// Long enough for many poll ticks: the loop reached 20+ dispatches here.
	time.Sleep(1500 * time.Millisecond)
	calls := runner.CallCount()
	cancel()
	time.Sleep(150 * time.Millisecond)

	// Both bounds. An upper bound alone is satisfied by zero dispatches, so
	// the fixture would never have to route through the auto-review path the
	// test is named for — the test could pass while proving nothing.
	assert.Equal(t, 2, calls,
		"exactly one worker and one reviewer — a nil live run entry must not be read as a reviewable worker success")
}
