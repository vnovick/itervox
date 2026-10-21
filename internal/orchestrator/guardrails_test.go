package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// shellRunner runs the agent command line through /bin/sh in the
// workspace with the run environment the orchestrator put in the turn's
// context (the guardrail environment), as the claude and codex runners do.
type shellRunner struct{}

func (shellRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, workspacePath, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = workspacePath
	env := agent.RunEnvFrom(ctx) // what the real runners apply (agent.TestRunnersApplyRunEnv)
	cmd.Env = append(os.Environ(), env.Vars...)
	if env.PathPrepend != "" {
		cmd.Env = append(cmd.Env, "PATH="+env.PathPrepend+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return agent.TurnResult{Failed: true, FailureText: string(out)}, err
	}
	return agent.TurnResult{ResultText: "done", AllTextBlocks: []string{"done"}}, nil
}

// deployRig is a real orchestrator with a real server for the guardrail
// endpoints, a fake agent that runs `vercel --prod` once, and a fake
// vercel that counts its runs.
type deployRig struct {
	t     *testing.T
	orch  *orchestrator.Orchestrator
	mt    *tracker.MemoryTracker
	out   string
	count string
}

func newDeployRig(t *testing.T) *deployRig {
	t.Helper()
	root := t.TempDir()
	r := &deployRig{t: t, out: filepath.Join(root, "agent.out"), count: filepath.Join(root, "vercel.count")}
	fakes := filepath.Join(root, "fakebin")
	require.NoError(t, os.MkdirAll(fakes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fakes, "vercel"), []byte("#!/bin/sh\necho \"$*\" >> "+r.count+"\n"), 0o755))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(root, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte("vercel --prod > "+r.out+" 2>&1\necho \"exit=$?\" >> "+r.out+"\n"), 0o755))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Command = "/bin/sh " + script
	cfg.Agent.TurnTimeoutMs = 60_000
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.ApprovalCommands = []string{"vercel --prod"}
	r.mt = tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, r.mt, shellRunner{}, &recordingWorkspaceProvider{path: filepath.Join(root, "ws")})

	store := agentactions.NewStore()
	srv := httptest.NewServer(server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Guardrails:       r.orch,
	}))
	t.Cleanup(srv.Close)
	r.orch.SetAgentActionBaseURL(srv.URL)
	r.orch.SetAgentActionTokens(store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// waitForApproval waits for the guardrail input request and returns it.
func (r *deployRig) waitForApproval() *orchestrator.InputRequiredEntry {
	r.t.Helper()
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(r.t, func() bool {
		entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 15*time.Second, 20*time.Millisecond, "the deploy command never raised an input request")
	return entry
}

func (r *deployRig) agentOutput() string {
	r.t.Helper()
	var out string
	require.Eventually(r.t, func() bool {
		data, _ := os.ReadFile(r.out)
		out = string(data)
		return strings.Contains(out, "exit=")
	}, 15*time.Second, 20*time.Millisecond, "the agent's deploy command never finished")
	return out
}

func (r *deployRig) runs() []string {
	data, err := os.ReadFile(r.count)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

// #204: a matching deploy command from an agent blocks and raises an input
// request ("Agent wants to run: …") on the running issue, also posted to the
// tracker; the agent cannot answer it itself; a human approval runs it
// exactly once and the agent gets its result.
func TestDeployCommandRaisesInputRequestAndRunsOnceOnApprove(t *testing.T) {
	r := newDeployRig(t)
	entry := r.waitForApproval()
	assert.Contains(t, entry.Context, "Agent wants to run: `vercel --prod`")
	snap := r.orch.Snapshot()
	require.Len(t, snap.Running, 1, "the run keeps going while it waits")
	assert.Empty(t, r.runs(), "nothing runs before the approval")
	require.Eventually(t, func() bool {
		issue, err := r.mt.FetchIssueDetail(context.Background(), "id1")
		if err != nil {
			return false
		}
		for _, c := range issue.Comments {
			if strings.Contains(c.Body, "Agent wants to run: `vercel --prod`") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the question reaches the tracker")

	// The agent-actions provide-input path never approves.
	require.NoError(t, r.orch.ProvideAgentInput("ENG-1", "approve"))
	time.Sleep(300 * time.Millisecond)
	assert.NotNil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"], "an agent cannot approve its own command")
	assert.Empty(t, r.runs())

	require.NoError(t, r.orch.ProvideInput("ENG-1", "approve"))
	out := r.agentOutput()
	assert.Contains(t, out, "exit=0", out)
	assert.Contains(t, out, "approved; running `vercel --prod` once")
	assert.Equal(t, []string{"--prod"}, r.runs(), "the approved command ran exactly once")
	require.Eventually(t, func() bool {
		return r.orch.Snapshot().InputRequiredIssues["ENG-1"] == nil
	}, 5*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, []string{"--prod"}, r.runs(), "and never again")
}

// A rejection (here, a tracker reply) refuses the command: the agent gets
// the reply as the reason, nothing runs, and the refusal is counted and
// recorded in RecentFailures.
func TestDeployCommandRejectedByTrackerReply(t *testing.T) {
	before := metrics.GuardrailBlockCount(guardrails.KindApprovalRejected)
	r := newDeployRig(t)
	r.waitForApproval()
	r.mt.AddHumanComment("id1", "no, deploy to staging first")
	out := r.agentOutput()
	assert.Contains(t, out, "exit=1", out)
	assert.Contains(t, out, "the operator rejected `vercel --prod`: no, deploy to staging first")
	assert.Empty(t, r.runs())
	assert.Equal(t, before+1, metrics.GuardrailBlockCount(guardrails.KindApprovalRejected))
	require.Eventually(t, func() bool {
		for _, f := range r.orch.Snapshot().RecentFailures {
			if f.Kind == orchestrator.FailureKindGuardrail && f.Source == guardrails.KindApprovalRejected && f.Identifier == "ENG-1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}
