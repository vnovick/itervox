package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// portProvider hands out a workspace with a port, the way workspace.Manager
// does (#250), and answers WorkspacePort for the run-context endpoint.
type portProvider struct {
	dir  string
	port int
}

func (p *portProvider) EnsureWorkspace(_ context.Context, id, _ string) (workspace.Workspace, error) {
	return workspace.Workspace{Path: p.dir, Identifier: id, CreatedNow: true, Port: p.port}, nil
}
func (p *portProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *portProvider) ResolvePath(string) string                             { return p.dir }
func (p *portProvider) WorkspacePort(string) int                              { return p.port }

// portEnvRunner records each turn's prompt and the run environment the real
// runners apply.
type portEnvRunner struct {
	mu      sync.Mutex
	prompts []string
	envs    [][]string
}

func (r *portEnvRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.envs = append(r.envs, slices.Clone(agent.RunEnvFrom(ctx).Vars))
	r.mu.Unlock()
	return agent.TurnResult{SessionID: "s", ResultText: "ok"}, nil
}

func (r *portEnvRunner) first() (string, []string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.prompts) == 0 {
		return "", nil, false
	}
	return r.prompts[0], r.envs[0], true
}

// TestWorkspacePortReachesAgentHooksAndPrompt (#250): the workspace's port
// is ITERVOX_PORT in the local agent's environment and in every hook's, and
// `{{ run.port }}` renders it in the prompt.
func TestWorkspacePortReachesAgentHooksAndPrompt(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "hook-port.txt")
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Hooks.AfterCreate = `printf 'after_create=%s\n' "$ITERVOX_PORT" >> '` + out + `'`
	cfg.Hooks.BeforeRun = `printf 'before_run=%s\n' "$ITERVOX_PORT" >> '` + out + `'`
	cfg.PromptTemplate = "Serve the app on port {{ run.port }}."
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("idp1", "P-1", "Todo", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &portEnvRunner{}
	orch := orchestrator.New(cfg, mt, runner, &portProvider{dir: dir, port: 41007})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	var prompt string
	var env []string
	require.Eventually(t, func() bool {
		var ok bool
		prompt, env, ok = runner.first()
		return ok
	}, 6*time.Second, 20*time.Millisecond)
	assert.Contains(t, prompt, "Serve the app on port 41007.")
	assert.Contains(t, env, "ITERVOX_PORT=41007")
	hooks, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "after_create=41007\nbefore_run=41007\n", string(hooks))
	assert.Equal(t, 41007, orch.WorkspacePort("P-1"))
}
