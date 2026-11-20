package orchestrator_test

import (
	"context"
	"slices"
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
	"github.com/vnovick/itervox/internal/tracker"
)

// modelServerTurn is what one fake turn saw: no real agent CLI runs and no
// model server is contacted.
type modelServerTurn struct {
	command       string
	set, vars     []string
	readTimeoutMs int
	turnTimeoutMs int
}

type modelServerRunner struct {
	mu    sync.Mutex
	turns map[string]modelServerTurn // by the issue identifier in the prompt
}

func (r *modelServerRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, command, _, _ string, readTimeoutMs, turnTimeoutMs int, _ agent.PermissionMode) (agent.TurnResult, error) {
	env := agent.RunEnvFrom(ctx)
	r.mu.Lock()
	id := ""
	for _, candidate := range []string{"M-1", "M-2", "M-3"} {
		if strings.Contains(prompt, candidate) {
			id = candidate
		}
	}
	r.turns[id] = modelServerTurn{
		command: command, set: slices.Clone(env.Set), vars: slices.Clone(env.Vars),
		readTimeoutMs: readTimeoutMs, turnTimeoutMs: turnTimeoutMs,
	}
	r.mu.Unlock()
	return agent.TurnResult{SessionID: "s", ResultText: "ok"}, nil
}

func (r *modelServerRunner) get(id string) (modelServerTurn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.turns[id]
	return t, ok
}

// TestModelServerReachesTheAgentForItsBackendOnly: a claude profile's
// model_server reaches its agent as ANTHROPIC_BASE_URL, a codex profile's
// as OPENAI_BASE_URL, nothing else leaks into either, a run without a
// model server gets none, and the model-server profiles run with the
// longer timeouts while the plain run keeps the agent-level ones.
func TestModelServerReachesTheAgentForItsBackendOnly(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Command = "claude"
	cfg.Agent.ReadTimeoutMs = 30_000
	cfg.Agent.TurnTimeoutMs = 3_600_000
	cfg.Agent.StallTimeoutMs = 300_000
	cfg.PromptTemplate = "{{ issue.identifier }}"
	profiles := map[string]config.AgentProfile{
		"local":       {Command: "claude", ModelServer: "http://127.0.0.1:8000"},
		"codex-local": {Command: "codex", ModelServer: "http://127.0.0.1:8000/v1"},
	}
	for name, p := range profiles {
		require.NoError(t, config.ApplyProfileModelServer(name, &p, cfg.Agent.Command))
		profiles[name] = p
	}
	cfg.Agent.Profiles = profiles
	mt := tracker.NewMemoryTracker([]domain.Issue{
		makeIssue("i1", "M-1", "Todo", nil, nil),
		makeIssue("i2", "M-2", "Todo", nil, nil),
		makeIssue("i3", "M-3", "Todo", nil, nil),
	}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &modelServerRunner{turns: map[string]modelServerTurn{}}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetIssueProfile("M-1", "local")
	orch.SetIssueProfile("M-2", "codex-local")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		_, a := runner.get("M-1")
		_, b := runner.get("M-2")
		_, c := runner.get("M-3")
		return a && b && c
	}, 8*time.Second, 20*time.Millisecond)

	claudeRun, _ := runner.get("M-1")
	assert.Equal(t, []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:8000"}, claudeRun.set)
	assert.Equal(t, "claude", claudeRun.command, "the command is not rewritten for a local run")
	assert.Equal(t, config.ModelServerReadTimeoutMs, claudeRun.readTimeoutMs)
	assert.Equal(t, config.ModelServerTurnTimeoutMs, claudeRun.turnTimeoutMs)

	codexRun, _ := runner.get("M-2")
	assert.Equal(t, []string{"OPENAI_BASE_URL=http://127.0.0.1:8000/v1"}, codexRun.set)

	plainRun, _ := runner.get("M-3")
	assert.Empty(t, plainRun.set, "a run without a model server gets no variable")
	assert.Equal(t, 30_000, plainRun.readTimeoutMs)
	assert.Equal(t, 3_600_000, plainRun.turnTimeoutMs)

	for _, run := range []modelServerTurn{claudeRun, codexRun, plainRun} {
		for _, kv := range slices.Concat(run.vars, run.set) {
			name, _, _ := strings.Cut(kv, "=")
			if slices.Contains(config.ModelServerEnvVars, name) {
				assert.Contains(t, run.set, kv, "a model-server variable only through Set")
			}
		}
	}
	assert.NotContains(t, strings.Join(claudeRun.set, " "), "OPENAI", "the claude run gets no codex variable")
	assert.NotContains(t, strings.Join(codexRun.set, " "), "ANTHROPIC", "the codex run gets no claude variable")
}

// TestReconcileStallsUsesTheProfileStallTimeout: a model-server
// profile's longer stall timeout keeps its silent run, the same silence
// stops a plain run, and a profile's timeout applies even when the agent
// level turned stall detection off.
func TestReconcileStallsUsesTheProfileStallTimeout(t *testing.T) {
	silentFor := time.Now().Add(-10 * time.Minute)
	withProfile := func(id, profile string) *orchestrator.RunEntry {
		e := runningEntry(id, "In Progress", &silentFor)
		e.Issue.Identifier = "ENG-" + id
		e.ProfileName = profile
		return e
	}

	cfg := cfgWithStall(300_000) // 5 min
	state := orchestrator.NewState(cfg)
	state.Running["local"] = withProfile("local", "local")
	state.Running["plain"] = withProfile("plain", "")
	events := make(chan orchestrator.OrchestratorEvent, 10)
	state = orchestrator.ReconcileStalls(state, cfg, map[string]int{"local": config.ModelServerStallTimeoutMs}, time.Now(), events, nil)
	assert.Contains(t, state.Running, "local", "15 min allowed: 10 min of silence is not a stall")
	assert.NotContains(t, state.Running, "plain", "5 min allowed: stalled")

	off := cfgWithStall(0)
	state = orchestrator.NewState(off)
	state.Running["slow"] = withProfile("slow", "slow")
	state.Running["plain"] = withProfile("plain", "")
	state = orchestrator.ReconcileStalls(state, off, map[string]int{"slow": 60_000}, time.Now(), events, nil)
	assert.NotContains(t, state.Running, "slow", "a profile's own stall_timeout_ms applies")
	assert.Contains(t, state.Running, "plain", "agent-level stall detection stays off for the rest")
}
