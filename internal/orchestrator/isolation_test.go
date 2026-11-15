package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// isolationRecordingRunner records each turn and the sandbox it was given.
type isolationRecordingRunner struct {
	mu    sync.Mutex
	turns []*agent.Isolation
	// logBuf, when set, is read during the turn: a successful run's log is
	// removed when the worker finishes (CORE-035).
	logBuf  *logbuffer.Buffer
	logSeen string
}

func (r *isolationRecordingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.turns = append(r.turns, agent.IsolationFrom(ctx))
	if r.logBuf != nil {
		r.logSeen = strings.Join(r.logBuf.Get("ENG-1"), "\n")
	}
	r.mu.Unlock()
	return agent.TurnResult{ResultText: "done", InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

func (r *isolationRecordingRunner) calls() []*agent.Isolation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*agent.Isolation(nil), r.turns...)
}

type stubMechanism struct{}

func (stubMechanism) Name() string     { return "bubblewrap" }
func (stubMechanism) Available() error { return nil }
func (stubMechanism) Wrap(argv []string, _ agent.IsolationPolicy) ([]string, error) {
	return argv, nil
}

// runIsolatedWorker runs the real runWorker once for a profile with
// isolation "process" and returns the runner, the events it sent and the
// orchestrator.
func runIsolatedWorker(t *testing.T, workerHost string, detect func() (agent.IsolationMechanism, error)) (*isolationRecordingRunner, []OrchestratorEvent, *Orchestrator) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles["sandboxed"] = config.AgentProfile{Command: "claude", Isolation: config.IsolationProcess}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &isolationRecordingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.logBuf = logbuffer.New()
	runner.logBuf = o.logBuf
	o.isolationDetect = detect
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, workerHost, "claude", "claude", "sandboxed", true, nil, nil, nil)
	var events []OrchestratorEvent
	for len(o.events) > 0 {
		events = append(events, <-o.events)
	}
	return runner, events, o
}

func workerExit(t *testing.T, events []OrchestratorEvent) OrchestratorEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == EventWorkerExited {
			return ev
		}
	}
	t.Fatal("the worker sent no exit")
	return OrchestratorEvent{}
}

// TestIsolationFailsClosedWhenUnavailable (#258): a profile asking for
// process isolation on a machine with no working sandbox, or on an SSH
// worker, never reaches the runner. The run exits failed with an error
// wrapping agent.ErrIsolationUnavailable, and the issue's log records the
// reason.
func TestIsolationFailsClosedWhenUnavailable(t *testing.T) {
	cases := []struct {
		name, host, reason string
		detect             func() (agent.IsolationMechanism, error)
	}{
		{name: "no_mechanism", reason: "bubblewrap: bwrap is not installed",
			detect: func() (agent.IsolationMechanism, error) {
				return nil, fmt.Errorf("%w: bubblewrap: bwrap is not installed; landlock: not supported", agent.ErrIsolationUnavailable)
			}},
		{name: "ssh_worker", host: "worker-1", reason: "not supported on SSH workers",
			detect: func() (agent.IsolationMechanism, error) { return stubMechanism{}, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, events, o := runIsolatedWorker(t, tc.host, tc.detect)
			assert.Empty(t, runner.calls(), "the agent must not start without its sandbox")
			exit := workerExit(t, events)
			assert.Equal(t, TerminalFailed, exit.RunEntry.TerminalReason)
			require.ErrorIs(t, exit.Error, agent.ErrIsolationUnavailable)
			assert.Contains(t, exit.Error.Error(), tc.reason)
			log := strings.Join(o.logBuf.Get("ENG-1"), "\n")
			assert.Contains(t, log, "run not started")
			assert.Contains(t, log, tc.reason, "the issue's log names the reason")
		})
	}
}

// TestIsolationRunUsesAndReportsSandbox: with a working mechanism every
// turn gets the run's Isolation, the run log states the mechanism and the
// policy, and the event loop learns the mechanism for the issue detail.
func TestIsolationRunUsesAndReportsSandbox(t *testing.T) {
	runner, events, o := runIsolatedWorker(t, "", func() (agent.IsolationMechanism, error) { return stubMechanism{}, nil })
	calls := runner.calls()
	require.NotEmpty(t, calls)
	for _, iso := range calls {
		require.NotNil(t, iso, "every turn runs sandboxed")
		assert.Equal(t, "bubblewrap", iso.Name())
		assert.NotEmpty(t, iso.Policy.Write, "the workspace is writable")
	}
	runner.mu.Lock()
	log := runner.logSeen
	runner.mu.Unlock()
	assert.Contains(t, log, "isolation: process via bubblewrap; write: ", "the run log states the mechanism and policy")
	var reported string
	for _, ev := range events {
		if ev.Type == EventWorkerUpdate && ev.RunEntry != nil && ev.RunEntry.Isolation != "" {
			reported = ev.RunEntry.Isolation
		}
	}
	assert.Equal(t, "bubblewrap", reported)

	// The event loop keeps it on the running entry (for the snapshot).
	state := NewState(o.cfg)
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}}
	state = o.handleEvent(t.Context(), state, OrchestratorEvent{Type: EventWorkerUpdate, IssueID: "id1",
		RunEntry: &RunEntry{Isolation: "bubblewrap"}})
	assert.Equal(t, "bubblewrap", state.Running["id1"].Isolation)
}
