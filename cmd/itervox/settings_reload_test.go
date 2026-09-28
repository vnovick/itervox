package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
	"github.com/vnovick/itervox/internal/workspace"
)

// turnBlockingRunner holds every RunTurn open until its ctx is cancelled,
// i.e. it models an agent turn that is still in flight. It records whether
// the turn's context was ever cancelled.
type turnBlockingRunner struct {
	started   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	cancelled bool
}

func (r *turnBlockingRunner) RunTurn(
	ctx context.Context, _ agent.Logger, _ func(agent.TurnResult),
	_ *string, _, _, _, _, _ string, _, _ int,
	_ agent.PermissionMode,
) (agent.TurnResult, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	return agent.TurnResult{}, ctx.Err()
}

func (r *turnBlockingRunner) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

type tempWorkspaceProvider struct{ root string }

func (p tempWorkspaceProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	path := filepath.Join(p.root, identifier)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: path, Identifier: identifier}, nil
}

func (p tempWorkspaceProvider) RemoveWorkspace(context.Context, string, string) error { return nil }

func (p tempWorkspaceProvider) ResolvePath(identifier string) string {
	return filepath.Join(p.root, identifier)
}

// TestSettingsSaveDoesNotCancelRunContext (CORE-116) wires the daemon's
// reload chain the way main() does — workflow.WatchFrom(runCtx, path,
// cfg.WorkflowHash, runCancel)
// alongside orch.Run(runCtx) — with a real config.Load'ed WORKFLOW.md, a
// real Orchestrator and the real orchestratorAdapter, and an agent turn held
// in flight by a fake runner. A dashboard settings save (SetWorkers →
// PatchIntField) must leave runCtx alive and the turn running after the
// production poll + debounce + 2 s, with the value live through the cfgMu
// getter. The positive control then makes an operator edit and asserts the
// same wiring DOES reload, so the negative result is not a dead watcher.
func TestSettingsSaveDoesNotCancelRunContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
polling:
  interval_ms: 50
agent:
  command: claude
  max_concurrent_agents: 2
workspace:
  root: ` + filepath.Join(dir, "workspaces") + `
server:
  port: 0
---

You are working on {{ issue.identifier }}.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &turnBlockingRunner{started: make(chan struct{})}
	orch := orchestrator.New(cfg, mt, runner, tempWorkspaceProvider{root: filepath.Join(dir, "workspaces")})

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		_ = workflow.WatchFrom(runCtx, path, cfg.WorkflowHash, runCancel)
	}()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = orch.Run(runCtx)
	}()
	defer func() {
		runCancel()
		<-watchDone
		<-runDone
	}()

	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent turn never started")
	}

	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: path, notify: func() {}}
	require.NoError(t, adapter.SetWorkers(4))

	// Production intervals: 1 s poll + 2 s debounce, plus 2 s margin.
	time.Sleep(5 * time.Second)
	require.NoError(t, runCtx.Err(),
		"a dashboard settings save cancelled the run context — every in-flight agent turn is killed")
	require.False(t, runner.wasCancelled(), "the in-flight agent turn was cancelled by a settings save")
	require.Equal(t, 4, orch.MaxWorkers(), "the saved value must be live through the cfgMu getter")
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(saved), "max_concurrent_agents: 4")

	// Positive control: an operator edit through the same wiring reloads.
	require.NoError(t, os.WriteFile(path,
		[]byte(strings.Replace(string(saved), "max_concurrent_agents: 4", "max_concurrent_agents: 6", 1)), 0o644))
	require.Eventually(t, func() bool { return runCtx.Err() != nil },
		8*time.Second, 50*time.Millisecond, "an operator edit must still reload")
}
