package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// promptCaptureRunner records every prompt the orchestrator sends in.
// Used to verify the worker's prompt assembly (WORKFLOW.md + handoff + run
// context + profile blocks).
type promptCaptureRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	done    chan struct{}
}

func (r *promptCaptureRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, prompt)
	calls := r.calls
	r.mu.Unlock()
	// Signal on the first call so the test can complete without polling.
	if calls == 1 && r.done != nil {
		select {
		case r.done <- struct{}{}:
		default:
		}
	}
	// Return a successful no-op so the worker exits cleanly.
	return agent.TurnResult{
		SessionID:    "test-session",
		InputTokens:  10,
		OutputTokens: 5,
		ResultText:   "ok",
	}, nil
}

func (r *promptCaptureRunner) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string{}, r.prompts...)
}

// recordingWorkspaceProvider tracks RemoveWorkspace invocations so tests can
// assert that AutoClearWorkspace fires (or does not fire) at the expected
// points in the worker lifecycle.
type recordingWorkspaceProvider struct {
	mu               sync.Mutex
	path             string
	removeCalls      atomic.Int64
	removeIdentifier []string
}

func (p *recordingWorkspaceProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: p.path, Identifier: identifier, CreatedNow: false}, nil
}

func (p *recordingWorkspaceProvider) RemoveWorkspace(_ context.Context, identifier, _ string) error {
	p.removeCalls.Add(1)
	p.mu.Lock()
	p.removeIdentifier = append(p.removeIdentifier, identifier)
	p.mu.Unlock()
	return nil
}

func (p *recordingWorkspaceProvider) ResolvePath(_ string) string {
	return p.path
}

func (p *recordingWorkspaceProvider) removeCount() int64 {
	return p.removeCalls.Load()
}

// 9A — End-to-end: a prior agent's handoff file already on disk is read by
// the orchestrator, formatted as a "## Prior Agent Handoffs" block, and
// included in the next worker's prompt. The worker also receives a
// "## Run Context" block with its own run.handoff_path.
func TestHandoffFlowsBetweenSuccessiveWorkerRuns(t *testing.T) {
	wsDir := t.TempDir()
	// Pre-populate a prior agent's handoff file as if a researcher had run
	// before this dispatch.
	handoffDir := filepath.Join(wsDir, ".itervox", "handoff")
	require.NoError(t, os.MkdirAll(handoffDir, 0o755))
	priorHandoff := "## Researcher findings\n\nThe issue lives in `internal/foo/`.\nRelated tests: `internal/foo/foo_test.go`.\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(handoffDir, "2026-05-26T10-00-00Z_researcher.md"),
		[]byte(priorHandoff),
		0o644,
	))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 30
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Implement {{ issue.identifier }}: {{ issue.title }}."
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {
			Command:      "claude",
			Soul:         "You are an implementer.",
			Instructions: "Read prior handoffs. Implement the smallest complete change.",
		},
	}
	// Route this issue's dispatch to the implementer profile via the
	// per-issue profile override (the orchestrator's documented API surface
	// for "use this profile for this issue").

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	wsProvider := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, runner, wsProvider)
	// Route this issue to the implementer profile. SetIssueProfile is the
	// documented setter for per-issue profile assignment (the rate-limit
	// auto-switch path uses the same field).
	orch.SetIssueProfile("ENG-1", "implementer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-runner.done:
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not run within 4s")
	}
	cancel()

	calls, prompts := runner.snapshot()
	require.GreaterOrEqual(t, calls, 1, "worker should have been invoked at least once")
	require.NotEmpty(t, prompts)

	prompt := prompts[0]

	// WORKFLOW.md template at the lead of the prompt.
	assert.Contains(t, prompt, "Implement ENG-1: T.",
		"rendered WORKFLOW.md template should lead the prompt")

	// Prior Agent Handoffs block, with the researcher's content inlined.
	assert.Contains(t, prompt, "## Prior Agent Handoffs",
		"orchestrator must prepend a Prior Agent Handoffs block before profile content")
	assert.Contains(t, prompt, "2026-05-26T10-00-00Z_researcher.md",
		"the prior handoff filename should appear as a sub-heading")
	assert.Contains(t, prompt, "The issue lives in `internal/foo/`.",
		"the prior handoff content must be inlined verbatim")

	// Run Context block with run.timestamp and run.handoff_path.
	assert.Contains(t, prompt, "## Run Context",
		"orchestrator must include a Run Context block")
	assert.Contains(t, prompt, "run.timestamp",
		"Run Context should expose run.timestamp")
	assert.Contains(t, prompt, "run.handoff_path",
		"Run Context should expose run.handoff_path")
	assert.Regexp(t,
		`\.itervox/handoff/\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}(\.\d+)?Z_implementer\.md`,
		prompt,
		"the handoff_path should be derived from run.timestamp and the active profile name (timestamp may include fractional seconds)",
	)

	// Profile prompt blocks should also appear after the handoff/run-context
	// (proving the assembly order is WORKFLOW → handoff → run-context → profile).
	assert.Contains(t, prompt, "You are an implementer.",
		"SOUL content should be appended after the run-context block")
	priorIdx := strings.Index(prompt, "## Prior Agent Handoffs")
	runCtxIdx := strings.Index(prompt, "## Run Context")
	soulIdx := strings.Index(prompt, "You are an implementer.")
	require.True(t, priorIdx > 0 && runCtxIdx > priorIdx && soulIdx > runCtxIdx,
		"assembly order must be: rendered WORKFLOW.md → Prior Handoffs → Run Context → profile blocks")
}

// chainingRunner emulates a real agent that:
//   - reads its assigned profile (passed via WriteProfile)
//   - on call N, writes its deliverable to the path recorded in the prompt's
//     "## Run Context" block
//   - captures every prompt it sees so the test can inspect cross-run flow
//
// This drives the real handoff chain: each invocation writes a file to disk
// that the next dispatched worker reads through buildHandoffContextBlock.
type chainingRunner struct {
	mu           sync.Mutex
	calls        int
	prompts      []string
	wsPath       string
	deliverables map[string]string // role -> body to write on next call
	currentRole  string
	done         chan int
}

func (r *chainingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, prompt)
	calls := r.calls
	role := r.currentRole
	body, hasDeliverable := r.deliverables[role]
	r.mu.Unlock()

	// Extract run.handoff_path from the Run Context block in the prompt.
	if hasDeliverable {
		const marker = "run.handoff_path: `"
		if _, rest, found := strings.Cut(prompt, marker); found {
			end := strings.Index(rest, "`")
			if end > 0 {
				handoffRel := rest[:end]
				abs := filepath.Join(r.wsPath, handoffRel)
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
					_ = os.WriteFile(abs, []byte(body), 0o644)
				}
			}
		}
	}

	// Signal once per call so the test can step through.
	if r.done != nil {
		select {
		case r.done <- calls:
		default:
		}
	}

	return agent.TurnResult{
		SessionID:    fmt.Sprintf("chain-session-%d", calls),
		InputTokens:  10,
		OutputTokens: 5,
		ResultText:   "ok",
	}, nil
}

func (r *chainingRunner) setRole(role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentRole = role
}

func (r *chainingRunner) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string{}, r.prompts...)
}

// G4 — End-to-end: two successive workers on the same issue, where the
// first worker's actual filesystem write becomes the second worker's
// `## Prior Agent Handoffs` block. No pre-seeding — the chain runs through
// the orchestrator's dispatch path twice.
func TestActualChainBetweenTwoWorkersOnSameIssue(t *testing.T) {
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 30
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }}: {{ issue.title }}."
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"researcher":  {Command: "claude", Soul: "You are a researcher."},
		"implementer": {Command: "claude", Soul: "You are an implementer."},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &chainingRunner{
		wsPath: wsDir,
		deliverables: map[string]string{
			"researcher": "## Researcher findings\n\nIssue lives in `internal/foo/`.\nKey risk: timezone handling on retry.",
		},
		done: make(chan int, 4),
	}
	wsProvider := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, runner, wsProvider)

	// === Dispatch worker A (researcher) ===
	runner.setRole("researcher")
	orch.SetIssueProfile("ENG-1", "researcher")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// Wait for worker A to run.
	select {
	case <-runner.done:
	case <-time.After(4 * time.Second):
		t.Fatal("worker A (researcher) did not run within 4s")
	}

	// Wait for issue to transition to terminal (worker succeeded → Done).
	require.Eventually(t, func() bool {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		if len(issues) != 1 {
			return false
		}
		return issues[0].State == "Done"
	}, 3*time.Second, 50*time.Millisecond, "issue should transition to Done after worker A succeeds")

	// Verify the researcher actually wrote a handoff file to disk.
	handoffDir := filepath.Join(wsDir, ".itervox", "handoff")
	entries, err := os.ReadDir(handoffDir)
	require.NoError(t, err, "handoff directory should exist after worker A's write")
	require.NotEmpty(t, entries, "worker A should have left a handoff file on disk")
	var researcherFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_researcher.md") {
			researcherFile = e.Name()
			break
		}
	}
	require.NotEmpty(t, researcherFile, "should find a *_researcher.md handoff file")

	// === Move issue back to active state and dispatch worker B (implementer) ===
	mt.SetIssueState("id1", "In Progress")
	runner.setRole("implementer")
	orch.SetIssueProfile("ENG-1", "implementer")

	// Wait for worker B to be invoked.
	select {
	case <-runner.done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker B (implementer) did not run within 5s")
	}

	cancel()

	// === Assertions: worker B's prompt contains worker A's deliverable ===
	calls, prompts := runner.snapshot()
	require.GreaterOrEqual(t, calls, 2, "both workers should have been invoked")
	require.GreaterOrEqual(t, len(prompts), 2)

	bPrompt := prompts[len(prompts)-1] // last prompt = worker B

	assert.Contains(t, bPrompt, "## Prior Agent Handoffs",
		"worker B's prompt must include the prior-handoffs block")
	assert.Contains(t, bPrompt, researcherFile,
		"worker B's prompt should reference researcher's filename as a sub-heading")
	assert.Contains(t, bPrompt, "Issue lives in `internal/foo/`.",
		"worker B's prompt must inline researcher's deliverable content verbatim")
	assert.Contains(t, bPrompt, "Key risk: timezone handling on retry.",
		"full researcher content should reach worker B")
	assert.Contains(t, bPrompt, "## Run Context",
		"worker B should also receive a Run Context block")
	assert.Regexp(t, `_implementer\.md`, bPrompt,
		"worker B's Run Context handoff_path should be for the implementer profile, not researcher")
}

// 9B — When a worker exhausts retries and the issue moves to FailedState,
// the orchestrator schedules a workspace clear. This pins the "terminal
// tracker state" half of the v0.2.0 semantic change: failure is also
// terminal.
func TestAutoClearFiresOnTerminalFailureAfterRetriesExhausted(t *testing.T) {
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1         // exhaust quickly
	cfg.Agent.MaxRetryBackoffMs = 10 // back-off ~0 so retry fires immediately
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.FailedState = "Failed" // terminal state on max-retries-exhausted
	cfg.Tracker.TerminalStates = append(cfg.Tracker.TerminalStates, "Failed")
	cfg.Workspace.AutoClearWorkspace = true // arm the new terminal-only clear

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	// Runner that always fails — the worker will exhaust retries.
	failRunner := &alwaysFailingRunner{}
	wsProvider := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, failRunner, wsProvider)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// Wait for the issue to land in Failed state — that's the signal the
	// orchestrator has decided the run is terminally failed.
	deadline := time.After(5 * time.Second)
	for {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		require.Len(t, issues, 1)
		if issues[0].State == "Failed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("issue did not reach Failed state; current=%s, failRunner.calls=%d", issues[0].State, failRunner.calls.Load())
		case <-time.After(30 * time.Millisecond):
		}
	}

	// Give the workspace-clear goroutine a moment to run.
	require.Eventually(t, func() bool {
		return wsProvider.removeCount() >= 1
	}, 3*time.Second, 30*time.Millisecond,
		"RemoveWorkspace must be called when the issue reaches FailedState with auto_clear enabled")
	cancel()
}

// F2 — a worker that exits clean WITHOUT writing its handoff must not reach
// TerminalSucceeded unmodified: the orchestrator synthesizes the handoff from
// the session summary before the completion-state transition, so "done"
// always includes the durable shared-state update.
func TestSuccessWithoutHandoffSynthesizesDeliverable(t *testing.T) {
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	// promptCaptureRunner exits successfully without ever writing a handoff.
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	wsProvider := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, runner, wsProvider)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// The completion-state transition happens strictly AFTER handoff
	// synthesis on the success path, so once the issue is Done the
	// synthesized file must already exist.
	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 5*time.Second, 30*time.Millisecond, "issue must reach the completion state")

	handoffDir := filepath.Join(wsDir, ".itervox", "handoff")
	entries, err := os.ReadDir(handoffDir)
	require.NoError(t, err, "handoff dir must exist after a successful run with no agent handoff")
	var synthesized int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(handoffDir, e.Name()))
		require.NoError(t, readErr)
		if strings.Contains(string(data), "Synthesized handoff") {
			synthesized++
			assert.NotContains(t, e.Name(), ".partial.md",
				"a synthesized handoff on the success path must not be marked partial")
		}
	}
	require.GreaterOrEqual(t, synthesized, 1,
		"the orchestrator must synthesize a handoff when the agent wrote none")
	cancel()
}

// alwaysFailingRunner returns a Failed turn result on every call so the
// orchestrator's retry loop runs to exhaustion.
type alwaysFailingRunner struct {
	calls atomic.Int64
}

func (r *alwaysFailingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int) (agent.TurnResult, error) {
	r.calls.Add(1)
	// FailureText must be non-empty — the worker treats Failed=true with
	// empty FailureText and zero tokens as "clean session end" rather than
	// a real failure.
	return agent.TurnResult{
		Failed:        true,
		FailureText:   "deliberate test failure",
		AllTextBlocks: []string{"deliberate test failure"},
		InputTokens:   1,
		OutputTokens:  1,
	}, nil
}

// mustGit runs `git -C dir args...` and fails the test on error.
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitOutput runs `git -C dir args...` and returns trimmed stdout, failing on error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WORK-1: the synthesized handoff must be COMMITTED on the issue branch, not
// left as an untracked working-tree file. When the workspace is a real git
// repo, the orchestrator scopes a `git add .itervox/handoff` + commit to the
// issue branch on the success path so the deliverable survives auto-clear and
// reaches reviewers via the PR push.
func TestSynthesizedHandoffIsCommitted(t *testing.T) {
	wsDir := t.TempDir()
	mustGit(t, wsDir, "init", "-b", "main")
	mustGit(t, wsDir, "config", "user.email", "test@example.com")
	mustGit(t, wsDir, "config", "user.name", "Test")
	mustGit(t, wsDir, "commit", "--allow-empty", "-m", "root")

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	// promptCaptureRunner exits successfully without ever writing a handoff,
	// so the orchestrator synthesizes one and must commit it.
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	wsProvider := &recordingWorkspaceProvider{path: wsDir}
	orch := orchestrator.New(cfg, mt, runner, wsProvider)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 5*time.Second, 30*time.Millisecond, "issue must reach the completion state")

	out := gitOutput(t, wsDir, "log", "--oneline", "--", ".itervox/handoff")
	require.NotEmpty(t, out, "handoff must be committed on the branch (WORK-1)")
	cancel()
}
