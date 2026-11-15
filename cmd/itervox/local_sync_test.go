package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
	"github.com/vnovick/itervox/internal/tracker/localsync"
)

// editorTracker is a memory tracker that can also create and edit issues,
// enough to build a local sync over it.
type editorTracker struct{ *tracker.MemoryTracker }

func (editorTracker) CreateScopedIssue(context.Context, tracker.IssueDraft) (*domain.Issue, error) {
	return nil, context.Canceled
}

func (editorTracker) UpdateIssueFields(context.Context, string, tracker.IssueFieldsPatch) error {
	return nil
}

func (editorTracker) FindIssueByKey(context.Context, string) (*domain.Issue, bool, error) {
	return nil, false, nil
}

// TestWithLocalSyncWrapsTheLocalTracker (#118): with tracker.sync set, the
// local tracker is wrapped with its sync (still a local tracker for every
// method); without it, or for another tracker, nothing changes.
func TestWithLocalSyncWrapsTheLocalTracker(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "WORKFLOW.md")
	cfg := &config.Config{WorkflowPath: wf, Tracker: config.TrackerConfig{Kind: "local", PushIntervalMs: 10_000,
		Sync: config.TrackerSyncConfig{Enabled: true, TrackerConnection: config.TrackerConnection{
			Kind: "github", APIKey: "ghp_test", ProjectSlug: "o/r", ActiveStates: []string{"Todo"}},
			PullStates: []string{"Todo"}, IntervalMs: 60_000, FullSyncIntervalMs: 3_600_000}}}
	lt := local.New(local.Config{Dir: config.LocalIssuesDir(cfg)})

	got := withLocalSync(cfg, wf, lt)
	w, ok := got.(*syncedLocal)
	require.True(t, ok, "tracker.sync wraps the local tracker")
	assert.Same(t, lt, w.Tracker)
	_, isSetter := got.(tracker.StateListSetter)
	assert.True(t, isSetter, "the local tracker's optional interfaces are kept")

	cfg.Tracker.Sync.Enabled = false
	assert.Same(t, lt, withLocalSync(cfg, wf, lt).(*local.Tracker))
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	cfg.Tracker.Sync.Enabled = true
	assert.Same(t, mem, withLocalSync(cfg, wf, mem).(*tracker.MemoryTracker))
}

// TestSyncStatusReportsLocalSync (#118): with tracker.sync, the sync status
// is in local_sync mode, its last pull is the sync's last good cycle, and
// the sync's queued remote writes count as pending writes.
func TestSyncStatusReportsLocalSync(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "issues")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ITX-1.md"), []byte("---\ntitle: One\nstate: Todo\n---\n"), 0o644))
	lt := local.New(local.Config{Dir: dir})
	ob, err := outbox.New("")
	require.NoError(t, err)
	ob.SetPushInterval(time.Hour) // the create stays queued
	syncer, err := localsync.New(localsync.Config{Kind: "github", Local: lt, Outbox: ob, PullStates: []string{"Todo"},
		Remote: editorTracker{tracker.NewMemoryTracker(nil, []string{"Todo"}, nil)}, Interval: time.Minute})
	require.NoError(t, err)
	require.NoError(t, syncer.Cycle(context.Background()))
	w := &syncedLocal{Tracker: lt, syncer: syncer}
	// What runCycle records after a good cycle.
	before := time.Now()

	cfg := &config.Config{Tracker: config.TrackerConfig{Kind: "local", Outbox: true}}
	src := newSyncStatusSource(cfg, w, nil, nil)
	row := src.row(time.Now())
	assert.Equal(t, server.SyncModeLocalSync, row.Mode)
	assert.Equal(t, int64(60_000), row.SyncIntervalMs)
	assert.Equal(t, 1, row.PendingWrites, "the queued remote create counts")
	assert.Equal(t, 1, row.PendingBatched)
	assert.Nil(t, row.LastPullAt, "Cycle alone records no status; Run does")

	ctx, cancel := context.WithCancel(context.Background())
	done := startLocalSync(ctx, w, func() {})
	require.Eventually(t, func() bool { return !syncer.Status().SyncedAt.IsZero() }, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	row = src.row(time.Now())
	require.NotNil(t, row.LastPullAt)
	assert.False(t, row.LastPullAt.Before(before))
	assert.False(t, row.Stale)
}
