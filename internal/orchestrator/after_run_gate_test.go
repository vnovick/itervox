package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// F3 — hooks.after_run_required turns the after_run hook into a per-unit
// gate: when the final turn's hook exits non-zero, the unit must NOT reach
// TerminalSucceeded on the agent backend's clean exit alone. With retries
// exhausted it lands in FailedState instead of CompletionState.
func TestAfterRunRequiredGateBlocksSuccess(t *testing.T) {
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.FailedState = "Failed"
	cfg.Tracker.TerminalStates = append(cfg.Tracker.TerminalStates, "Failed")
	cfg.Hooks.AfterRun = "exit 1"
	cfg.Hooks.AfterRunRequired = true
	cfg.Hooks.TimeoutMs = 5000

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	// The agent backend itself always exits clean — only the gate fails.
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Failed"
	}, 7*time.Second, 30*time.Millisecond,
		"a clean agent exit with a failing required after_run gate must fail the unit, not complete it")

	issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
	require.NoError(t, err)
	require.NotEqual(t, "Done", issues[0].State,
		"the unit must never reach the completion state while the gate fails")
	cancel()
}

// Default behavior is unchanged: without after_run_required, a failing
// after_run hook is best-effort (logged and ignored) and the unit completes.
func TestAfterRunFailureIgnoredWhenNotRequired(t *testing.T) {
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Hooks.AfterRun = "exit 1"
	cfg.Hooks.AfterRunRequired = false
	cfg.Hooks.TimeoutMs = 5000

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 7*time.Second, 30*time.Millisecond,
		"without after_run_required a failing hook must not block completion")
	cancel()
}
