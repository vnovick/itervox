package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// holdRig runs the real claude runner (its stream reader and idle read
// deadline) against a fake claude CLI at an absolute path in a temp dir —
// it can never fall through to an installed claude — with guardrails on, a
// real server for the guardrail endpoints and `vercel --prod` held for
// approval. The fake prints stream-json, runs body, then prints a result.
type holdRig struct {
	t    *testing.T
	orch *orchestrator.Orchestrator
	mt   *tracker.MemoryTracker
	out  string
}

// Longer than a shim's approval poll (1s), so the agent can act on the
// answer within it once the stall clock restarts.
const holdStallTimeoutMs = 1500

func newHoldRig(t *testing.T, body string) *holdRig {
	t.Helper()
	root := t.TempDir()
	r := &holdRig{t: t, out: filepath.Join(root, "agent.out")}
	fakes := filepath.Join(root, "fakebin")
	require.NoError(t, os.MkdirAll(fakes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fakes, "vercel"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	claude := filepath.Join(root, "cli", "claude")
	require.NoError(t, os.MkdirAll(filepath.Dir(claude), 0o755))
	script := "#!/bin/sh\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-hold"}'` + "\n" +
		strings.ReplaceAll(body, "$OUT", r.out) + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-hold","result":"deployed"}'` + "\n"
	require.NoError(t, os.WriteFile(claude, []byte(script), 0o755))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 60_000 // a retry stays pending for the assertions
	cfg.Agent.Command = claude
	cfg.Agent.ReadTimeoutMs = 200
	cfg.Agent.StallTimeoutMs = holdStallTimeoutMs
	cfg.Agent.TurnTimeoutMs = 60_000
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.ApprovalCommands = []string{"vercel --prod"}
	r.mt = tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, r.mt, agent.NewClaudeRunner(), &recordingWorkspaceProvider{path: filepath.Join(root, "ws")})

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

func (r *holdRig) retryError() string {
	if re, ok := r.orch.Snapshot().RetryAttempts["id1"]; ok && re != nil {
		if re.Error != nil {
			return *re.Error
		}
		return "(retry with no error)"
	}
	return ""
}

// A held command waits for the operator longer than agent.read_timeout_ms
// (and agent.stall_timeout_ms) with the agent CLI printing nothing: the turn
// is neither failed by the idle read deadline nor killed as stalled, and
// once approved the command runs and the run completes.
func TestGuardrailApprovalHoldOutlastsReadTimeout(t *testing.T) {
	r := newHoldRig(t, `vercel --prod > $OUT 2>&1; echo "exit=$?" >> $OUT`)
	require.Eventually(t, func() bool {
		e := r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return e != nil && e.GuardrailApprovalID != ""
	}, 15*time.Second, 20*time.Millisecond, "the held command never raised an approval")

	// Ten read deadlines and more than a stall timeout of silence.
	time.Sleep(2 * time.Second)
	snap := r.orch.Snapshot()
	require.Len(t, snap.Running, 1, "the run keeps waiting for the operator")
	assert.Empty(t, r.retryError(), "a pending approval must not fail the turn")
	require.NotNil(t, snap.InputRequiredIssues["ENG-1"], "the approval is still pending")

	require.NoError(t, r.orch.ProvideInput("ENG-1", "approve"))
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(r.out)
		return strings.Contains(string(data), "exit=0")
	}, 15*time.Second, 20*time.Millisecond, "the approved command never ran")
	require.Eventually(t, func() bool {
		issues, err := r.mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 15*time.Second, 20*time.Millisecond, "the run never completed after the approval")
	assert.Empty(t, r.retryError(), "the completed run was not retried")
}

// Control: a guarded run whose agent is idle with no pending approval
// still fails its turn on the idle read deadline, as before.
func TestGuardedRunIdleWithoutApprovalStillReadTimesOut(t *testing.T) {
	r := newHoldRig(t, "sleep 3")
	require.Eventually(t, func() bool {
		return strings.Contains(r.retryError(), "read timeout after 200ms idle")
	}, 10*time.Second, 20*time.Millisecond, "an idle agent with no approval must still read-time-out")
}
