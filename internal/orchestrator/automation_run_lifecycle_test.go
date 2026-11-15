package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// helperAnswerRunner plays both sides of an input_required automation: the
// implementer asks a question on its first turn and finishes on the resumed
// turn; the input-responder helper answers through provide-input (the agent
// action, FromAgent) and reports success. Every turn reports tokens and a
// distinct result, so neither the 0-token nor the duplicate-result early exit
// ends a run: only the turn budget does.
type helperAnswerRunner struct {
	mu             sync.Mutex
	orch           *orchestrator.Orchestrator
	responderTurns int
	implFresh      int
	implResumed    int
	resumePrompts  []string
	answerErr      error
}

func (r *helperAnswerRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(prompt, "RESPONDER-ROLE") {
		r.responderTurns++
		if r.responderTurns == 1 {
			r.answerErr = r.orch.ProvideAgentInput("ENG-1", "Continue on the existing branch.")
		}
		return agent.TurnResult{SessionID: "helper-session", InputTokens: 5, OutputTokens: 3,
			ResultText: fmt.Sprintf("answered the question (rerun %d)", r.responderTurns)}, nil
	}
	if sessionID == nil || *sessionID == "" {
		r.implFresh++
		return agent.TurnResult{SessionID: "impl-session", InputTokens: 7, OutputTokens: 3,
			InputRequired: true, FailureText: "Which branch should I continue on?"}, nil
	}
	r.implResumed++
	r.resumePrompts = append(r.resumePrompts, prompt)
	return agent.TurnResult{SessionID: "impl-session", InputTokens: 9, OutputTokens: 4, ResultText: "implemented"}, nil
}

func (r *helperAnswerRunner) counts() (responder, fresh, resumed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.responderTurns, r.implFresh, r.implResumed
}

// TestHelperAutomationRunDoesNotRepeatAfterAnswering: an input_required
// automation's helper answers the blocked run through provide-input on its
// first turn. Its issue stays in an active state, but the helper's job is
// done: it takes that one turn, not max_turns continuation turns of the same
// prompt. The answer resumes the original run exactly once.
func TestHelperAutomationRunDoesNotRepeatAfterAnswering(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 8
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer":     {Command: "claude", Instructions: "IMPLEMENTER-ROLE"},
		"input-responder": {Command: "claude", Instructions: "RESPONDER-ROLE", AllowedActions: []string{"comment", "provide_input"}},
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	runner := &helperAnswerRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	runner.orch = orch
	orch.SetIssueProfile("ENG-1", "implementer")
	orch.SetInputRequiredAutomations([]orchestrator.InputRequiredAutomation{{
		ID:                "input-responder",
		ProfileName:       "input-responder",
		InputContextRegex: regexp.MustCompile(`branch`),
		AutoResume:        true,
	}})

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.Eventually(t, func() bool {
		_, _, resumed := runner.counts()
		return resumed >= 1 && issueState(t, mt) == "Done"
	}, 10*time.Second, 20*time.Millisecond, "the helper's answer resumes the implementer, which completes")
	// Let any further (wrong) helper turn or resume show up.
	time.Sleep(300 * time.Millisecond)

	responder, fresh, resumed := runner.counts()
	assert.Equal(t, 1, responder, "the helper takes one turn, not max_turns continuation turns")
	assert.Equal(t, 1, fresh, "the implementer asked once")
	assert.Equal(t, 1, resumed, "the original run is resumed exactly once")
	runner.mu.Lock()
	assert.NoError(t, runner.answerErr)
	require.Len(t, runner.resumePrompts, 1)
	assert.Contains(t, runner.resumePrompts[0], "Continue on the existing branch.", "the resumed turn carries the helper's answer")
	runner.mu.Unlock()
	assert.Empty(t, orch.Snapshot().InputRequiredIssues, "the input request was resolved")

	var helperRuns int
	for _, h := range orch.RunHistory() {
		if h.AutomationID == "input-responder" {
			helperRuns++
			assert.Equal(t, "succeeded", h.Status)
			assert.Equal(t, 1, h.TurnCount)
		}
	}
	assert.Equal(t, 1, helperRuns, "one helper run")
}

// continuationRunner always reports work, with a distinct result per turn.
type continuationRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *continuationRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return agent.TurnResult{SessionID: "s", InputTokens: 5, OutputTokens: 3, ResultText: fmt.Sprintf("turn %d", r.calls)}, nil
}

func (r *continuationRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestWorkerRunKeepsContinuationTurnsWhileIssueActive: the single-turn rule
// for automation runs leaves worker runs alone. A worker whose issue stays in
// an active state keeps taking continuation turns up to max_turns; an
// automation run on the same active issue takes one.
func TestWorkerRunKeepsContinuationTurnsWhileIssueActive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		automation bool
		wantTurns  int
	}{
		{name: "worker", automation: false, wantTurns: 3},
		{name: "automation", automation: true, wantTurns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Polling.IntervalMs = 20
			cfg.Agent.MaxTurns = 3
			cfg.Agent.Profiles = map[string]config.AgentProfile{"helper": {Command: "claude"}}
			issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
			candidateStates := cfg.Tracker.ActiveStates
			if tc.automation {
				// The tracker does not offer the issue as a candidate, so no
				// worker is dispatched for it; to the daemon it is active.
				candidateStates = []string{"Todo"}
			}
			mt := tracker.NewMemoryTracker([]domain.Issue{issue}, candidateStates, cfg.Tracker.TerminalStates)
			runner := &continuationRunner{}
			orch := orchestrator.New(cfg, mt, runner, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				_ = orch.Run(ctx)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				<-done
			})
			if tc.automation {
				require.True(t, orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
					AutomationID: "cron-helper",
					ProfileName:  "helper",
					Trigger:      orchestrator.AutomationTriggerContext{Type: config.AutomationTriggerCron, FiredAt: time.Now(), AutomationID: "cron-helper"},
				}))
			}
			var run *orchestrator.CompletedRun
			require.Eventually(t, func() bool {
				for _, h := range orch.RunHistory() {
					if h.Identifier == "ENG-1" && (h.AutomationID != "") == tc.automation {
						run = &h
						return true
					}
				}
				return false
			}, 8*time.Second, 10*time.Millisecond, "the run finishes")
			assert.Equal(t, tc.wantTurns, run.TurnCount)
			if !tc.automation {
				assert.Equal(t, tc.wantTurns, runner.count(), "max_turns turns while the issue stays active")
			}
		})
	}
}

// chainRunner plays a researcher → implementer automation chain: each run
// writes its handoff at the run.handoff_path its prompt names.
type chainRunner struct {
	mu      sync.Mutex
	prompts map[string][]string
}

var handoffPathRe = regexp.MustCompile("run\\.handoff_path: `([^`]+)`")

func (r *chainRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, workspacePath, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	role, body := "implementer", "IMPLEMENTATION-NOTES: changed internal/foo."
	if strings.Contains(prompt, "RESEARCHER-ROLE") {
		role, body = "researcher", "RESEARCH-FINDINGS-42: the bug lives in internal/foo."
	}
	r.mu.Lock()
	if r.prompts == nil {
		r.prompts = map[string][]string{}
	}
	r.prompts[role] = append(r.prompts[role], prompt)
	r.mu.Unlock()
	m := handoffPathRe.FindStringSubmatch(prompt)
	if m == nil {
		return agent.TurnResult{}, fmt.Errorf("no run.handoff_path in the %s prompt", role)
	}
	path := filepath.Join(workspacePath, m[1])
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agent.TurnResult{}, err
	}
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		return agent.TurnResult{}, err
	}
	return agent.TurnResult{SessionID: "s-" + role, InputTokens: 5, OutputTokens: 3, ResultText: role + " done"}, nil
}

func (r *chainRunner) promptsOf(role string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts[role]...)
}

// TestAutomationChainCarriesHandoffs: two issue_entered_state automation
// runs on one issue (researcher, then implementer) share the issue's
// worktree. The researcher's handoff is committed on the issue branch and is
// in the implementer's "## Prior Agent Handoffs"; the implementer's handoff
// is committed too. With workspace.auto_clear on, an automation run's
// success does not remove the worktree (and branch) of an issue that is not
// terminal: its lifecycle is not over.
func TestAutomationChainCarriesHandoffs(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "config", "user.email", "t@t")
	git(root, "config", "user.name", "t")
	git(root, "commit", "-q", "--allow-empty", "-m", "base")

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.BaseBranch = "main"
	cfg.Workspace.AutoClearWorkspace = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"researcher":  {Command: "claude", Instructions: "RESEARCHER-ROLE"},
		"implementer": {Command: "claude", Instructions: "IMPLEMENTER-ROLE"},
	}
	issue := makeIssue("id1", "ENG-1", "Research", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &chainRunner{}
	orch := orchestrator.New(cfg, mt, runner, workspace.NewManager(cfg))

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	finished := func(id string) bool {
		for _, h := range orch.RunHistory() {
			if h.AutomationID == id {
				return true
			}
		}
		return false
	}
	entered := func(id, profile, st string) orchestrator.AutomationDispatch {
		return orchestrator.AutomationDispatch{
			AutomationID: id,
			ProfileName:  profile,
			Trigger: orchestrator.AutomationTriggerContext{
				Type: config.AutomationTriggerIssueEnteredState, FiredAt: time.Now(), AutomationID: id,
				TriggerState: st, CurrentState: st,
			},
		}
	}
	wt := filepath.Join(root, "worktrees", "ENG-1")

	require.True(t, orch.DispatchAutomation(ctx, issue, entered("research", "researcher", "Research")))
	require.Eventually(t, func() bool { return finished("research") }, 10*time.Second, 10*time.Millisecond, "the researcher finishes")
	// A clear started by the researcher's exit would remove the worktree in
	// the background; give it the time it would take.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wt); os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	issue.State = "Implementation"
	require.True(t, orch.DispatchAutomation(ctx, issue, entered("implement", "implementer", "Implementation")))
	require.Eventually(t, func() bool { return finished("implement") }, 10*time.Second, 10*time.Millisecond, "the implementer finishes")

	impl := runner.promptsOf("implementer")
	require.NotEmpty(t, impl)
	assert.Contains(t, impl[0], "## Prior Agent Handoffs", "the implementer sees the chain's prior handoffs")
	assert.Contains(t, impl[0], "RESEARCH-FINDINGS-42", "the researcher's handoff reaches the implementer")

	require.DirExists(t, wt, "the worktree survives the automation runs of a non-terminal issue")
	branch := git(wt, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Equal(t, 2, strings.Count(git(root, "log", "--format=%s", branch), "chore(itervox): record agent handoff"),
		"both runs' handoffs are committed on the issue branch")
	tracked := git(wt, "ls-files", ".itervox/handoff")
	assert.Contains(t, tracked, "_researcher.md")
	assert.Contains(t, tracked, "_implementer.md")
	assert.Empty(t, git(wt, "status", "--porcelain", "--", ".itervox"), "no untracked handoff is left behind")
}

// closingRunner moves its issue to a terminal state during its turn, as an
// automation's agent closing the issue would.
type closingRunner struct {
	mt *tracker.MemoryTracker
}

func (r *closingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mt.SetIssueState("id1", "Done")
	return agent.TurnResult{SessionID: "s", InputTokens: 5, OutputTokens: 3, ResultText: "closed"}, nil
}

// TestAutomationRunThatEndsTerminalStillAutoClears: an automation run whose
// issue is terminal when it finishes (its agent closed the issue) still
// clears the workspace with auto_clear on, as before: nothing else will use
// it. (Automations are not started on an issue that is already terminal.)
func TestAutomationRunThatEndsTerminalStillAutoClears(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Workspace.AutoClearWorkspace = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{"helper": {Command: "claude"}}
	issue := makeIssue("id1", "ENG-1", "Review", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	ws := &recordingWorkspaceProvider{path: t.TempDir()}
	orch := orchestrator.New(cfg, mt, &closingRunner{mt: mt}, ws)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.True(t, orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
		AutomationID: "close-out",
		ProfileName:  "helper",
		Trigger:      orchestrator.AutomationTriggerContext{Type: config.AutomationTriggerIssueEnteredState, FiredAt: time.Now(), AutomationID: "close-out", CurrentState: "Review"},
	}))
	require.Eventually(t, func() bool { return ws.removeCount() == 1 }, 8*time.Second, 10*time.Millisecond,
		"the workspace of an issue that ended terminal is cleared after the automation run")
}
