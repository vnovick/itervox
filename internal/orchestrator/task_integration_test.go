package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// taskTestConfig is a project whose poll interval is long enough that only
// an explicit dispatch tick (Start) can start a run within the test.
func taskTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	project := t.TempDir()
	cfg := baseConfig()
	cfg.WorkflowPath = filepath.Join(project, "WORKFLOW.md")
	cfg.Polling.IntervalMs = 600_000
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Implement {{ issue.identifier }}."
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Soul: "SOUL-MARK", Instructions: "INSTRUCTIONS-MARK"},
	}
	return cfg, project
}

// waitFirstPoll blocks until the orchestrator's startup tick has polled, so
// a later dispatch can only come from a new tick.
func waitFirstPoll(t *testing.T, orch *orchestrator.Orchestrator) {
	t.Helper()
	require.Eventually(t, func() bool { return !orch.Readiness().LastPollOKAt.IsZero() },
		5*time.Second, 10*time.Millisecond, "the startup poll")
}

// TestNewTaskCreatesLocalIssueAndDispatches (#251): in a project whose
// tracker is not local, New task writes an Itervox-only task file
// (TASK-1.md, first active state) next to the tracker, and "Create and
// start" dispatches it through the normal tick — with the poll interval far
// in the future, only the requested tick can start it. The run's prompt
// carries the task's instructions after INSTRUCTIONS.md, the first run
// freezes them, and the run's completion moves the task file, not a
// tracker issue.
func TestNewTaskCreatesLocalIssueAndDispatches(t *testing.T) {
	cfg, project := taskTestConfig(t)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	issuesDir := filepath.Join(project, ".itervox", "issues")
	tasks := local.New(local.Config{Dir: issuesDir, Prefix: local.TaskPrefix,
		ActiveStates: cfg.Tracker.ActiveStates, TerminalStates: cfg.Tracker.TerminalStates})
	runner := &evidenceRunner{}
	orch := orchestrator.New(cfg, local.WithTasks(mt, tasks), runner, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetTaskSpecsFile(filepath.Join(project, ".itervox", "task_specs.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	waitFirstPoll(t, orch)
	require.True(t, orch.LocalTasksAvailable())

	res, err := orch.CreateTask(ctx, orchestrator.TaskRequest{
		Title: "Upgrade lodash", Instructions: "TASK-MARK upgrade lodash and run the tests.",
		Profile: "implementer", Start: true,
	})
	require.NoError(t, err)
	assert.True(t, res.Local)
	assert.True(t, res.StartRequested)
	assert.Equal(t, "TASK-1", res.Issue.Identifier)
	assert.Equal(t, "Todo", res.Issue.State, "created in the first active state")
	raw, err := os.ReadFile(filepath.Join(issuesDir, "TASK-1.md"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "title: Upgrade lodash")
	none, err := mt.FetchIssuesByStates(ctx, cfg.Tracker.ActiveStates)
	require.NoError(t, err)
	assert.Empty(t, none, "a local task never reaches the tracker")

	require.Eventually(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		return len(runner.prompts) > 0
	}, 5*time.Second, 10*time.Millisecond, "Start dispatches through the normal tick")
	runner.mu.Lock()
	p := runner.prompts[0]
	runner.mu.Unlock()
	instr, block := strings.Index(p, "INSTRUCTIONS-MARK"), strings.Index(p, "## Task instructions\n\nTASK-MARK")
	require.True(t, instr > 0 && block > instr, "the task block follows INSTRUCTIONS.md (instructions %d, task %d)", instr, block)

	spec, ok := orch.TaskSpec("TASK-1")
	require.True(t, ok)
	assert.NotNil(t, spec.StartedAt, "the first run marks the task started")
	assert.ErrorIs(t, orch.SetTaskInstructions("TASK-1", "too late"), orchestrator.ErrTaskStarted)

	// The run's completion moves the task file to completion_state, and the
	// run's history names the task instructions it was given.
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(issuesDir, "TASK-1.md"))
		return err == nil && strings.Contains(string(raw), "state: Done")
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(orch.RunHistory()) > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.NotEmpty(t, orch.RunHistory()[0].PromptParts.Task)

	// The spec survives a restart (task_specs.json, written by the ledger
	// writer).
	require.Eventually(t, func() bool {
		r := orchestrator.New(cfg, mt, nil, nil)
		r.SetTaskSpecsFile(filepath.Join(project, ".itervox", "task_specs.json"))
		s, ok := r.TaskSpec("TASK-1")
		return ok && s.StartedAt != nil && s.Local
	}, 5*time.Second, 20*time.Millisecond)
}

// TestNewTaskOnTrackerUsesTrackerCreate (#251): "Tracker issue" creates the
// task with the tracker's CreateIssue (here the memory tracker) in the
// first active state, records its instructions, base branch and playbook
// under the tracker's identifier, and applies the profile and backend as
// the issue's overrides. Without Start nothing is dispatched by the create
// itself. A tracker that cannot hold local tasks refuses a local task.
func TestNewTaskOnTrackerUsesTrackerCreate(t *testing.T) {
	cfg, _ := taskTestConfig(t)
	seed := makeIssue("id-seed", "ENG-1", "Backlog", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{seed}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &evidenceRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: t.TempDir()})
	ctx := t.Context()
	require.False(t, orch.LocalTasksAvailable())

	_, err := orch.CreateTask(ctx, orchestrator.TaskRequest{Title: "Local please"})
	assert.ErrorIs(t, err, orchestrator.ErrLocalTasksUnavailable)
	_, err = orch.CreateTask(ctx, orchestrator.TaskRequest{Title: "  ", OnTracker: true})
	assert.ErrorIs(t, err, orchestrator.ErrTaskTitleRequired)

	res, err := orch.CreateTask(ctx, orchestrator.TaskRequest{
		Title: "Add parser tests", Instructions: "Cover the error paths.", OnTracker: true,
		Profile: "implementer", Backend: "codex", BaseBranch: "release/2.x",
	})
	require.NoError(t, err)
	assert.False(t, res.Local)
	assert.False(t, res.StartRequested)
	got, err := mt.FetchIssueByIdentifier(ctx, res.Issue.Identifier)
	require.NoError(t, err, "the issue is in the tracker")
	assert.Equal(t, "Add parser tests", got.Title)
	assert.Equal(t, "Todo", got.State)

	spec, ok := orch.TaskSpec(res.Issue.Identifier)
	require.True(t, ok)
	assert.Equal(t, "Cover the error paths.", spec.Instructions)
	assert.Equal(t, "release/2.x", spec.BaseBranch)
	assert.False(t, spec.Local)
	assert.Nil(t, spec.StartedAt)
	snap := orch.Snapshot()
	assert.Equal(t, "implementer", snap.IssueProfiles[res.Issue.Identifier])
	assert.Equal(t, "codex", snap.IssueBackends[res.Issue.Identifier])

	// Editable until it starts.
	require.NoError(t, orch.SetTaskInstructions(res.Issue.Identifier, "Cover every path."))
	spec, _ = orch.TaskSpec(res.Issue.Identifier)
	assert.Equal(t, "Cover every path.", spec.Instructions)
	assert.ErrorIs(t, orch.SetTaskInstructions("ENG-1", "x"), orchestrator.ErrTaskNotFound)

	// Joined with local tasks, "Tracker issue" still goes to the tracker.
	joined := orchestrator.New(cfg, local.WithTasks(mt, local.New(local.Config{Dir: t.TempDir(), Prefix: local.TaskPrefix})), runner, nil)
	res2, err := joined.CreateTask(ctx, orchestrator.TaskRequest{Title: "Tracker one", OnTracker: true})
	require.NoError(t, err)
	assert.False(t, res2.Local)
	_, err = mt.FetchIssueByIdentifier(ctx, res2.Issue.Identifier)
	assert.NoError(t, err)
	assert.False(t, strings.HasPrefix(res2.Issue.Identifier, local.TaskPrefix))
	runner.mu.Lock()
	assert.Empty(t, runner.prompts, "nothing ran: the orchestrator was never started")
	runner.mu.Unlock()
}
