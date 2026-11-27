package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// gatedRunner holds each turn until its issue's gate is released.
type gatedRunner struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func newGatedRunner() *gatedRunner { return &gatedRunner{gates: map[string]chan struct{}{}} }

func (g *gatedRunner) gate(ws string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates[ws] == nil {
		g.gates[ws] = make(chan struct{})
	}
	return g.gates[ws]
}

func (g *gatedRunner) release(ws string) { close(g.gate(ws)) }

func (g *gatedRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, ws, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	select {
	case <-g.gate(filepath.Base(ws)):
	case <-ctx.Done():
		return agent.TurnResult{Failed: true}, ctx.Err()
	}
	return agent.TurnResult{ResultText: "done"}, nil
}

// issueDirWorkspaces gives each issue its own directory, named after it.
type issueDirWorkspaces struct{ root string }

func (p *issueDirWorkspaces) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	dir := filepath.Join(p.root, identifier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: dir, Identifier: identifier}, nil
}

func (p *issueDirWorkspaces) RemoveWorkspace(context.Context, string, string) error { return nil }

func (p *issueDirWorkspaces) ResolvePath(identifier string) string {
	return filepath.Join(p.root, identifier)
}

type policyRig struct {
	orch   *orchestrator.Orchestrator
	mt     *tracker.MemoryTracker
	runner *gatedRunner
	mu     sync.Mutex
	alerts []orchestrator.PolicyChangeAlert
}

func newPolicyRig(t *testing.T, p policyProject, issues ...domain.Issue) *policyRig {
	t.Helper()
	cfg := baseConfig()
	cfg.WorkflowPath = p.workflow
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Playbooks.Enabled = true
	r := &policyRig{runner: newGatedRunner()}
	r.mt = tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, r.mt, r.runner, &issueDirWorkspaces{root: t.TempDir()})
	r.orch.SetPolicyChangeHook(func(a orchestrator.PolicyChangeAlert) {
		r.mu.Lock()
		r.alerts = append(r.alerts, a)
		r.mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func (r *policyRig) running(t *testing.T, identifier string) string {
	t.Helper()
	var id string
	require.Eventually(t, func() bool {
		for _, e := range r.orch.Snapshot().Running {
			if e != nil && e.Issue.Identifier == identifier && e.RunID != "" {
				id = e.RunID
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "%s runs", identifier)
	return id
}

func (r *policyRig) history(t *testing.T, runID string) orchestrator.CompletedRun {
	t.Helper()
	var got orchestrator.CompletedRun
	require.Eventually(t, func() bool {
		for _, h := range r.orch.RunHistory() {
			if h.RunID == runID {
				got = h
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "run %s is in history", runID)
	return got
}

func (r *policyRig) policyEvents(source string) []orchestrator.FeedEvent {
	var out []orchestrator.FeedEvent
	for _, ev := range r.orch.Snapshot().Feed {
		if ev.Kind == orchestrator.FeedPolicyChanged && ev.Payload.Source == source {
			out = append(out, ev)
		}
	}
	return out
}

// TestRunPolicyFingerprintFlagsMidRunChange: a run records the policy that
// governs it at start; when a governing file changes before it exits, its
// history row names the changed files (names only), a policy_changed feed
// event names the run (by run ID) and says a running unsandboxed agent
// could have made the change. A run during which nothing changed records
// nothing.
func TestRunPolicyFingerprintFlagsMidRunChange(t *testing.T) {
	p := newPolicyProject(t)
	r := newPolicyRig(t, p, makeIssue("id1", "ENG-1", "In Progress", nil, nil), makeIssue("id2", "ENG-2", "Backlog", nil, nil))

	run1 := r.running(t, "ENG-1")
	require.NoError(t, os.WriteFile(filepath.Join(p.dir, ".itervox", "rules.json"), []byte(`{"rules": []}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(p.dir, ".itervox", "playbooks", "new.md"), []byte("# New\n"), 0o644))
	r.runner.release("ENG-1")
	h := r.history(t, run1)
	assert.Equal(t, []string{".itervox/playbooks/new.md", ".itervox/rules.json"}, h.PolicyChanged)
	require.Eventually(t, func() bool { return len(r.policyEvents(orchestrator.PolicySourceRunExit)) == 1 }, 5*time.Second, 10*time.Millisecond)
	ev := r.policyEvents(orchestrator.PolicySourceRunExit)[0]
	assert.Equal(t, "ENG-1", ev.Identifier)
	assert.Equal(t, []string{run1}, ev.Payload.RunIDs)
	assert.Equal(t, h.PolicyChanged, ev.Payload.Files)
	assert.True(t, ev.Payload.PossiblyAgent, "an unsandboxed local agent could write the project directory")

	r.mt.SetIssueState("id2", "Todo")
	run2 := r.running(t, "ENG-2")
	r.runner.release("ENG-2")
	assert.Empty(t, r.history(t, run2).PolicyChanged, "nothing changed during this run")
}

// TestPolicyChangeWhileRunsActiveIsRaised: a governing file that changes
// while a run is active is raised loudly — at the next dispatch (which
// re-reads the files) and when the config watcher reports it — naming the
// files and the active run IDs, and telling the policy change hook (the
// dashboard banner). With no run active the watcher's report raises
// nothing.
func TestPolicyChangeWhileRunsActiveIsRaised(t *testing.T) {
	p := newPolicyProject(t)
	r := newPolicyRig(t, p, makeIssue("id1", "ENG-1", "In Progress", nil, nil), makeIssue("id2", "ENG-2", "Backlog", nil, nil))
	run1 := r.running(t, "ENG-1")

	// The config watcher saw WORKFLOW.md change.
	require.True(t, r.orch.NotePolicyFilesChanged([]string{p.workflow}))
	require.Eventually(t, func() bool { return len(r.policyEvents(orchestrator.PolicySourceWatcher)) == 1 }, 5*time.Second, 10*time.Millisecond)
	w := r.policyEvents(orchestrator.PolicySourceWatcher)[0]
	assert.Equal(t, []string{"WORKFLOW.md"}, w.Payload.Files)
	assert.Equal(t, []string{run1}, w.Payload.RunIDs)
	assert.True(t, w.Payload.PossiblyAgent)

	// A dispatch reads the files again and sees the agent profile changed.
	require.NoError(t, os.WriteFile(filepath.Join(p.dir, ".itervox", "agents", "coder", "SOUL.md"), []byte("You are fast.\n"), 0o644))
	r.mt.SetIssueState("id2", "Todo")
	r.running(t, "ENG-2")
	require.Eventually(t, func() bool { return len(r.policyEvents(orchestrator.PolicySourceDispatch)) == 1 }, 5*time.Second, 10*time.Millisecond)
	d := r.policyEvents(orchestrator.PolicySourceDispatch)[0]
	assert.Equal(t, []string{".itervox/agents/coder/SOUL.md"}, d.Payload.Files)
	assert.Equal(t, []string{run1}, d.Payload.RunIDs, "the runs that were active, not the one being dispatched")

	r.mu.Lock()
	alerts := append([]orchestrator.PolicyChangeAlert(nil), r.alerts...)
	r.mu.Unlock()
	require.Len(t, alerts, 2, "the banner hook saw both")
	assert.Equal(t, orchestrator.PolicySourceWatcher, alerts[0].Source)
	assert.Equal(t, orchestrator.PolicySourceDispatch, alerts[1].Source)

	r.runner.release("ENG-1")
	r.runner.release("ENG-2")
	require.Eventually(t, func() bool { return len(r.orch.Snapshot().Running) == 0 }, 10*time.Second, 10*time.Millisecond)
	r.orch.NotePolicyFilesChanged([]string{p.workflow})
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, r.policyEvents(orchestrator.PolicySourceWatcher), 1, "no active run: nothing to raise")
}
