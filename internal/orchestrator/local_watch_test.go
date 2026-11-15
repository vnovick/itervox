package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// TestEditedLocalIssueIsDispatchedWithoutWaitingForThePoll (#133): with the
// local tracker's watch wired to Refresh (as cmd/itervox does), an issue file
// moved to Todo is dispatched at once, although the next poll is an hour away.
func TestEditedLocalIssueIsDispatchedWithoutWaitingForThePoll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "issues")
	require.NoError(t, local.WriteIssue(dir, "ITX-1", local.IssueSpec{Title: "One", State: "Backlog"}))
	cfg := baseConfig()
	cfg.Polling.IntervalMs = int(time.Hour / time.Millisecond)
	cfg.Agent.MaxTurns = 1
	cfg.PromptTemplate = "Work on {{ issue.identifier }}."
	tr := local.New(local.Config{Dir: dir, ActiveStates: cfg.Tracker.ActiveStates, TerminalStates: cfg.Tracker.TerminalStates})
	runner := &countingRunner{}
	orch := orchestrator.New(cfg, tr, runner, &recordingWorkspaceProvider{path: t.TempDir()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = orch.Run(ctx) }()
	go func() { defer wg.Done(); tr.Watch(ctx, time.Hour, orch.Refresh) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	time.Sleep(500 * time.Millisecond) // the first poll has run and found nothing to do
	require.Zero(t, runner.calls.Load())

	// A person edits the file: Backlog → Todo.
	path := filepath.Join(dir, "ITX-1.md")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("---\ntitle: One\nstate: Todo\n---\n"), 0o644))
	later := info.ModTime().Add(2 * time.Second) // past the racy-mtime window
	require.NoError(t, os.Chtimes(path, later, later))
	require.Eventually(t, func() bool { return runner.calls.Load() > 0 }, 5*time.Second, 10*time.Millisecond,
		"the edit is dispatched without waiting for the next poll")
}
