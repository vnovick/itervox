package orchestrator_test

// CORE-011 — orchestrator-chain regression tests for the rate-limit
// auto-switch path.
//
// Scope (deliberately narrow, see the CORE-011 spec title): these drive the
// REAL orchestrator event loop (orchestrator.New + Run, real State, real
// retry scheduling, real classifier, real rate_limited dispatch and the real
// agent.MultiRunner backend routing) with agenttest fakes standing in for the
// vendor CLIs. They do NOT exercise ParseLine / ParseCodexLine, stderr
// assembly or process launch — the runner-level fixtures for those live in
// internal/agent (CORE-001 / CORE-002).
//
// What the chain proves that the handleEvent-level test
// (TestWorkerExitedRateLimitedAutoSwitchSkipsFailedStateAndQueuesSwitchProfile)
// cannot: the failure text travels runner → FailureText → EventWorkerExited →
// bounded retries → IsRateLimitFailureWithPatterns → rate_limited dispatch →
// resolveBackendForIssue → MultiRunner, and the recovery run reaches the
// codex runner with a command whose first token is codex. A switch profile
// whose effective command is "claude ..." (the CORE-010 misconfiguration)
// would reach the codex runner with a claude command and fail the command
// assertion below.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// chainRecoveryCall is what the codex-side double observed for one RunTurn.
type chainRecoveryCall struct {
	command string
}

// chainRecordingRunner records every RunTurn command, announces the call on
// calls (buffered, never blocks the worker), then parks until release is
// closed so the test can inspect the live RunEntry before the run exits.
// The explicit channels are the completion signals — no sleeps.
type chainRecordingRunner struct {
	calls   chan chainRecoveryCall
	release chan struct{}

	mu       sync.Mutex
	commands []string
}

func newChainRecordingRunner() *chainRecordingRunner {
	return &chainRecordingRunner{
		calls:   make(chan chainRecoveryCall, 16),
		release: make(chan struct{}),
	}
}

func (r *chainRecordingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	select {
	case r.calls <- chainRecoveryCall{command: command}:
	default:
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return agent.TurnResult{Failed: true, FailureText: "cancelled"}, ctx.Err()
	}
	return agent.TurnResult{SessionID: "codex-recovery", InputTokens: 10, OutputTokens: 5}, nil
}

func (r *chainRecordingRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commands)
}

// waitForPublication closes the returned channel once published exceeds seen,
// i.e. once the event loop has completed at least one more snapshot
// publication. It stops polling when ctx ends.
func waitForPublication(ctx context.Context, published *atomic.Int64, seen int64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for published.Load() <= seen {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
		close(done)
	}()
	return done
}

const chainMaxRetries = 2

// chainConfig is the shared config for both chain tests: default command is
// claude, the switch profile's command is codex, retries are bounded, and a
// failed_state is configured so the no-match path has an observable end.
func chainConfig() *config.Config {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.Command = "claude --model opus"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = chainMaxRetries
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	cfg.Agent.SwitchWindowHours = 6
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.FailedState = "Cancelled"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"fallback": {Command: "codex"},
	}
	return cfg
}

func chainRule() orchestrator.RateLimitedAutomation {
	return orchestrator.RateLimitedAutomation{
		ID:              "rate-limit-switch",
		SwitchToProfile: "fallback",
		SwitchToBackend: "codex",
		AutoResume:      true,
	}
}

func TestE2E_ClaudeRateLimitSwitchesToCodex(t *testing.T) {
	cfg := chainConfig()
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	claude := agenttest.RateLimitedFailRunner()
	codex := newChainRecordingRunner()
	// The real MultiRunner routes on the backend encoded in the runner
	// command, so which double runs is decided by the orchestrator's
	// resolved runnerCommand — not by the test.
	runner := agent.NewMultiRunner(claude, map[string]agent.Runner{
		"claude": claude,
		"codex":  codex,
	})
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetRateLimitedAutomations([]orchestrator.RateLimitedAutomation{chainRule()})
	// Counts snapshot publications (storeSnap calls OnStateChange right after
	// publishing). Set before Run, as OnStateChange requires.
	var published atomic.Int64
	orch.OnStateChange = func() { published.Add(1) }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	var call chainRecoveryCall
	select {
	case call = <-codex.calls:
	case <-ctx.Done():
		t.Fatalf("recovery never reached the codex runner; claude calls=%d", claude.CallCount())
	}

	// The recovery worker is started from inside the event-loop handler that
	// dispatches it, and the loop publishes that handler's State (storeSnap)
	// only after the handler returns. So RunTurn can be reached — and the call
	// above received — while Snapshot() still returns the PREVIOUS
	// publication: the last claude retry's RunEntry, with no switch recorded.
	// Wait for one publication that completes after the call was observed;
	// the loop is sequential, so it is the dispatching handler's (or a later
	// one's) and must carry the recovery run. The assertions below are
	// unchanged and made on that snapshot.
	seen := published.Load()
	select {
	case <-waitForPublication(ctx, &published, seen):
	case <-ctx.Done():
		t.Fatal("no snapshot was published after the recovery run started")
	}

	// Bounded retries: the initial attempt plus MaxRetries retries, then the
	// exhausted-retry classifier fires. No further claude call may follow.
	assert.EqualValues(t, chainMaxRetries+1, claude.CallCount(),
		"claude must run exactly the initial attempt plus max_retries before the switch")

	// Effective recovery command: MultiRunner strips the backend hint, so this
	// is the switch profile's effective command as the codex runner sees it.
	firstToken := strings.Fields(call.command)
	require.NotEmpty(t, firstToken, "recovery command must not be empty")
	assert.Equal(t, "codex", firstToken[0],
		"recovery runnerCommand must start with codex, got %q", call.command)

	// Recovery backend and profile as recorded on the live RunEntry.
	snap := orch.Snapshot()
	entry, ok := snap.Running["id1"]
	require.True(t, ok, "recovery run must be live while the codex double is parked")
	assert.Equal(t, "codex", entry.Backend, "recovery backend")
	assert.Equal(t, "fallback", entry.ProfileName, "recovery profile")
	assert.Equal(t, "rate-limit-switch", entry.AutomationID)
	assert.Equal(t, config.AutomationTriggerRateLimited, entry.TriggerType)
	assert.Equal(t, "codex", snap.IssueBackends["ENG-1"])

	close(codex.release)

	require.Eventually(t, func() bool {
		got, err := mt.FetchIssueDetail(context.Background(), "id1")
		return err == nil && got.State == "Done"
	}, 5*time.Second, 10*time.Millisecond, "codex recovery must complete the issue")
	assert.Equal(t, 1, codex.callCount(), "exactly one recovery run")
	assert.EqualValues(t, chainMaxRetries+1, claude.CallCount(), "no claude run after the switch")

	cancel()
	<-runDone
}

func TestOrchestratorChain_NonRateLimitFailureDoesNotSwitch(t *testing.T) {
	cfg := chainConfig()
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	// Same rule, same profiles — only the failure text differs, and it must
	// not match any default rate-limit pattern.
	claude := agenttest.FailRunner("error: compilation failed in pkg/foo")
	codex := newChainRecordingRunner()
	close(codex.release) // never expected to run; do not park if it does
	runner := agent.NewMultiRunner(claude, map[string]agent.Runner{
		"claude": claude,
		"codex":  codex,
	})
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetRateLimitedAutomations([]orchestrator.RateLimitedAutomation{chainRule()})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Explicit end signal: retry exhaustion with failed_state configured moves
	// the issue to FailedState through asyncDiscardAndTransitionTo.
	require.Eventually(t, func() bool {
		got, err := mt.FetchIssueDetail(context.Background(), "id1")
		return err == nil && got.State == "Cancelled"
	}, 8*time.Second, 10*time.Millisecond, "non-rate-limit exhaustion must end in failed_state")

	assert.EqualValues(t, chainMaxRetries+1, claude.CallCount(),
		"retries must be bounded by max_retries")
	assert.Equal(t, 0, codex.callCount(), "no recovery dispatch may reach the codex runner")
	snap := orch.Snapshot()
	assert.Empty(t, snap.IssueBackends["ENG-1"], "no backend switch recorded")
	assert.Empty(t, snap.IssueProfiles["ENG-1"], "no profile switch recorded")

	cancel()
	<-runDone
}
