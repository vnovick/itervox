package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

// moveMidTurnRunner is an agent that, during its first turn, opens a PR (the
// fake gh reports one) and moves its own issue out of the active states — the
// shape of an agent calling the move_state action right after `gh pr
// create` — and then keeps working for a while before its turn ends, long
// enough for several reconcile ticks to see the moved issue.
type moveMidTurnRunner struct {
	mu        sync.Mutex
	calls     int
	move      func(ctx context.Context)
	hold      time.Duration
	cancelled bool
}

func (r *moveMidTurnRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	first := n == 1
	r.mu.Unlock()
	if first && r.move != nil {
		r.move(ctx)
		select {
		case <-time.After(r.hold):
		case <-ctx.Done():
			r.mu.Lock()
			r.cancelled = true
			r.mu.Unlock()
			return agent.TurnResult{}, ctx.Err()
		}
	}
	// A distinct result per turn: the worker ends a run whose turns repeat
	// themselves, which would hide whether the deferred move ended it.
	return agent.TurnResult{SessionID: "s", InputTokens: 1, OutputTokens: 1,
		ResultText: "Opened the PR and wrote the summary (turn " + strconv.Itoa(n) + ")."}, nil
}

func (r *moveMidTurnRunner) turns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *moveMidTurnRunner) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

func dsmComments(t *testing.T, mt *tracker.MemoryTracker, id string) []string {
	t.Helper()
	iss, err := mt.FetchIssueDetail(context.Background(), id)
	require.NoError(t, err)
	out := make([]string, 0, len(iss.Comments))
	for _, c := range iss.Comments {
		out = append(out, c.Body)
	}
	return out
}

func dsmState(t *testing.T, mt *tracker.MemoryTracker, id string) string {
	t.Helper()
	issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{id})
	require.NoError(t, err)
	if len(issues) == 0 {
		return ""
	}
	return issues[0].State
}

func handoffFiles(t *testing.T, wsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(wsDir, orchestrator.HandoffDirRelPath))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// runUntilCancelled runs orch until ctx is cancelled and makes the test wait
// for Run to return: Run's shutdown flush writes the ledgers, and a write
// landing during t.TempDir's cleanup would fail the test.
func runUntilCancelled(t *testing.T, orch *orchestrator.Orchestrator, ctx context.Context) {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("orchestrator Run did not return after cancel")
		}
	})
}

// agentMove is what the move_state agent action does (cmd/itervox's adapter):
// offer the move to the orchestrator first, and write it to the tracker
// directly only when the orchestrator did not defer it.
func agentMove(orch *orchestrator.Orchestrator, mt *tracker.MemoryTracker, issue domain.Issue, target string) func(ctx context.Context) {
	return func(ctx context.Context) {
		if orch.DeferAgentStateMove(ctx, orchestrator.DeferredStateMove{
			IssueID: issue.ID, Identifier: issue.Identifier, TargetState: target, FromState: issue.State,
		}) {
			return
		}
		_ = mt.UpdateIssueState(ctx, issue.ID, target)
	}
}

// TestPROpenedCompletionStateMoveLetsWorkerFinish (#177): the agent opens its
// PR and the issue is moved to completion_state by itervox while the worker is
// still running. The worker must not be stopped by reconciliation: it posts
// its post-PR tracker comment, writes its handoff, exits as a success, and
// the pr_opened automation still fires after the exit.
func TestPROpenedCompletionStateMoveLetsWorkerFinish(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/77"
	calls := installFakeGHForPR(t, prURL)
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"summarizer": {Command: "claude"}}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &moveMidTurnRunner{hold: 400 * time.Millisecond}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})
	runner.move = agentMove(orch, mt, issue, "In Review")
	orch.SetPROpenedAutomations([]orchestrator.PROpenedAutomation{{ID: "pr-summary", ProfileName: "summarizer"}})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runUntilCancelled(t, orch, ctx)

	firstRun := func() (orchestrator.CompletedRun, bool) {
		for _, h := range orch.RunHistory() {
			if h.Identifier == "ENG-1" && h.Kind != "automation" {
				return h, true
			}
		}
		return orchestrator.CompletedRun{}, false
	}
	require.Eventually(t, func() bool { _, ok := firstRun(); return ok },
		7*time.Second, 20*time.Millisecond, "the worker run must finish and be recorded")

	run, _ := firstRun()
	status := run.Status
	assert.Equal(t, "succeeded", status, "the worker must finish its run, not be stopped by reconciliation")
	assert.Equal(t, 1, run.TurnCount, "the deferred move ends the turn loop like a landed move would")

	comments := dsmComments(t, mt, "id1")
	var prComment bool
	for _, c := range comments {
		if strings.Contains(c, "Pull request created: "+prURL) {
			prComment = true
		}
	}
	assert.True(t, prComment, "the post-PR tracker comment must be written; comments: %v; gh calls: %v", comments, calls())
	assert.NotEmpty(t, handoffFiles(t, wsDir), "the run's handoff must be written")
	require.Eventually(t, func() bool { return dsmState(t, mt, "id1") == "In Review" },
		2*time.Second, 20*time.Millisecond, "the issue ends in completion_state")
	require.Eventually(t, func() bool {
		for _, h := range orch.RunHistory() {
			if h.Identifier == "ENG-1" && h.AutomationID == "pr-summary" {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "the pr_opened automation runs after the worker's exit")
	cancel()
}

// TestHumanTerminalMoveStillStopsRunningWorker (#177): a person moving the
// issue to a terminal state still stops its running worker — even when the
// agent had already asked for a move that is waiting for the run's exit. The
// person's move wins: the deferred move is dropped, and the worker writes no
// post-PR comment.
func TestHumanTerminalMoveStillStopsRunningWorker(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/78"
	installFakeGHForPR(t, prURL)
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "In Review"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &moveMidTurnRunner{hold: 5 * time.Second}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})
	deferred := make(chan bool, 1)
	runner.move = func(ctx context.Context) {
		// The agent's own move is deferred …
		deferred <- orch.DeferAgentStateMove(ctx, orchestrator.DeferredStateMove{
			IssueID: issue.ID, Identifier: issue.Identifier, TargetState: "In Review", FromState: issue.State,
		})
		// … then a person cancels the issue in the tracker (the dashboard
		// and the tracker UI write straight to the tracker).
		_ = mt.UpdateIssueState(ctx, issue.ID, "Cancelled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runUntilCancelled(t, orch, ctx)

	select {
	case d := <-deferred:
		require.True(t, d, "the agent's move is deferred while its worker runs")
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never moved its issue")
	}
	require.Eventually(t, runner.wasCancelled, 3*time.Second, 20*time.Millisecond,
		"reconciliation must stop the worker when a person moves the issue to a terminal state")
	require.Eventually(t, func() bool { return len(orch.Snapshot().DeferredStateMoves) == 0 },
		2*time.Second, 20*time.Millisecond, "the person's move drops the deferred move")
	time.Sleep(200 * time.Millisecond) // a few more ticks: nothing may move the issue back
	assert.Equal(t, "Cancelled", dsmState(t, mt, "id1"), "the person's move wins")
	for _, c := range dsmComments(t, mt, "id1") {
		assert.NotContains(t, c, "Pull request created", "a stopped worker posts no post-PR comment")
	}
	cancel()
}

// TestDeferredStateMoveAppliedAfterWorkerExits (#177): with no
// completion_state of its own to apply, the worker finishes its post-PR work
// with the issue still active in the tracker, and the agent's move lands
// after the run's exit.
func TestDeferredStateMoveAppliedAfterWorkerExits(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/79"
	installFakeGHForPR(t, prURL)
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &moveMidTurnRunner{hold: 300 * time.Millisecond}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})
	movesFile := filepath.Join(t.TempDir(), "deferred_state_moves.json")
	orch.SetDeferredStateMovesFile(movesFile)
	var stateWhileFinishing string
	runner.move = func(ctx context.Context) {
		agentMove(orch, mt, issue, "Done")(ctx)
		stateWhileFinishing = dsmState(t, mt, "id1")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runUntilCancelled(t, orch, ctx)

	require.Eventually(t, func() bool { return dsmState(t, mt, "id1") == "Done" },
		7*time.Second, 20*time.Millisecond, "the deferred move lands after the run")
	assert.Equal(t, "In Progress", stateWhileFinishing, "the move waits for the worker: the tracker is untouched while it runs")
	var status string
	for _, h := range orch.RunHistory() {
		if h.Identifier == "ENG-1" {
			status = h.Status
		}
	}
	assert.Equal(t, "succeeded", status)
	var prComment bool
	for _, c := range dsmComments(t, mt, "id1") {
		prComment = prComment || strings.Contains(c, "Pull request created: "+prURL)
	}
	assert.True(t, prComment, "the post-PR comment was written before the move")
	assert.NotEmpty(t, handoffFiles(t, wsDir))
	require.Eventually(t, func() bool { return len(orch.Snapshot().DeferredStateMoves) == 0 },
		2*time.Second, 20*time.Millisecond, "an applied move leaves the ledger")
	var row bool
	for _, ch := range orch.Snapshot().IssueStatusHistory["ENG-1"] {
		row = row || (ch.ToState == "Done" && ch.Source == orchestrator.StatusSourceWorkerLifecycle)
	}
	assert.True(t, row, "the applied move is recorded in the issue's status history")
	cancel()
}

// TestDeferredStateMoveSurvivesRestart (#177): the daemon stops while the
// worker is still running with a deferred move. The move is on disk; the
// next daemon applies it on its first tick, and does not start the issue
// again before it has landed.
func TestDeferredStateMoveSurvivesRestart(t *testing.T) {
	installFakeGHForPR(t, "https://github.com/o/r/pull/80")
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	movesFile := filepath.Join(t.TempDir(), "deferred_state_moves.json")

	// First daemon: the agent's move is deferred, then the daemon goes away
	// while the worker is still running. The ledger as it stood then is what
	// a crash would leave behind.
	mt1 := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner1 := &moveMidTurnRunner{hold: 5 * time.Second}
	orch1 := orchestrator.New(cfg, mt1, runner1, &recordingWorkspaceProvider{path: t.TempDir()})
	orch1.SetDeferredStateMovesFile(movesFile)
	runner1.move = agentMove(orch1, mt1, issue, "Done")
	ctx1, cancel1 := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel1()
	runUntilCancelled(t, orch1, ctx1)
	var onDisk []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(movesFile)
		if err != nil || !strings.Contains(string(b), `"target_state":"Done"`) {
			return false
		}
		onDisk = b
		return true
	}, 5*time.Second, 20*time.Millisecond, "the deferred move is persisted while the worker runs")
	assert.Equal(t, "In Progress", dsmState(t, mt1, "id1"), "nothing was written to the tracker yet")
	cancel1()

	// Second daemon, same tracker state, the ledger the first one left.
	restartFile := filepath.Join(t.TempDir(), "deferred_state_moves.json")
	require.NoError(t, os.WriteFile(restartFile, onDisk, 0o644))
	mt2 := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner2 := &moveMidTurnRunner{}
	orch2 := orchestrator.New(cfg, mt2, runner2, &recordingWorkspaceProvider{path: t.TempDir()})
	orch2.SetDeferredStateMovesFile(restartFile)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel2()
	runUntilCancelled(t, orch2, ctx2)

	require.Eventually(t, func() bool { return dsmState(t, mt2, "id1") == "Done" },
		5*time.Second, 20*time.Millisecond, "the restarted daemon applies the deferred move")
	assert.Zero(t, runner2.turns(), "the issue is not started again before its move lands")
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(restartFile)
		return err == nil && !strings.Contains(string(b), "ENG-1")
	}, 2*time.Second, 20*time.Millisecond, "the applied move leaves the ledger on disk")
	cancel2()
}

// TestDeferredStateMoveDroppedWhenIssueMovedMeanwhile (#177): a deferred move
// is applied only while the tracker still shows the state the agent moved the
// issue from. Here a person moved the issue while the daemon was down; the
// restarted daemon drops the move instead of overwriting theirs.
func TestDeferredStateMoveDroppedWhenIssueMovedMeanwhile(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	movesFile := filepath.Join(t.TempDir(), "deferred_state_moves.json")
	require.NoError(t, os.WriteFile(movesFile, []byte(`{"ENG-1":{"issue_id":"id1","identifier":"ENG-1",`+
		`"target_state":"Done","from_state":"In Progress","requested_at":"2026-01-01T00:00:00Z"}}`), 0o644))
	issue := makeIssue("id1", "ENG-1", "Backlog", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &moveMidTurnRunner{}, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetDeferredStateMovesFile(movesFile)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runUntilCancelled(t, orch, ctx)

	require.Eventually(t, func() bool {
		b, err := os.ReadFile(movesFile)
		return err == nil && !strings.Contains(string(b), "ENG-1")
	}, 5*time.Second, 20*time.Millisecond, "the move is settled")
	assert.Equal(t, "Backlog", dsmState(t, mt, "id1"), "the person's move is not overwritten")
	cancel()
}
