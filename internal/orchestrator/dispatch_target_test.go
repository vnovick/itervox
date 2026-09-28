package orchestrator

// CORE-115 — one dispatch resolver that never pairs a backend hint with a
// command whose binary belongs to the other backend. Before, a per-issue pin
// (or agent.backend, a profile backend, a rate_limited switch_to_backend) of
// codex over a "claude ..." command produced
// "@@itervox-backend=codex claude ...": MultiRunner routed it to the Codex
// runner, which ran `claude ... exec --json`.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// assertNoMismatch fails when the runner command's binary is claude or codex
// and disagrees with the backend the runner will be selected for.
func assertNoMismatch(t *testing.T, tgt dispatchTarget) {
	t.Helper()
	routed := agent.BackendFromCommand(tgt.RunnerCommand)
	if routed == "" {
		routed = tgt.Backend
	}
	assert.Equal(t, tgt.Backend, routed, "runner command %q routes to %q but backend is %q", tgt.RunnerCommand, routed, tgt.Backend)
	_, cleaned := config.ParseBackendHint(tgt.RunnerCommand)
	if cleaned == "" {
		cleaned = tgt.RunnerCommand
	}
	bin := filepath.Base(config.FirstCommandToken(cleaned))
	if config.IsSupportedBackend(bin) {
		assert.Equal(t, tgt.Backend, bin, "runner command %q executes %q under backend %q", tgt.RunnerCommand, bin, tgt.Backend)
	}
}

func TestResolveDispatchTarget_PinCodexOverClaudeCommandNeverPairsMismatch(t *testing.T) {
	cases := []struct {
		name string
		in   dispatchTargetInput
	}{
		{"per-issue pin", dispatchTargetInput{DefaultCommand: "claude --verbose", IssueBackend: "codex"}},
		{"agent.backend", dispatchTargetInput{DefaultCommand: "claude --verbose", DefaultBackend: "codex"}},
		{"profile backend", dispatchTargetInput{DefaultCommand: "codex", Profile: &config.AgentProfile{Command: "claude --verbose", Backend: "codex"}}},
		{"rate_limited switch_to_backend", dispatchTargetInput{DefaultCommand: "claude --verbose", RecoveryBackend: "codex"}},
		{"claude pin over codex command", dispatchTargetInput{DefaultCommand: "codex --model gpt-5", IssueBackend: "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDispatchTarget(tc.in)
			assertNoMismatch(t, got)
			assert.NotEmpty(t, got.Reason, "a refused hint must carry a reason the event loop logs")
			assert.False(t, strings.Contains(got.RunnerCommand, config.BackendHintPrefix),
				"a refused hint must not be encoded: %q", got.RunnerCommand)
		})
	}
	got := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "claude --verbose", IssueBackend: "codex"})
	assert.Equal(t, dispatchTarget{
		Command: "claude --verbose", RunnerCommand: "claude --verbose", Backend: "claude", Reason: got.Reason,
	}, got, "the command's own backend wins")
}

func TestResolveDispatchTarget_WrapperCommandHonoursHint(t *testing.T) {
	got := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "/opt/bin/agent-wrapper --fast", IssueBackend: "codex"})
	assert.Equal(t, dispatchTarget{
		Command:       "/opt/bin/agent-wrapper --fast",
		RunnerCommand: "@@itervox-backend=codex /opt/bin/agent-wrapper --fast",
		Backend:       "codex",
	}, got)

	// The same holds for the recovery override and a profile backend.
	got = resolveDispatchTarget(dispatchTargetInput{
		DefaultCommand: "claude", Profile: &config.AgentProfile{Command: "npx @acme/agent"}, RecoveryBackend: "codex",
	})
	assert.Equal(t, "codex", got.Backend)
	assert.Equal(t, "@@itervox-backend=codex npx @acme/agent", got.RunnerCommand)
	assert.Empty(t, got.Reason)

	// A matching hint is a no-op.
	got = resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "codex", IssueBackend: "codex"})
	assert.Equal(t, dispatchTarget{Command: "codex", RunnerCommand: "codex", Backend: "codex"}, got)
}

// recordingDispatchRunner records every runner command it receives.
type recordingDispatchRunner struct{ commands chan string }

func (r *recordingDispatchRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.commands <- command
	return agent.TurnResult{}, nil
}

// TestResolveDispatchTarget_SameResultForWorkerReviewerAutomation drives the
// three real dispatch paths (worker dispatch, reviewer dispatch, automation
// run) with the same profile and the same per-issue pin, and requires the
// same effective (runner command, backend) from each — with no mismatch.
func TestResolveDispatchTarget_SameResultForWorkerReviewerAutomation(t *testing.T) {
	type observed struct{ command, backend string }
	run := func(t *testing.T, path string) observed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cfg := automationBaseCfg()
		cfg.Agent.Command = "claude --verbose"
		cfg.Agent.MaxTurns = 1
		cfg.Agent.Profiles = map[string]config.AgentProfile{"p": {Command: "claude --model m"}}
		issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
		mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
		runner := &recordingDispatchRunner{commands: make(chan string, 4)}
		o := New(cfg, mt, runner, nil)
		o.SetIssueProfile(issue.Identifier, "p")
		o.SetIssueBackend(issue.Identifier, "codex") // operator pin
		state := NewState(cfg)
		now := time.Now()
		switch path {
		case "worker":
			state = o.dispatch(ctx, state, issue, 0)
		case "reviewer":
			o.dispatchReviewerForIssue(ctx, &state, issue, "p", now)
		case "automation":
			require.True(t, o.startAutomationRun(ctx, &state, issue, now, AutomationDispatch{AutomationID: "a", ProfileName: "p"}))
		}
		entry, ok := state.Running[issue.ID]
		require.True(t, ok, "%s path must start a run", path)
		var cmd string
		select {
		case cmd = <-runner.commands:
		case <-ctx.Done():
			t.Fatalf("%s runner never called", path)
		}
		cancel()
		o.workersWg.Wait(5 * time.Second)
		return observed{command: cmd, backend: entry.Backend}
	}
	worker := run(t, "worker")
	reviewer := run(t, "reviewer")
	automation := run(t, "automation")
	assert.Equal(t, worker, reviewer, "reviewer dispatch must resolve like worker dispatch")
	assert.Equal(t, worker, automation, "automation dispatch must resolve like worker dispatch")
	assertNoMismatch(t, dispatchTarget{RunnerCommand: worker.command, Backend: worker.backend})
	assert.Equal(t, observed{command: "claude --model m", backend: "claude"}, worker)
}

func TestResolveDispatchTarget_ResumeStoredClaudeCommandWithCodexBackendNeverPairsMismatch(t *testing.T) {
	got := resolveResumeTarget("claude --verbose", "codex", "", "claude")
	assertNoMismatch(t, got)
	assert.Equal(t, "codex", got.Backend, "the stored session belongs to codex")
	assert.Equal(t, "codex", got.Command)
	assert.Equal(t, "codex", got.RunnerCommand)
	assert.NotEmpty(t, got.Reason)

	// A stored runner command that already carried the mismatched hint.
	got = resolveResumeTarget("@@itervox-backend=codex claude --verbose", "codex", "", "claude")
	assertNoMismatch(t, got)
	assert.Equal(t, dispatchTarget{Command: "codex", RunnerCommand: "codex", Backend: "codex", Reason: got.Reason}, got)

	// A matching stored command is kept verbatim.
	got = resolveResumeTarget("codex --model gpt-5", "codex", "", "claude")
	assert.Equal(t, dispatchTarget{Command: "codex --model gpt-5", RunnerCommand: "codex --model gpt-5", Backend: "codex"}, got)
}

func TestResolveDispatchTarget_ResumeEmptyCommandStillFallsBackToBackendBinary(t *testing.T) {
	got := resolveResumeTarget("", "codex", "", "claude --verbose")
	assert.Equal(t, dispatchTarget{Command: "codex", RunnerCommand: "codex", Backend: "codex"}, got)
	assertNoMismatch(t, got)

	// Claude with no profile falls back to agent.command.
	got = resolveResumeTarget("", "claude", "", "claude --verbose")
	assert.Equal(t, dispatchTarget{Command: "claude --verbose", RunnerCommand: "claude --verbose", Backend: "claude"}, got)
}

func TestResolveDispatchTarget_ResumeEmptyCommandWithConflictingProfileNeverPairsMismatch(t *testing.T) {
	got := resolveResumeTarget("", "codex", "claude --verbose", "claude")
	assertNoMismatch(t, got)
	assert.Equal(t, "codex", got.Backend)
	assert.Equal(t, "codex", got.Command)
	assert.Equal(t, "codex", got.RunnerCommand)
	assert.NotEmpty(t, got.Reason)

	// A wrapper profile command keeps the hint.
	got = resolveResumeTarget("", "codex", "/opt/bin/agent-wrapper", "claude")
	assert.Equal(t, dispatchTarget{Command: "/opt/bin/agent-wrapper", RunnerCommand: "@@itervox-backend=codex /opt/bin/agent-wrapper", Backend: "codex"}, got)
}
