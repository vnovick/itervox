package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// holdRunner keeps every turn running until its context ends.
type holdRunner struct{}

func (holdRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

// TestConfigWatcherRaisesPolicyChangeWhileRunsActive: a hand edit of a
// governing file while a run is active is loud — the config watcher hands
// it to the running generation, which emits a policy_changed feed event
// naming the file and the active run (possibly changed by the unsandboxed
// agent), publishes the dashboard banner, and `itervox watch` prints it as
// a POLICY CHANGED line.
func TestConfigWatcherRaisesPolicyChangeWhileRunsActive(t *testing.T) {
	policyAlertPtr.Store(nil)
	t.Cleanup(func() { policyAlertPtr.Store(nil) })
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	project := config.ProjectSettingsPath(path)
	writeSettingsFile(t, project, maxAgentsSettings("3"))

	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}},
		[]string{"In Progress"}, []string{"Done"})
	cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates = []string{"In Progress"}, []string{"Done"}
	cfg.Polling.IntervalMs = 20
	cfg.Workspace.Root = filepath.Join(t.TempDir(), "ws")
	wm := workspace.NewManager(cfg)
	orch := orchestrator.New(cfg, mt, holdRunner{}, wm)
	t.Cleanup(watchPolicyChanges(orch))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	var runID string
	require.Eventually(t, func() bool {
		for _, e := range orch.Snapshot().Running {
			if e != nil && e.RunID != "" {
				runID = e.RunID
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "ENG-1 runs")

	w := startConfigWatch(t, path)
	mark := w.mark()
	writeSettingsFile(t, project, maxAgentsSettings("7"))
	w.waitJudged(t, mark, project)

	var ev orchestrator.FeedEvent
	require.Eventually(t, func() bool {
		for _, e := range orch.Snapshot().Feed {
			if e.Kind == orchestrator.FeedPolicyChanged {
				ev = e
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the change is raised")
	assert.Equal(t, orchestrator.PolicySourceWatcher, ev.Payload.Source)
	assert.Equal(t, []string{".itervox/settings.jsonc"}, ev.Payload.Files)
	assert.Equal(t, []string{runID}, ev.Payload.RunIDs)
	assert.True(t, ev.Payload.PossiblyAgent)

	banner := loadPolicyAlert()
	require.NotNil(t, banner, "the dashboard banner is published")
	assert.Equal(t, []string{".itervox/settings.jsonc"}, banner.Files)
	assert.Equal(t, []string{runID}, banner.RunIDs)
	assert.True(t, banner.PossiblyAgent)

	assertWatchShowsPolicyChange(t, ev, runID)
}
