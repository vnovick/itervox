package sysmon

import (
	"log/slog"
	"sync"
	"time"
)

// Monitor bounds and cadences.
const (
	// finishedRunsKept is how many finished runs keep their usage and
	// activity in memory (older ones are read back from the store).
	finishedRunsKept = 32
	// machineEvery is how many sample ticks pass between machine reads.
	machineEvery = 5
	// activityFlushEvery bounds how often a live run's activity is saved.
	activityFlushEvery = 15 * time.Second
	// orphanRunTTL closes a run whose groups all exited and that no worker
	// claimed (a hook started after its run ended) or ended.
	orphanRunTTL = 30 * time.Minute
)

// Options configure a Monitor. Read once at construction.
type Options struct {
	// Resources turns per-run resource sampling on (agent.resource_sampling).
	Resources bool
	// Mode is the activity mode asked for: ModeOff, ModeSampled or
	// ModePrecise (agent.activity_audit).
	Mode string
	// Dir is where activity logs are kept; empty keeps them in memory only.
	Dir string
	// DiskPath is the workspace root whose volume's free space is watched.
	DiskPath string
	// DiskWarnBytes / DiskPauseBytes are the low-disk thresholds (0 = off).
	DiskWarnBytes  uint64
	DiskPauseBytes uint64
	// Interval overrides SampleInterval (tests).
	Interval time.Duration
	// OnMachine is called on the sampler goroutine after every machine
	// read, with the new status and whether DiskLow or DiskCritical
	// changed. Must not block for long.
	OnMachine func(st MachineStatus, changed bool)
}

// runEntry is one run the Monitor watches. Fields marked (sampler) are
// owned by the sampler goroutine; the rest are guarded by Monitor.mu.
type runEntry struct {
	identifier string
	runID      string
	workspace  string
	remote     bool
	slot       uint32
	pgids      map[int]bool
	leaders    map[int]bool
	started    time.Time
	idleSince  time.Time
	usage      RunUsage
	peakCPU    float64
	activity   *activityLog
	closed     bool
	finishedAt time.Time
	saved      bool
	lastSaved  time.Time
	sampler    *runSampler // (sampler)
}

// Monitor watches agent runs' process trees and the machine. See the
// package doc for its concurrency contract.
type Monitor struct {
	opts       Options
	src        processSource
	store      *activityStore
	procRoot   string
	now        func() time.Time
	mode       string
	modeReason string

	tracerMu sync.Mutex
	tracer   activityTracer
	// preciseNames caches each pid's display name and arguments for
	// precise events. Owned by the tracer's reader goroutine.
	preciseNames map[uint32][2]string

	mu       sync.Mutex
	runs     map[string]*runEntry // open runs by identifier
	slots    map[uint32]*runEntry
	finished []*runEntry // newest last
	nextSlot uint32
	machine  MachineStatus
	ticks    int
}

// New returns a Monitor for opts. Precise mode is not started until Run;
// until then Mode reports what was asked for.
func New(opts Options) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = SampleInterval
	}
	switch opts.Mode {
	case ModeOff, ModeSampled, ModePrecise:
	default:
		opts.Mode = ModeSampled
	}
	return &Monitor{
		opts: opts, src: newProcessSource(), store: newActivityStore(opts.Dir), procRoot: "/proc",
		now: time.Now, mode: opts.Mode, runs: map[string]*runEntry{}, slots: map[uint32]*runEntry{},
	}
}

// Mode returns the activity mode in effect and, when precise mode fell
// back, why.
func (m *Monitor) Mode() (mode, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode, m.modeReason
}

// openRunLocked returns identifier's open run, creating it.
func (m *Monitor) openRunLocked(identifier string) *runEntry {
	if e := m.runs[identifier]; e != nil {
		return e
	}
	m.nextSlot++
	now := m.now()
	e := &runEntry{identifier: identifier, slot: m.nextSlot, pgids: map[int]bool{}, leaders: map[int]bool{},
		started: now, activity: newActivityLog(), sampler: newRunSampler(now)}
	e.usage = RunUsage{Identifier: identifier, Live: true, StartedAt: now}
	m.runs[identifier] = e
	m.slots[e.slot] = e
	return e
}

// GroupStarted implements procgroup.Observer: a local process group of
// label's run started (the agent CLI of a turn, or a hook).
func (m *Monitor) GroupStarted(pgid, pid int, label string) {
	if label == "" {
		return
	}
	m.mu.Lock()
	e := m.openRunLocked(label)
	e.pgids[pgid] = true
	e.leaders[pid] = true
	e.idleSince = time.Time{}
	slot := e.slot
	m.mu.Unlock()
	m.tracerMu.Lock()
	if m.tracer != nil {
		if err := m.tracer.track(uint32(pid), slot); err != nil { //nolint:gosec // pids fit in uint32
			slog.Debug("sysmon: precise tracking failed", "identifier", label, "pid", pid, "error", err)
		}
	}
	m.tracerMu.Unlock()
}

// GroupExited implements procgroup.Observer.
func (m *Monitor) GroupExited(pgid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.runs {
		if e.pgids[pgid] {
			delete(e.pgids, pgid)
			delete(e.leaders, pgid)
			if len(e.pgids) == 0 {
				e.idleSince = m.now()
			}
		}
	}
}

// AttachRun names identifier's current run: its run ID, workspace and
// whether it runs on an SSH worker host. Called by the worker once its
// workspace is ready.
func (m *Monitor) AttachRun(identifier, runID, workspace string, remote bool) {
	if identifier == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.runs[identifier]; e != nil && e.runID != "" && e.runID != runID {
		m.closeLocked(e) // its run ended without an EndRun
	}
	e := m.openRunLocked(identifier)
	e.runID, e.remote = runID, remote
	if workspace != "" {
		e.workspace = workspace
	}
	e.usage.RunID, e.usage.Remote = runID, remote
	if len(e.pgids) == 0 && e.idleSince.IsZero() {
		e.idleSince = m.now()
	}
}

// EndRun closes identifier's run and returns its summary. Called by the
// orchestrator's event loop when the run is recorded in history: it takes
// the lock briefly and does no I/O (the sampler saves the activity log).
func (m *Monitor) EndRun(identifier string) (RunSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.runs[identifier]
	if e == nil {
		return RunSummary{}, false
	}
	m.closeLocked(e)
	return m.summaryLocked(e), true
}

func (m *Monitor) closeLocked(e *runEntry) {
	now := m.now()
	e.closed, e.finishedAt = true, now
	e.usage.Live, e.usage.FinishedAt = false, now
	delete(m.runs, e.identifier)
	m.finished = append(m.finished, e)
	if len(m.finished) > finishedRunsKept {
		old := m.finished[0]
		delete(m.slots, old.slot)
		m.finished = m.finished[1:]
	}
}

func (m *Monitor) summaryLocked(e *runEntry) RunSummary {
	cmds, written, conns := e.activity.counts()
	return RunSummary{
		CPUSeconds: e.usage.CPUSeconds, PeakCPUPercent: e.peakCPU, LastRSSBytes: e.usage.RSSBytes,
		PeakRSSBytes: e.usage.PeakRSSBytes, PeakProcesses: e.usage.PeakProcesses,
		DiskReadBytes: e.usage.DiskReadBytes, DiskWriteBytes: e.usage.DiskWriteBytes,
		Commands: cmds, FilesWritten: written, Connections: conns, ActivityMode: m.mode, Samples: e.usage.Samples,
	}
}
