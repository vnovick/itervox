package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// lanesFixture is an orchestrator whose ENG-1 worktree changes a workflow
// (with a continue-on-error line), an auth file, ordinary code, and — not
// yet committed — a test that gains a t.Skip.
func lanesFixture(t *testing.T) *Orchestrator {
	t.Helper()
	root := t.TempDir()
	cfg := automationBaseCfg()
	cfg.Workspace.Root = root
	cfg.Workspace.BaseBranch = "main"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.Lanes.Human = []string{".github/**", "Makefile"}
	cfg.Guardrails.Lanes.SecondOpinion = []string{"auth/**"}
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, workspace.NewManager(cfg))
	dir := filepath.Join(root, "ENG-1")
	write := func(name, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	write("main.go", "package main\n")
	write("calc_test.go", "package main\n\nimport \"testing\"\n\nfunc TestCalc(t *testing.T) {\n}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "itervox/eng-1")
	write(".github/workflows/ci.yml", "jobs:\n  test:\n    continue-on-error: true\n")
	write("auth/login.go", "package auth\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "work")
	write("calc_test.go", "package main\n\nimport \"testing\"\n\nfunc TestCalc(t *testing.T) {\n\tt.Skip(\"flaky\")\n}\n")
	return o
}

// TestWorktreeLanesGroupsFilesWithRulesFindingsAndSplit: the Lanes view
// puts every changed file in its lane with the pattern that put it there,
// hangs the audit findings (with line) on their file — the uncommitted
// working tree included — says why the change needs a person, and carries
// the issue's stack split, recorded lane and pending approval from the
// published snapshot.
func TestWorktreeLanesGroupsFilesWithRulesFindingsAndSplit(t *testing.T) {
	o := lanesFixture(t)
	o.snapMu.Lock()
	o.lastSnap.StackSplits = map[string]StackSplitRecord{"ENG-1": {RunID: "r1", Order: StackSplitCodeFirst,
		Reason: "the code passed its checks alone", CodeFiles: []string{"main.go"}, CodeFilesTotal: 1,
		PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1, LowerPR: 7}}
	o.lastSnap.PushLanes = map[string]PushLaneRecord{"ENG-1": {Lane: guardrails.LaneHuman}}
	o.lastSnap.InputRequiredIssues = map[string]*InputRequiredEntry{"ENG-1": {Identifier: "ENG-1",
		GuardrailApprovalID: "a1", GuardrailLanePaths: []string{".github/workflows/ci.yml"}}}
	o.snapMu.Unlock()

	v, err := o.WorktreeLanes(context.Background(), "ENG-1")
	require.NoError(t, err)
	assert.True(t, v.Enforced)
	assert.Equal(t, guardrails.LaneHuman, v.Lane)
	byPath := map[string]LaneFile{}
	for _, f := range v.Files {
		byPath[f.Path] = f
	}
	require.Len(t, byPath, 4)
	assert.Equal(t, [2]string{guardrails.LaneHuman, ".github/**"}, [2]string{byPath[".github/workflows/ci.yml"].Lane, byPath[".github/workflows/ci.yml"].Rule})
	assert.Equal(t, [2]string{guardrails.LaneSecondOpinion, "auth/**"}, [2]string{byPath["auth/login.go"].Lane, byPath["auth/login.go"].Rule})
	assert.Equal(t, [2]string{guardrails.LaneAuto, ""}, [2]string{byPath["main.go"].Lane, byPath["main.go"].Rule})

	ci := byPath[".github/workflows/ci.yml"].Findings
	require.Len(t, ci, 1)
	assert.Equal(t, ".github/workflows/ci.yml:3", ci[0].Location())
	assert.Equal(t, "ci_continue_on_error", ci[0].Rule)
	assert.Equal(t, "continue-on-error: true", ci[0].Text)
	skip := byPath["calc_test.go"].Findings
	require.Len(t, skip, 1, "the uncommitted working tree is audited too")
	assert.Equal(t, "calc_test.go:6", skip[0].Location())
	assert.Empty(t, byPath["main.go"].Findings)

	lines := guardrails.LaneReasonLines(v.Reasons, v.ReasonsTotal, 10)
	assert.Equal(t, []string{
		".github/workflows/ci.yml:3 adds `continue-on-error: true` (failure ignored in a CI workflow (continue-on-error / allow_failure: true))",
		"calc_test.go:6 adds `t.Skip(\"flaky\")` (test skip added (t.Skip))",
		".github/workflows/ci.yml matched .github/** → human",
		"auth/login.go matched auth/** → second_opinion",
	}, lines)

	require.NotNil(t, v.Split)
	assert.Equal(t, StackSplitCodeFirst, v.Split.Order)
	assert.Equal(t, 7, v.Split.LowerPR)
	require.NotNil(t, v.PushLane)
	assert.Equal(t, guardrails.LaneHuman, v.PushLane.Lane)
	assert.True(t, v.ApprovalPending)

	_, err = o.WorktreeLanes(context.Background(), "ENG-404")
	assert.ErrorIs(t, err, ErrNoWorktree)
}

// TestWorktreeLanesRunsOffEventLoop: the Lanes view runs git in the
// caller's goroutine and reads State only from the published snapshot —
// with no loop running and the event channel full it still answers and
// sends nothing.
func TestWorktreeLanesRunsOffEventLoop(t *testing.T) {
	o := lanesFixture(t)
	for len(o.events) < cap(o.events) {
		o.events <- OrchestratorEvent{Type: EventWorkerUpdate}
	}
	done := make(chan error, 1)
	go func() {
		v, err := o.WorktreeLanes(context.Background(), "ENG-1")
		if err == nil && len(v.Files) == 0 {
			err = assert.AnError
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the lanes view waited on the event loop")
	}
	assert.Equal(t, cap(o.events), len(o.events), "the lanes view sent nothing to the event loop")
	for len(o.events) > 0 {
		<-o.events
	}
}
