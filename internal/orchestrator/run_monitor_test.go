package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/sysmon"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeRunMonitor records the orchestrator's calls. AttachRun runs on the
// worker goroutine, so the fields are locked.
type fakeRunMonitor struct {
	mu       sync.Mutex
	attached []string // identifier|runID|workspace|remote
	ended    []string
	summary  sysmon.RunSummary
	known    bool
}

func (f *fakeRunMonitor) AttachRun(identifier, runID, workspace string, remote bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := "local"
	if remote {
		r = "remote"
	}
	f.attached = append(f.attached, identifier+"|"+runID+"|"+workspace+"|"+r)
}

func (f *fakeRunMonitor) EndRun(identifier string) (sysmon.RunSummary, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, identifier)
	return f.summary, f.known
}

// TestRecordHistoryKeepsRunMonitorSummary: recording a finished run closes
// it on the monitor and keeps the summary on the history entry; without a
// monitor run (or without a monitor) the entry has none.
func TestRecordHistoryKeepsRunMonitorSummary(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T"}
	live := &RunEntry{Issue: issue, StartedAt: time.Now().Add(-time.Minute), RunID: "run-1"}

	o := &Orchestrator{}
	o.recordHistory(live, issue, time.Now(), "succeeded")
	require.Len(t, o.RunHistory(), 1)
	assert.Nil(t, o.RunHistory()[0].Resources, "no monitor installed")

	mon := &fakeRunMonitor{known: true, summary: sysmon.RunSummary{PeakRSSBytes: 512 << 20, CPUSeconds: 12.5, PeakProcesses: 4, Commands: 7}}
	o = &Orchestrator{}
	o.SetRunMonitor(mon)
	o.recordHistory(live, issue, time.Now(), "succeeded")
	hist := o.RunHistory()
	require.Len(t, hist, 1)
	require.NotNil(t, hist[0].Resources)
	assert.Equal(t, mon.summary, *hist[0].Resources)
	assert.Equal(t, []string{"ENG-1"}, mon.ended)

	mon.known = false
	o.completedRuns = nil
	o.recordHistory(live, issue, time.Now(), "failed")
	assert.Nil(t, o.RunHistory()[0].Resources, "the monitor never saw a local process of this run")
}

// TestWorkerNamesItsRunOnTheMonitor: the worker names its run at start
// (run ID, no workspace yet) and again once the workspace is ready, so the
// monitor attributes the run's process groups and classifies its writes.
func TestWorkerNamesItsRunOnTheMonitor(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	ws := t.TempDir()
	o := New(cfg, mt, &isolationRecordingRunner{}, &stubWorkspaceProvider{path: ws})
	o.logBuf = logbuffer.New()
	mon := &fakeRunMonitor{}
	o.SetRunMonitor(mon)
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	ctx := context.WithValue(t.Context(), runHandleKey{}, &runHandle{id: "run-7"})
	o.runWorker(ctx, issue, 0, "", "claude", "claude", "", true, nil, nil, nil)

	mon.mu.Lock()
	defer mon.mu.Unlock()
	require.Len(t, mon.attached, 2)
	assert.Equal(t, "ENG-1|run-7||local", mon.attached[0])
	assert.Equal(t, "ENG-1|run-7|"+ws+"|local", mon.attached[1])
}
