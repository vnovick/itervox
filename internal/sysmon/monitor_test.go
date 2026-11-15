package sysmon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMonitorRunLifecycle: groups attach to the identifier's open run; a
// new run ID closes the previous run that was never ended; EndRun moves
// the run to the finished list; unlabelled groups are ignored; a run whose
// groups all exited without an EndRun is closed after orphanRunTTL.
func TestMonitorRunLifecycle(t *testing.T) {
	now := time.Unix(10_000, 0)
	m := New(Options{Mode: ModeSampled})
	m.now = func() time.Time { return now }

	m.GroupStarted(500, 500, "")
	assert.Empty(t, m.LiveUsage(), "a group without a label is not a run")

	m.AttachRun("ENG-1", "run-1", "", false)
	m.GroupStarted(500, 500, "ENG-1")
	m.AttachRun("ENG-1", "run-1", "/ws/ENG-1", false)
	m.mu.Lock()
	e := m.runs["ENG-1"]
	assert.Equal(t, "/ws/ENG-1", e.workspace)
	assert.True(t, e.pgids[500])
	m.mu.Unlock()

	m.GroupExited(500)
	m.AttachRun("ENG-1", "run-2", "", true) // run-1 ended without EndRun
	u, ok := m.Usage("ENG-1")
	require.True(t, ok)
	assert.Equal(t, "run-2", u.RunID)
	assert.True(t, u.Remote)
	m.mu.Lock()
	require.Len(t, m.finished, 1)
	assert.Equal(t, "run-1", m.finished[0].runID)
	m.mu.Unlock()

	sum, ok := m.EndRun("ENG-1")
	require.True(t, ok)
	assert.Equal(t, ModeSampled, sum.ActivityMode)
	_, ok = m.EndRun("ENG-1")
	assert.False(t, ok, "already ended")
	assert.Empty(t, m.LiveUsage())

	// A hook after the run: a new open run with no worker; it is reaped.
	m.GroupStarted(600, 600, "ENG-1")
	m.GroupExited(600)
	m.reapOrphans()
	assert.Len(t, m.LiveUsage(), 1, "not yet")
	now = now.Add(orphanRunTTL + time.Second)
	m.reapOrphans()
	assert.Empty(t, m.LiveUsage())

	// The finished list is bounded.
	for i := range finishedRunsKept + 3 {
		m.AttachRun("X", string(rune('a'+i)), "", false)
		m.EndRun("X")
	}
	m.mu.Lock()
	assert.Len(t, m.finished, finishedRunsKept)
	assert.LessOrEqual(t, len(m.slots), finishedRunsKept, "slots of dropped runs are released")
	m.mu.Unlock()
}

// TestMonitorActivityNotFound: no run, nothing stored.
func TestMonitorActivityNotFound(t *testing.T) {
	m := New(Options{Mode: ModeSampled, Dir: t.TempDir()})
	_, err := m.Activity("NOPE-1", "")
	assert.ErrorIs(t, err, ErrActivityNotFound)
	m.AttachRun("ENG-1", "run-1", "", false)
	_, err = m.Activity("ENG-1", "other-run")
	assert.ErrorIs(t, err, ErrActivityNotFound, "a run ID that is neither live, kept nor stored")
	r, err := m.Activity("ENG-1", "")
	require.NoError(t, err)
	assert.True(t, r.Live)
	assert.Equal(t, []ActivityRunRef{{RunID: "run-1", StartedAt: r.StartedAt, Live: true}}, r.Runs)
}

// TestPreciseEventsLandOnTheirRun: events are routed by slot; an unknown
// slot is dropped; paths under /proc are not files.
func TestPreciseEventsLandOnTheirRun(t *testing.T) {
	m := New(Options{Mode: ModePrecise})
	m.procRoot = t.TempDir() // no /proc entries: names come from the event
	m.AttachRun("ENG-1", "run-1", "/ws", false)
	m.mu.Lock()
	slot := m.runs["ENG-1"].slot
	m.mu.Unlock()
	m.preciseEvent(preciseEvent{Kind: preciseExec, PID: 1, Slot: slot, Path: "/usr/bin/git"})
	m.preciseEvent(preciseEvent{Kind: preciseOpen, PID: 1, Slot: slot, Path: "/tmp/out.txt"})
	m.preciseEvent(preciseEvent{Kind: preciseOpen, PID: 1, Slot: slot, Path: "/proc/self/status"})
	m.preciseEvent(preciseEvent{Kind: preciseConnect, PID: 1, Slot: slot, Protocol: "inet", Remote: "1.2.3.4:443"})
	m.preciseEvent(preciseEvent{Kind: preciseExec, PID: 1, Slot: slot + 99, Path: "/usr/bin/curl"})
	r, err := m.Activity("ENG-1", "")
	require.NoError(t, err)
	require.Len(t, r.Commands, 1)
	assert.Equal(t, "git", r.Commands[0].Name)
	require.Len(t, r.Files, 1)
	assert.True(t, r.Files[0].Write)
	assert.True(t, r.Files[0].OutsideWorkspace)
	require.Len(t, r.Connections, 1)
	assert.Equal(t, "1.2.3.4:443", r.Connections[0].Remote)
}
