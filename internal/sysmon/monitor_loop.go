package sysmon

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"
)

// The Monitor's sampler goroutine: process-tree samples, machine reads,
// activity-log saves and orphan reaping. Only Run's goroutine calls these
// (tests call them directly).

// Run samples until ctx ends, then saves every unsaved activity log and
// detaches precise mode.
func (m *Monitor) Run(ctx context.Context) {
	if m.opts.Mode == ModePrecise {
		m.startPrecise()
	}
	defer m.stopPrecise()
	m.sampleMachine()
	t := time.NewTicker(m.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, e := range m.runs {
				m.closeLocked(e)
			}
			m.mu.Unlock()
			m.persist(true)
			return
		case <-t.C:
			m.tick()
		}
	}
}

// tick takes one sample of every run with a live process group, reads the
// machine every machineEvery ticks and saves activity logs.
func (m *Monitor) tick() {
	m.ticks++
	if m.ticks%machineEvery == 0 {
		m.sampleMachine()
	}
	m.sampleRuns()
	m.persist(false)
	m.reapOrphans()
}

type sampleTarget struct {
	e       *runEntry
	pgids   map[int]bool
	leaders map[int]bool
}

func (m *Monitor) sampleRuns() {
	if !m.opts.Resources && m.mode != ModeSampled {
		return
	}
	m.mu.Lock()
	targets := make([]sampleTarget, 0, len(m.runs))
	for _, e := range m.runs {
		if len(e.pgids) > 0 || len(e.leaders) > 0 {
			targets = append(targets, sampleTarget{e, maps.Clone(e.pgids), maps.Clone(e.leaders)})
		}
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	m.src.beginTick()
	procs, err := m.src.list()
	if err != nil {
		slog.Debug("sysmon: process list failed", "error", err)
		return
	}
	activity := m.mode == ModeSampled
	for _, t := range targets {
		idx := memberIndexes(procs, t.pgids, t.leaders)
		ms := make([]procInfo, 0, len(idx))
		for _, i := range idx {
			p := procs[i]
			m.src.detail(&p, activity)
			ms = append(ms, p)
		}
		m.observe(t.e, ms, activity)
	}
}

// observe folds one sample into e: usage, and in sampled mode the new
// processes, open files and connections.
func (m *Monitor) observe(e *runEntry, ms []procInfo, activity bool) {
	now := m.now()
	var fresh []bool
	if activity {
		fresh = make([]bool, len(ms))
		for i := range ms {
			_, known := e.sampler.prev[ms[i].key()]
			fresh[i] = !known
		}
	}
	m.mu.Lock()
	u := e.usage
	m.mu.Unlock()
	e.sampler.observe(now, ms, m.src.ioKnown(), &u)
	m.mu.Lock()
	defer m.mu.Unlock()
	u.Live = !e.closed
	u.RunID, u.Remote, u.FinishedAt = e.usage.RunID, e.usage.Remote, e.usage.FinishedAt
	if !m.opts.Resources {
		u = RunUsage{Identifier: u.Identifier, RunID: u.RunID, Live: u.Live, Remote: u.Remote, StartedAt: u.StartedAt, FinishedAt: u.FinishedAt}
	}
	e.usage = u
	e.peakCPU = max(e.peakCPU, u.CPUPercent)
	if !activity {
		return
	}
	for i := range ms {
		p := &ms[i]
		name, args := commandLine(p)
		if fresh[i] {
			e.activity.addCommand(now, p.PID, name, args)
		}
		for _, f := range p.Files {
			if fresh[i] || f.Write {
				e.activity.addFile(now, f.Path, f.Write, name)
			}
		}
		for _, c := range p.Conns {
			if !c.Listening() {
				e.activity.addConn(now, c.Protocol, c.Remote, c.State, name)
			}
		}
	}
}

// sampleMachine reads the machine status and reports a disk threshold
// crossing.
func (m *Monitor) sampleMachine() {
	st := readMachine(m.procRoot, m.opts.DiskPath, m.now())
	applyDiskThresholds(&st, m.opts.DiskWarnBytes, m.opts.DiskPauseBytes)
	m.mu.Lock()
	prev := m.machine
	m.machine = st
	m.mu.Unlock()
	if m.opts.OnMachine != nil {
		m.opts.OnMachine(st, prev.DiskLow != st.DiskLow || prev.DiskCritical != st.DiskCritical)
	}
}

// Machine returns the last machine status.
func (m *Monitor) Machine() MachineStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.machine
}

// persist saves the activity of finished runs not yet saved and of live
// runs that changed since their last save (at most every
// activityFlushEvery); all of them when final.
func (m *Monitor) persist(final bool) {
	if m.mode == ModeOff {
		return
	}
	now := m.now()
	m.mu.Lock()
	var reports []ActivityReport
	var closedSlots []uint32
	pending := slices.Collect(maps.Values(m.runs))
	pending = append(pending, m.finished...)
	for _, e := range pending {
		if e.runID == "" || e.saved {
			continue
		}
		due := e.closed || final || (e.activity.dirty && now.Sub(e.lastSaved) >= activityFlushEvery)
		if !due {
			continue
		}
		reports = append(reports, m.reportLocked(e))
		e.activity.dirty, e.lastSaved = false, now
		if e.closed {
			e.saved = true
			closedSlots = append(closedSlots, e.slot)
		}
	}
	m.mu.Unlock()
	for _, r := range reports {
		if err := m.store.save(r); err != nil {
			slog.Warn("sysmon: activity log not saved", "identifier", r.Identifier, "run_id", r.RunID, "error", err)
		}
	}
	m.tracerMu.Lock()
	if m.tracer != nil {
		for _, s := range closedSlots {
			m.tracer.untrack(s)
		}
	}
	m.tracerMu.Unlock()
}

// reapOrphans closes runs whose groups all exited long ago without an
// EndRun (a hook that ran after its run ended).
func (m *Monitor) reapOrphans() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.runs {
		if len(e.pgids) == 0 && !e.idleSince.IsZero() && now.Sub(e.idleSince) > orphanRunTTL {
			m.closeLocked(e)
		}
	}
}
