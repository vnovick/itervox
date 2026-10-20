package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

type fakeStoreStatus struct{ st store.SyncStatus }

func (f fakeStoreStatus) SyncStatus() store.SyncStatus { return f.st }

// writeLiveHeartbeat lays out a project whose daemon (this test process) is
// alive and whose HEARTBEAT.md carries row, rendered by the daemon's own
// heartbeat renderer.
func writeLiveHeartbeat(t *testing.T, row *server.SyncStatusRow) string {
	t.Helper()
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\nnot: valid\n---\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "daemon.pid"),
		fmt.Appendf(nil, "%d\t%s\tflock\n", os.Getpid(), wf), 0o600))
	hb := renderHeartbeat(server.StateSnapshot{SyncStatus: row}, heartbeatOptions{WorkflowPath: wf}, time.Now())
	require.NoError(t, os.WriteFile(heartbeatPath(wf), []byte(hb), 0o644))
	return wf
}

// TestDoctorPrintsSyncStatusAndWarnsPastLagThreshold pins #117's doctor
// half end to end: the daemon's sync status (store → snapshot row →
// HEARTBEAT.md) is printed by `itervox doctor`, which warns once the lag at
// its own clock is past the daemon's threshold — without failing the exit
// code, since a lagging sync is an outage, not a broken configuration.
func TestDoctorPrintsSyncStatusAndWarnsPastLagThreshold(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	ob.SetPushInterval(time.Hour)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "1", Identifier: "ENG-1", TargetState: "Done"}))

	source := func(lastPull time.Time, lastErr error) *server.SyncStatusRow {
		src := &syncStatusSource{
			store: fakeStoreStatus{store.SyncStatus{
				SyncedAt: lastPull, LastErr: lastErr, LastErrAt: lastPull.Add(time.Minute),
				Failures: 3, Interval: time.Minute,
			}},
			outbox: ob,
		}
		return src.row(time.Now())
	}

	t.Run("fresh", func(t *testing.T) {
		wf := writeLiveHeartbeat(t, source(time.Now().Add(-time.Minute), nil))
		report, _ := collectDoctorReport(wf)
		require.Empty(t, report.HeartbeatStaleWarning)
		require.NotNil(t, report.Sync)
		assert.Equal(t, "store", report.Sync.Mode)
		assert.Empty(t, report.SyncLagWarning)
		out := renderDoctorReport(report)
		assert.Contains(t, out, "sync: store · last pull ")
		assert.Contains(t, out, "warns after 5m0s")
		assert.Contains(t, out, "pending writes 1 (urgent 0, batched 1, retrying 0, degraded 0, rate limited 0)")
		assert.Contains(t, out, "next push ")
		assert.NotContains(t, out, "WARNING: tracker sync lag")
	})

	t.Run("stale", func(t *testing.T) {
		down := fmt.Errorf("pull: %w", &tracker.APIStatusError{Adapter: "linear", Status: 503})
		wf := writeLiveHeartbeat(t, source(time.Now().Add(-12*time.Minute), down))
		report, _ := collectDoctorReport(wf)
		require.NotNil(t, report.Sync)
		assert.Regexp(t, `^tracker sync lag 12m[0-9]s is past 5m0s`, report.SyncLagWarning)
		out := renderDoctorReport(report)
		assert.Regexp(t, `\nWARNING: tracker sync lag 12m[0-9]s is past 5m0s`, out)
		assert.Contains(t, out, "last sync error: api_status at ")
		assert.Contains(t, out, "linear: api status 503")

		report.SchemaPassed = true // isolate the sync warning from the test workflow
		assert.Equal(t, 0, doctorExitCode(report), "a lagging sync warns; it does not fail doctor")
	})

	t.Run("no sync section", func(t *testing.T) {
		wf := writeLiveHeartbeat(t, nil)
		report, _ := collectDoctorReport(wf)
		assert.Nil(t, report.Sync)
		assert.NotContains(t, renderDoctorReport(report), "sync:")
	})
}

// TestSyncStatusRowFromStoreAndOutbox pins the snapshot row (#117): store
// mode with lag, stale threshold and a classified (never raw) error; direct
// mode from the last successful poll.
func TestSyncStatusRowFromStoreAndOutbox(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	raw := errors.New(`linear_unknown_payload: map[errors:[lin_api_SECRET]]`)
	src := &syncStatusSource{store: fakeStoreStatus{store.SyncStatus{
		SyncedAt: now.Add(-4 * time.Minute), Cursor: now.Add(-5 * time.Minute),
		LastErr: raw, LastErrAt: now.Add(-time.Minute), Failures: 2, Interval: time.Minute,
	}}}
	row := src.row(now)
	assert.Equal(t, server.SyncModeStore, row.Mode)
	require.NotNil(t, row.LagMs)
	assert.Equal(t, (4 * time.Minute).Milliseconds(), *row.LagMs)
	assert.Equal(t, (5 * time.Minute).Milliseconds(), row.LagWarnAfterMs, "3 x 1m interval, floored at 5m")
	assert.False(t, row.Stale)
	assert.Equal(t, 2, row.ConsecutiveFailures)
	require.NotNil(t, row.LastError)
	assert.Equal(t, tracker.ErrorClassUnknown, row.LastError.Kind)
	assert.NotContains(t, row.LastError.Message, "SECRET")
	assert.Zero(t, row.PendingWrites, "no outbox: nothing pending")

	src.store = fakeStoreStatus{store.SyncStatus{SyncedAt: now.Add(-31 * time.Minute), Interval: 10 * time.Minute}}
	row = src.row(now)
	assert.Equal(t, (30 * time.Minute).Milliseconds(), row.LagWarnAfterMs, "3 x the webhook safety-net interval")
	assert.True(t, row.Stale)

	src.store = fakeStoreStatus{store.SyncStatus{LastErr: raw, LastErrAt: now, Interval: time.Minute}}
	row = src.row(now)
	assert.Nil(t, row.LastPullAt)
	assert.True(t, row.Stale, "never pulled and failing")

	direct := &syncStatusSource{pollOKAt: func() time.Time { return now.Add(-30 * time.Second) }, pollInterval: 30 * time.Second}
	row = direct.row(now)
	assert.Equal(t, server.SyncModeDirect, row.Mode)
	assert.Equal(t, int64(30_000), *row.LagMs)
	assert.Nil(t, row.LastError)
}

// storeLikeTracker is a tracker that reports a store's sync status, as the
// tracker store (#113) in front of a Linear or GitHub adapter does.
type storeLikeTracker struct {
	*tracker.MemoryTracker
	fakeStoreStatus
}

// TestSnapshotCarriesSyncStatus pins the wiring (#117): buildSnapFunc puts
// the sync status on the snapshot as `syncStatus`, in store mode when the
// tracker is the store, with the outbox's pending writes.
func TestSnapshotCarriesSyncStatus(t *testing.T) {
	cfg := &config.Config{Tracker: config.TrackerConfig{
		ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}, Outbox: true, PushIntervalMs: 10_000,
	}}
	pulled := time.Now().Add(-2 * time.Minute)
	tr := storeLikeTracker{
		MemoryTracker:   tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		fakeStoreStatus: fakeStoreStatus{store.SyncStatus{SyncedAt: pulled, Interval: time.Minute}},
	}
	orch := orchestrator.New(cfg, tr, &agenttest.FakeRunner{}, nil)
	ob, err := outbox.New("")
	require.NoError(t, err)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "1", Identifier: "ENG-1", Body: "x", Urgent: true}))

	got := buildSnapFunc(orch, tr, cfg, "sess-1", logbuffer.New(), t.TempDir()+"/WORKFLOW.md", nil, ob)()
	require.NotNil(t, got.SyncStatus)
	assert.Equal(t, server.SyncModeStore, got.SyncStatus.Mode)
	require.NotNil(t, got.SyncStatus.LastPullAt)
	assert.True(t, got.SyncStatus.LastPullAt.Equal(pulled))
	assert.Equal(t, 1, got.SyncStatus.PendingWrites)
	assert.Equal(t, 1, got.SyncStatus.PendingUrgent)
	assert.Equal(t, int64(10_000), got.SyncStatus.PushIntervalMs)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	sync, ok := wire["syncStatus"].(map[string]any)
	require.True(t, ok, "wire key must be syncStatus (web/src/types/schemas.ts)")
	assert.Equal(t, "store", sync["mode"])
	assert.InDelta(t, 1, sync["pendingWrites"], 0)

	direct := buildSnapFunc(orch, tr.MemoryTracker, cfg, "sess-1", logbuffer.New(), t.TempDir()+"/WORKFLOW.md", nil, ob)()
	assert.Equal(t, server.SyncModeDirect, direct.SyncStatus.Mode)
}
