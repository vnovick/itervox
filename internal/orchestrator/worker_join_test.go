package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
)

// CORE-026 — Run joins its agent workers (bounded by a grace period) before
// it returns, so nothing a worker does after RunTurn — its final log lines,
// post-run comments, git pushes — overlaps the caller's shutdown/reload work.

// lingeringRunner models the two worker kinds CORE-026 names. The default
// backend's first turn for an issue asks for input (so a later ProvideInput
// launches a worker from the resume path); an automation run (the profile's
// "claude" command) and the resumed run both block until their ctx is
// cancelled and then keep running for linger — the agent's kill window —
// before they return. With ignoreCtx, an automation run blocks until
// release is closed instead.
type lingeringRunner struct {
	linger    time.Duration
	ignoreCtx bool
	release   chan struct{}

	mu      sync.Mutex
	entered map[string]chan struct{}
	exited  map[string]time.Time
}

func newLingeringRunner(linger time.Duration) *lingeringRunner {
	return &lingeringRunner{
		linger:  linger,
		release: make(chan struct{}),
		entered: map[string]chan struct{}{"automation": make(chan struct{}), "resume": make(chan struct{})},
		exited:  map[string]time.Time{},
	}
}

func (r *lingeringRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	kind := "resume"
	switch {
	case command == "claude":
		kind = "automation"
	case sessionID == nil || *sessionID == "":
		return agent.TurnResult{
			SessionID:     "join-session-1",
			InputTokens:   7,
			OutputTokens:  3,
			TotalTokens:   10,
			InputRequired: true,
			FailureText:   "Need approval",
		}, nil
	}
	r.mu.Lock()
	select {
	case <-r.entered[kind]:
	default:
		close(r.entered[kind])
	}
	r.mu.Unlock()
	if r.ignoreCtx && kind == "automation" {
		<-r.release
	} else {
		<-ctx.Done()
		time.Sleep(r.linger)
	}
	r.mu.Lock()
	r.exited[kind] = time.Now()
	r.mu.Unlock()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func (r *lingeringRunner) waitEntered(t *testing.T, kind string) {
	t.Helper()
	select {
	case <-r.entered[kind]:
	case <-time.After(5 * time.Second):
		t.Fatalf("the %s worker never reached RunTurn", kind)
	}
}

func (r *lingeringRunner) exitedAt(kind string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.exited[kind]
	return at, ok
}

// joinTestOrchestrator builds an orchestrator with ENG-1 (active: its first
// turn asks for input) and ENG-2 (backlog: only an automation dispatches it).
func joinTestOrchestrator(t *testing.T, runner agent.Runner) (*orchestrator.Orchestrator, domain.Issue) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Agent.Command = "codex"
	cfg.Agent.Backend = "codex"
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	cfg.PromptTemplate = "Handle {{ issue.identifier }}"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Backend: "claude"},
	}
	branch := "feature/eng-1"
	active := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	active.BranchName = &branch
	backlog := makeIssue("id2", "ENG-2", "Backlog", nil, nil)
	ct := newCommentTracker([]domain.Issue{active, backlog}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	wm := &stableWorkspaceProvider{path: filepath.Join(t.TempDir(), "workspace")}
	orch := orchestrator.New(cfg, ct, runner, wm)
	orch.SetInputRequiredFile(filepath.Join(t.TempDir(), "input_required.json"))
	return orch, backlog
}

func dispatchJoinAutomation(t *testing.T, ctx context.Context, orch *orchestrator.Orchestrator, issue domain.Issue) {
	t.Helper()
	require.True(t, orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
		AutomationID: "join-review",
		ProfileName:  "reviewer",
		Instructions: "Review it.",
		Trigger: orchestrator.AutomationTriggerContext{
			Type:         "issue_moved_to_backlog",
			FiredAt:      time.Now(),
			AutomationID: "join-review",
			CurrentState: "Backlog",
		},
	}))
}

// Run must not return while a worker launched from the automation path
// (automation.go) or from the input-required resume path (event_loop.go's
// processPendingInputResumes) is still running: both exit only linger after
// the cancel, and Run returns only after both have.
func TestRunWaitsForWorkersWithGrace(t *testing.T) {
	runner := newLingeringRunner(400 * time.Millisecond)
	orch, backlog := joinTestOrchestrator(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})

	deadline := time.After(5 * time.Second)
	for {
		if _, ok := orch.Snapshot().InputRequiredIssues["ENG-1"]; ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("ENG-1 never entered input_required")
		case <-time.After(20 * time.Millisecond):
		}
	}
	require.NoError(t, orch.ProvideInput("ENG-1", "approved"))
	dispatchJoinAutomation(t, ctx, orch, backlog)
	runner.waitEntered(t, "resume")
	runner.waitEntered(t, "automation")

	cancel()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s of cancel")
	}
	returned := time.Now()
	for _, kind := range []string{"automation", "resume"} {
		at, ok := runner.exitedAt(kind)
		if assert.True(t, ok, "Run returned while the %s worker was still running (CORE-026)", kind) {
			assert.False(t, at.After(returned), "the %s worker exited after Run returned (CORE-026)", kind)
		}
	}
}

// A worker that outlives the grace period must not hold Run forever: Run
// returns once the grace expires and names the still-running identifier in a
// Warn.
func TestRunGraceExpiryLogsAndReturns(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	runner := newLingeringRunner(0)
	runner.ignoreCtx = true
	defer close(runner.release)
	orch, backlog := joinTestOrchestrator(t, runner)
	const grace = 200 * time.Millisecond
	orch.SetWorkerJoinGrace(grace)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = orch.Run(ctx)
	}()

	dispatchJoinAutomation(t, ctx, orch, backlog)
	runner.waitEntered(t, "automation")
	cancel()
	start := time.Now()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its worker-join grace expired")
	}
	assert.GreaterOrEqual(t, time.Since(start), grace, "Run returned before the grace expired")
	_, exited := runner.exitedAt("automation")
	assert.False(t, exited, "precondition: the worker is still running")

	out := logs.String()
	var warn string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "worker join grace expired") {
			warn = line
		}
	}
	require.NotEmpty(t, warn, "no grace-expiry Warn logged; logs:\n%s", out)
	assert.Contains(t, warn, "ENG-2", "the Warn must name the still-running identifier")
}
