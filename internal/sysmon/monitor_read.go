package sysmon

import (
	"runtime"
	"slices"
)

// The Monitor's read side: copies for the snapshot builder, the run
// context, the activity endpoint and /metrics. Any goroutine.

// Usage returns identifier's live run's usage, else its most recent
// finished run's (while kept in memory).
func (m *Monitor) Usage(identifier string) (RunUsage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.runs[identifier]; e != nil {
		return cloneUsage(e.usage), true
	}
	for i := len(m.finished) - 1; i >= 0; i-- {
		if e := m.finished[i]; e.identifier == identifier {
			return cloneUsage(e.usage), true
		}
	}
	return RunUsage{}, false
}

// LiveUsage returns the usage of every open run, by identifier.
func (m *Monitor) LiveUsage() map[string]RunUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]RunUsage, len(m.runs))
	for id, e := range m.runs {
		out[id] = cloneUsage(e.usage)
	}
	return out
}

func cloneUsage(u RunUsage) RunUsage {
	u.TopCommands = slices.Clone(u.TopCommands)
	return u
}

// System returns the machine status, the live runs' totals and the
// activity mode.
func (m *Monitor) System() SystemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := SystemStatus{Machine: m.machine, ActivityMode: m.mode, ActivityModeReason: m.modeReason, Platform: runtime.GOOS}
	for _, e := range m.runs {
		if e.usage.Samples == 0 {
			continue
		}
		st.Runs.Runs++
		st.Runs.CPUPercent = round1(st.Runs.CPUPercent + e.usage.CPUPercent)
		st.Runs.RSSBytes += e.usage.RSSBytes
		st.Runs.Processes += e.usage.Processes
	}
	return st
}

// Activity returns the activity of identifier's run runID, or of its
// latest run when runID is empty: the open run, else the newest finished
// one in memory, else the newest one stored.
func (m *Monitor) Activity(identifier, runID string) (ActivityReport, error) {
	m.mu.Lock()
	var pick *runEntry
	if e := m.runs[identifier]; e != nil && (runID == "" || e.runID == runID) {
		pick = e
	}
	for i := len(m.finished) - 1; pick == nil && i >= 0; i-- {
		if e := m.finished[i]; e.identifier == identifier && (runID == "" || e.runID == runID) {
			pick = e
		}
	}
	var r ActivityReport
	refs := m.runRefsLocked(identifier)
	if pick != nil {
		r = m.reportLocked(pick)
	}
	m.mu.Unlock()
	if pick == nil {
		stored := m.store.runs(identifier)
		if runID == "" && len(stored) > 0 {
			runID = stored[0].RunID
		}
		var err error
		if r, err = m.store.load(identifier, runID); err != nil {
			return ActivityReport{}, err
		}
	}
	r.Runs = mergeRunRefs(refs, m.store.runs(identifier))
	return r, nil
}

// runRefsLocked lists identifier's runs held in memory, newest first.
func (m *Monitor) runRefsLocked(identifier string) []ActivityRunRef {
	var refs []ActivityRunRef
	if e := m.runs[identifier]; e != nil && e.runID != "" {
		refs = append(refs, ActivityRunRef{RunID: e.runID, StartedAt: e.started, Live: true})
	}
	for i := len(m.finished) - 1; i >= 0; i-- {
		if e := m.finished[i]; e.identifier == identifier && e.runID != "" {
			refs = append(refs, ActivityRunRef{RunID: e.runID, StartedAt: e.started, FinishedAt: e.finishedAt})
		}
	}
	return refs
}

// mergeRunRefs adds the stored runs not already listed, newest first,
// keeping at most 20.
func mergeRunRefs(refs []ActivityRunRef, stored []activityIndexEntry) []ActivityRunRef {
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.RunID] = true
	}
	for _, s := range stored {
		if !seen[s.RunID] {
			refs = append(refs, ActivityRunRef{RunID: s.RunID, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt})
		}
	}
	slices.SortStableFunc(refs, func(a, b ActivityRunRef) int { return b.StartedAt.Compare(a.StartedAt) })
	return refs[:min(len(refs), 20)]
}

func (m *Monitor) reportLocked(e *runEntry) ActivityReport {
	support, notes := platformNotes(runtime.GOOS, m.mode, e.remote)
	r := ActivityReport{
		Identifier: e.identifier, RunID: e.runID, Live: !e.closed, Mode: m.mode, ModeReason: m.modeReason,
		Platform: runtime.GOOS, Supports: support, Notes: notes, Workspace: e.workspace, Remote: e.remote,
		StartedAt: e.started, FinishedAt: e.finishedAt,
	}
	e.activity.fill(&r, e.workspace)
	return r
}
