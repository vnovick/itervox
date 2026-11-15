package orchestrator

import "github.com/vnovick/itervox/internal/sysmon"

// RunMonitor is the system-visibility store (internal/sysmon.Monitor) as
// the orchestrator sees it. It is NOT orchestrator State: it is a
// separately locked store that learns process groups from procgroup and is
// read by the snapshot builder and the HTTP handlers. The orchestrator only
// names runs and closes them:
//   - AttachRun, from the worker goroutine, once the run's ID (at start)
//     and its workspace (once ready) are known;
//   - EndRun, from the event loop in recordHistory: it takes the store's
//     lock briefly, does no I/O, and returns the run's resource summary,
//     which history keeps (CompletedRun.Resources).
type RunMonitor interface {
	AttachRun(identifier, runID, workspace string, remote bool)
	EndRun(identifier string) (sysmon.RunSummary, bool)
}

// SetRunMonitor installs the run monitor. Must be called before Run; nil
// (the default) turns the calls off.
func (o *Orchestrator) SetRunMonitor(m RunMonitor) {
	o.runMonitor = m
}

// attachRunMonitor names the worker's run on the monitor. Worker goroutine;
// a no-op without a monitor or a run ID.
func (o *Orchestrator) attachRunMonitor(identifier, runID, workspace, workerHost string) {
	if o.runMonitor == nil || runID == "" {
		return
	}
	o.runMonitor.AttachRun(identifier, runID, workspace, workerHost != "")
}

// endRunMonitor closes identifier's run on the monitor and returns its
// summary (nil without one). Event loop only.
func (o *Orchestrator) endRunMonitor(identifier string) *sysmon.RunSummary {
	if o.runMonitor == nil {
		return nil
	}
	sum, ok := o.runMonitor.EndRun(identifier)
	if !ok {
		return nil
	}
	return &sum
}
