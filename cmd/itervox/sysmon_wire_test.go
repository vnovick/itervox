package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/sysmon"
)

// fakePauser is the orchestrator's pause surface for diskPauseGuard.
type fakePauser struct {
	mu sync.Mutex
	// notRunning fails that many PauseDispatch calls, as before Run.
	notRunning int
	pause      orchestrator.DispatchPause
	pauses     []string
	resumes    int
}

func (f *fakePauser) Snapshot() orchestrator.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return orchestrator.State{DispatchPaused: f.pause}
}

func (f *fakePauser) PauseDispatch(_ context.Context, reason string, _ time.Time) (orchestrator.DispatchPause, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notRunning > 0 {
		f.notRunning--
		return orchestrator.DispatchPause{}, errors.New("orchestrator: event loop is not running")
	}
	f.pause = orchestrator.DispatchPause{Active: true, Reason: reason}
	f.pauses = append(f.pauses, reason)
	return f.pause, nil
}

func (f *fakePauser) ResumeDispatch(context.Context) (orchestrator.DispatchPause, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pause = orchestrator.DispatchPause{}
	f.resumes++
	return f.pause, nil
}

func (f *fakePauser) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pauses), f.resumes
}

func waitCounts(t *testing.T, f *fakePauser, pauses, resumes int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, r := f.counts()
		if p == pauses && r == resumes {
			return
		}
		require.True(t, time.Now().Before(deadline), "pauses=%d resumes=%d, want %d/%d", p, r, pauses, resumes)
		time.Sleep(5 * time.Millisecond)
	}
}

func diskAt(freeGB float64, pauseGB uint64) sysmon.MachineStatus {
	st := sysmon.MachineStatus{Disk: &sysmon.DiskStatus{FreeBytes: uint64(freeGB * gib), TotalBytes: 100 * gib}}
	st.DiskCritical = pauseGB > 0 && st.Disk.FreeBytes < pauseGB*gib
	return st
}

// TestDiskPauseGuardPausesAndResumesOnlyItsOwn: below
// workspace.disk_pause_free_gb the guard pauses all dispatch; it resumes
// only its own pause, only once free space is back above the threshold
// plus the margin, and never touches an operator's pause.
func TestDiskPauseGuardPausesAndResumesOnlyItsOwn(t *testing.T) {
	ctx := t.Context()
	f := &fakePauser{}
	g := &diskPauseGuard{pauseBytes: 5 * gib, orch: f}

	g.observe(ctx, diskAt(10, 5))
	waitCounts(t, f, 0, 0)
	// At startup the first read comes before the event loop runs: the
	// guard retries until the pause lands.
	f.notRunning = 2
	g.observe(ctx, diskAt(4, 5))
	waitCounts(t, f, 1, 0)
	assert.Contains(t, f.pauses[0], lowDiskPausePrefix)
	assert.Contains(t, f.pauses[0], "4.0 GB free")

	g.observe(ctx, diskAt(5.2, 5)) // above the threshold, inside the margin
	waitCounts(t, f, 1, 0)
	g.observe(ctx, diskAt(6, 5))
	waitCounts(t, f, 1, 1)

	// An operator's pause is never resumed by the guard.
	f.mu.Lock()
	f.pause = orchestrator.DispatchPause{Active: true, Reason: "deploy freeze"}
	f.mu.Unlock()
	g.observe(ctx, diskAt(50, 5))
	g.observe(ctx, diskAt(1, 5))
	waitCounts(t, f, 1, 1)

	off := &diskPauseGuard{orch: f}
	off.observe(ctx, diskAt(0.1, 0))
	waitCounts(t, f, 1, 1)

	// Its own pause left by an earlier run ends once the threshold is
	// turned off, even with the disk still low.
	f.mu.Lock()
	f.pause = orchestrator.DispatchPause{Active: true, Reason: lowDiskPausePrefix + ": 0.1 GB free"}
	f.mu.Unlock()
	off.observe(ctx, diskAt(0.1, 0))
	waitCounts(t, f, 1, 2)
}

// TestWithSystemSnapshotAddsTheMonitor: the decorator adds the system
// block and the capability; running rows get resources only once sampled.
func TestWithSystemSnapshotAddsTheMonitor(t *testing.T) {
	base := func() server.StateSnapshot {
		return server.StateSnapshot{Running: []server.RunningRow{{Identifier: "ENG-1"}}, Capabilities: []string{"run_context"}}
	}
	assert.Nil(t, withSystemSnapshot(base, nil)().System, "no monitor, no system block")

	mon := sysmon.New(sysmon.Options{Mode: sysmon.ModeSampled})
	mon.AttachRun("ENG-1", "run-1", "", false)
	s := withSystemSnapshot(base, mon)()
	require.NotNil(t, s.System)
	assert.Equal(t, sysmon.ModeSampled, s.System.ActivityMode)
	assert.Contains(t, s.Capabilities, server.CapabilitySystemVisibility)
	assert.Contains(t, s.Capabilities, "run_context")
	assert.Nil(t, s.Running[0].Resources, "no sample yet")
	assert.Nil(t, runUsageFor(mon, "ENG-1", "run-1"), "no sample yet")
	assert.Nil(t, runUsageFor(nil, "ENG-1", ""))
}
