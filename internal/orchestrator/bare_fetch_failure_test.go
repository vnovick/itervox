package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// bareFetchFailureProvider hands out a workspace whose creation reported a
// failed shared bare-clone fetch, the way workspace.Manager does (#195).
type bareFetchFailureProvider struct{ dir string }

func (p *bareFetchFailureProvider) EnsureWorkspace(_ context.Context, id, _ string) (workspace.Workspace, error) {
	return workspace.Workspace{Path: p.dir, Identifier: id, CreatedNow: true,
		BareFetchFailure: "shared bare clone fetch failed, so new worktrees branch from a stale base: fatal: unable to access origin"}, nil
}
func (p *bareFetchFailureProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *bareFetchFailureProvider) ResolvePath(string) string                             { return p.dir }

// TestWorkerRecordsBareFetchFailure (#195): a failed bare fetch reported by
// the workspace lands in recent failures as workspace_fetch (it used to be a
// WARN line only), and the run still goes ahead on the last fetched base.
func TestWorkerRecordsBareFetchFailure(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("ida1", "A-1", "Todo", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &bareFetchFailureProvider{dir: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	var got orchestrator.FailureRecord
	require.Eventually(t, func() bool {
		for _, f := range orch.Snapshot().RecentFailures {
			if f.Kind == orchestrator.FailureKindWorkspaceFetch {
				got = f
				return true
			}
		}
		return false
	}, 6*time.Second, 20*time.Millisecond)
	assert.Equal(t, "A-1", got.Identifier)
	assert.Equal(t, "bare_fetch", got.Source)
	assert.Contains(t, got.Message, "stale base")
	require.Eventually(t, func() bool { return runnerCalls(runner) > 0 }, 6*time.Second, 20*time.Millisecond,
		"the run is not blocked by a failed fetch")
}
