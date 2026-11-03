package orchestrator_test

import (
	"context"
	"fmt"
	"os"
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
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// evidenceRunner is a successful agent that optionally records evidence the
// way the "Evidence (required)" prompt block asks.
type evidenceRunner struct {
	evidence string // JSON written to .itervox/evidence/implementer.json; "" writes nothing
	// gitCommit makes the agent commit a code change first and replace
	// "HEAD" in evidence with the resulting commit, as the prompt asks.
	gitCommit bool
	// dirtyAfter leaves a further, uncommitted change to main.go after the
	// commit the evidence is stamped with (needs gitCommit).
	dirtyAfter bool
	mu         sync.Mutex
	prompts   []string
}

func (r *evidenceRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, workspacePath, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	evidence := r.evidence
	if r.gitCommit {
		git := func(args ...string) (string, error) {
			out, err := gitexec.Command(context.Background(), workspacePath, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		if err := os.WriteFile(filepath.Join(workspacePath, "main.go"), []byte("package main\n"), 0o644); err != nil {
			return agent.TurnResult{}, err
		}
		if _, err := git("add", "main.go"); err != nil {
			return agent.TurnResult{}, err
		}
		if out, err := git("commit", "-q", "-m", "implement"); err != nil {
			return agent.TurnResult{}, fmt.Errorf("commit: %v: %s", err, out)
		}
		head, err := git("rev-parse", "HEAD")
		if err != nil {
			return agent.TurnResult{}, err
		}
		evidence = strings.ReplaceAll(evidence, `"HEAD"`, `"`+head+`"`)
		if r.dirtyAfter {
			if err := os.WriteFile(filepath.Join(workspacePath, "main.go"), []byte("package main // untested\n"), 0o644); err != nil {
				return agent.TurnResult{}, err
			}
		}
	}
	if evidence != "" {
		dir := filepath.Join(workspacePath, ".itervox", "evidence")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.TurnResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "implementer.json"), []byte(evidence), 0o644); err != nil {
			return agent.TurnResult{}, err
		}
	}
	return agent.TurnResult{SessionID: "s-ev", InputTokens: 10, OutputTokens: 5, ResultText: "done"}, nil
}

// runEvidenceWorker dispatches ENG-1 once under the implementer profile and
// returns the orchestrator, the tracker and the runner after the run ended.
func runEvidenceWorker(t *testing.T, required []string, evidence string) (*orchestrator.Orchestrator, *tracker.MemoryTracker, *evidenceRunner) {
	t.Helper()
	return runEvidenceWorkerIn(t, t.TempDir(), &evidenceRunner{evidence: evidence}, required)
}

func runEvidenceWorkerIn(t *testing.T, wsPath string, runner *evidenceRunner, required []string) (*orchestrator.Orchestrator, *tracker.MemoryTracker, *evidenceRunner) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Instructions: "Implement it.", RequireEvidence: required},
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsPath})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool {
		if _, held := orch.Snapshot().InputRequiredIssues["ENG-1"]; held {
			return true
		}
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 6*time.Second, 20*time.Millisecond, "the run must either move the issue or hold it")
	return orch, mt, runner
}

func issueState(t *testing.T, mt *tracker.MemoryTracker) string {
	t.Helper()
	issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
	require.NoError(t, err)
	return issues[0].State
}

// TestEvidenceGateHoldsRunWithoutEvidence (#80): with require_evidence on, a
// successful run that recorded no evidence does not move the issue; it is
// held input-required with a "Needs evidence" reason.
func TestEvidenceGateHoldsRunWithoutEvidence(t *testing.T) {
	orch, mt, runner := runEvidenceWorker(t, []string{"test", "lint"}, "")
	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry, "the run must be held for input")
	assert.Contains(t, entry.Context, `Needs evidence before moving to "Done"`)
	assert.Contains(t, entry.Context, "`test`: no evidence file at `.itervox/evidence/implementer.json`")
	assert.Contains(t, entry.Context, "`lint`")
	assert.Equal(t, "In Progress", issueState(t, mt), "the issue is not moved")
	require.NotEmpty(t, runner.prompts)
	assert.Contains(t, runner.prompts[0], "## Evidence (required)")
	assert.Contains(t, runner.prompts[0], "- required checks: `test`, `lint`")
}

// TestEvidenceGateMovesRunWithPassingEvidence (#80): with passing evidence
// for every required check the issue moves to completion_state as before.
func TestEvidenceGateMovesRunWithPassingEvidence(t *testing.T) {
	ev := `{"commit":"","checks":[` +
		`{"name":"test","command":"go test ./...","output":"ok  pkg 0.1s","passed":true},` +
		`{"name":"lint","command":"golangci-lint run","output":"0 issues.","passed":true}]}`
	orch, mt, _ := runEvidenceWorker(t, []string{"test", "lint"}, ev)
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
}

// TestEvidenceGateMovesRunWithPassingEvidenceInGitWorkspace (#80): in a git
// worktree the agent stamps its evidence with its last commit; the handoff
// commit the worker makes afterwards must not invalidate it.
func TestEvidenceGateMovesRunWithPassingEvidenceInGitWorkspace(t *testing.T) {
	ws := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	ev := `{"commit":"HEAD","checks":[{"name":"test","command":"go test ./...","output":"ok  pkg 0.1s","passed":true}]}`
	orch, mt, _ := runEvidenceWorkerIn(t, ws, &evidenceRunner{evidence: ev, gitCommit: true}, []string{"test"})
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
	log, err := gitexec.Command(context.Background(), ws, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Contains(t, string(log), "chore(itervox): record agent handoff",
		"the worker's handoff commit landed after the evidence stamp")
}

// TestEvidenceGateHoldsRunWithUncommittedChanges (#80 review): evidence
// stamped on the agent's commit does not move the issue while a tracked file
// still has an uncommitted change on top of that commit.
func TestEvidenceGateHoldsRunWithUncommittedChanges(t *testing.T) {
	ws := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	ev := `{"commit":"HEAD","checks":[{"name":"test","command":"go test ./...","output":"ok  pkg 0.1s","passed":true}]}`
	orch, mt, _ := runEvidenceWorkerIn(t, ws, &evidenceRunner{evidence: ev, gitCommit: true, dirtyAfter: true}, []string{"test"})
	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry, "the run must be held for input")
	assert.Contains(t, entry.Context, "`main.go` has uncommitted changes")
	assert.Equal(t, "In Progress", issueState(t, mt))
}

// TestEvidenceGateRejectsFailingEvidence: an entry that did not pass does
// not count.
func TestEvidenceGateRejectsFailingEvidence(t *testing.T) {
	ev := `{"checks":[{"name":"test","command":"go test ./...","output":"FAIL pkg","passed":false}]}`
	orch, mt, _ := runEvidenceWorker(t, []string{"test"}, ev)
	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.Contains(t, entry.Context, "`test`: no passing entry")
	assert.Equal(t, "In Progress", issueState(t, mt))
}

// TestEvidenceGateOffByDefault (#80): without require_evidence nothing
// changes — no prompt block, and the issue moves without any evidence.
func TestEvidenceGateOffByDefault(t *testing.T) {
	orch, mt, runner := runEvidenceWorker(t, nil, "")
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
	require.NotEmpty(t, runner.prompts)
	assert.False(t, strings.Contains(runner.prompts[0], "Evidence (required)"))
}
