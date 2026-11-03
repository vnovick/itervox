package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/procgroup"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/sysmon"
)

// sysmon_wire.go — system visibility: the run monitor (internal/sysmon) per
// WORKFLOW.md generation. It observes every local process group next to the
// CORE-042 ledger, is named and closed by the orchestrator (SetRunMonitor),
// samples on its own goroutine, and is read by the snapshot decorator, the
// run context, the activity endpoint and /metrics. None of it is
// orchestrator State and none of it takes cfgMu.

const gib = 1 << 30

// activityDir is where run activity logs are kept: runtime state under
// .itervox/runtime/ (gitignored by ensureItervoxGitignore).
func activityDir(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "runtime", "activity")
}

// runMonitorWiring is one generation's monitor and the hooks bound to it
// once the HTTP server exists.
type runMonitorWiring struct {
	mon    *sysmon.Monitor
	notify atomic.Pointer[func()]
	guard  *diskPauseGuard
}

// startRunMonitor builds the generation's monitor from cfg (read-only
// fields), installs it as a process-group observer and on orch, and starts
// its sampler. The returned stop function removes the observer; the sampler
// ends with ctx.
func startRunMonitor(ctx context.Context, cfg *config.Config, workflowPath string, orch *orchestrator.Orchestrator) (*runMonitorWiring, func()) {
	w := &runMonitorWiring{guard: &diskPauseGuard{pauseBytes: uint64(cfg.Workspace.DiskPauseFreeGB) * gib, orch: orch}} //nolint:gosec // non-negative by parse
	w.mon = sysmon.New(sysmon.Options{
		Resources:      cfg.Agent.ResourceSampling,
		Mode:           cfg.Agent.ActivityAudit,
		Dir:            activityDir(workflowPath),
		DiskPath:       cfg.Workspace.Root,
		DiskWarnBytes:  uint64(cfg.Workspace.DiskWarnFreeGB) * gib,  //nolint:gosec // non-negative by parse
		DiskPauseBytes: uint64(cfg.Workspace.DiskPauseFreeGB) * gib, //nolint:gosec // non-negative by parse
		OnMachine: func(st sysmon.MachineStatus, changed bool) {
			w.guard.observe(ctx, st)
			if changed {
				if f := w.notify.Load(); f != nil {
					(*f)()
				}
			}
		},
	})
	remove := procgroup.AddObserver(w.mon)
	orch.SetRunMonitor(w.mon)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer failFastOnPanic("run-monitor")
		w.mon.Run(ctx)
	}()
	return w, func() {
		remove()
		// A reload cancels ctx before run returns: let the sampler save
		// its activity logs before the next generation's monitor opens
		// the same directory.
		if ctx.Err() != nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// bindNotify makes a disk-threshold crossing push a fresh snapshot to the
// dashboards.
func (w *runMonitorWiring) bindNotify(f func()) { w.notify.Store(&f) }

// lowDiskPausePrefix starts the reason of a pause the disk guard set, so
// the guard resumes only its own pause, never an operator's.
const lowDiskPausePrefix = "Low disk space"

// diskPauseResumeMargin is how far above workspace.disk_pause_free_gb free
// space must climb before the guard resumes, so a volume hovering at the
// threshold does not flap.
const diskPauseResumeMargin = gib / 2

// diskPauseGuard pauses all dispatch (#213's pause, so every admission path
// holds) while free space is below workspace.disk_pause_free_gb, and
// resumes once it is back above it plus a margin.
type diskPauseGuard struct {
	pauseBytes uint64
	orch       interface {
		Snapshot() orchestrator.State
		PauseDispatch(ctx context.Context, reason string, until time.Time) (orchestrator.DispatchPause, error)
		ResumeDispatch(ctx context.Context) (orchestrator.DispatchPause, error)
	}
	busy atomic.Bool
}

// observe runs on the monitor's sampler goroutine; the pause round-trip
// to the event loop runs on its own goroutine, one at a time.
func (g *diskPauseGuard) observe(ctx context.Context, st sysmon.MachineStatus) {
	if st.Disk == nil {
		return
	}
	cur := g.orch.Snapshot().DispatchPaused
	ours := cur.Active && strings.HasPrefix(cur.Reason, lowDiskPausePrefix)
	var act func(context.Context) error
	switch {
	case g.pauseBytes > 0 && st.DiskCritical && !cur.Active:
		reason := fmt.Sprintf("%s: %s free on the workspace volume (workspace.disk_pause_free_gb: %d)",
			lowDiskPausePrefix, fmtGB(st.Disk.FreeBytes), g.pauseBytes/gib)
		act = func(c context.Context) error {
			_, err := g.orch.PauseDispatch(c, reason, time.Time{})
			return err
		}
	// Its own pause (also one persisted by an earlier run) ends once free
	// space recovers, or once the threshold is turned off.
	case ours && (g.pauseBytes == 0 || st.Disk.FreeBytes >= g.pauseBytes+diskPauseResumeMargin):
		act = func(c context.Context) error {
			_, err := g.orch.ResumeDispatch(c)
			return err
		}
	default:
		return
	}
	if !g.busy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer failFastOnPanic("disk-pause-guard")
		defer g.busy.Store(false)
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The first machine read comes before the event loop runs (the
		// monitor starts first): retry until the loop answers.
		for {
			err := act(c)
			if err == nil {
				return
			}
			select {
			case <-c.Done():
				slog.Warn("sysmon: low-disk dispatch pause change failed", "error", err)
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
}

func fmtGB(b uint64) string {
	return fmt.Sprintf("%.1f GB", float64(b)/gib)
}

// withSystemSnapshot decorates the dashboard snapshot with the monitor's
// data: each running row's latest sample, the system block and the
// capability. A nil monitor returns snap unchanged.
func withSystemSnapshot(snap func() server.StateSnapshot, mon *sysmon.Monitor) func() server.StateSnapshot {
	if mon == nil {
		return snap
	}
	return func() server.StateSnapshot {
		s := snap()
		live := mon.LiveUsage()
		for i := range s.Running {
			if u, ok := live[s.Running[i].Identifier]; ok && u.Samples > 0 {
				s.Running[i].Resources = &u
			}
		}
		sys := mon.System()
		s.System = &sys
		if !slices.Contains(s.Capabilities, server.CapabilitySystemVisibility) {
			s.Capabilities = append(slices.Clone(s.Capabilities), server.CapabilitySystemVisibility)
		}
		return s
	}
}

// runUsageFor returns the monitor's usage of identifier's run runID (the
// live run, or the latest finished one the monitor still holds), or nil.
func runUsageFor(mon *sysmon.Monitor, identifier, runID string) *sysmon.RunUsage {
	if mon == nil {
		return nil
	}
	u, ok := mon.Usage(identifier)
	if !ok || u.Samples == 0 || (runID != "" && u.RunID != "" && u.RunID != runID) {
		return nil
	}
	return &u
}

// addSystemMetrics adds the monitor's gauges to a metrics view.
func addSystemMetrics(v *metrics.View, mon *sysmon.Monitor) {
	if mon == nil {
		return
	}
	sys := mon.System()
	m := sys.Machine
	v.System = &metrics.SystemView{
		LoadKnown: m.LoadKnown, Load1: m.Load1, MemTotalBytes: m.MemTotalBytes, MemAvailableBytes: m.MemAvailableBytes,
		DiskKnown: m.Disk != nil, DiskLow: m.DiskLow,
	}
	if m.Disk != nil {
		v.System.DiskFreeBytes = m.Disk.FreeBytes
	}
	for id, u := range mon.LiveUsage() {
		if u.Samples == 0 {
			continue
		}
		v.System.Runs = append(v.System.Runs, metrics.RunGauge{Identifier: id, CPUPercent: u.CPUPercent,
			RSSBytes: u.RSSBytes, Processes: u.Processes})
	}
	slices.SortFunc(v.System.Runs, func(a, b metrics.RunGauge) int { return strings.Compare(a.Identifier, b.Identifier) })
}
