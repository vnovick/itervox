package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
)

// CORE-057 — graceful drain on SIGTERM/SIGINT and before an operator-edit
// reload.
//
// Shutdown: the first signal begins a drain (no new work is admitted, /ready
// answers 503 draining, in-flight turns keep running on an uncancelled
// context). The daemon then waits for the last turn to finish, the
// --shutdown-grace to expire, or a second signal. Only then does it cancel
// the generation — which SIGKILLs any remaining agent process group through
// cmd.Cancel — and wait for run() to return, so the event loop has consumed
// EventWorkerExited, flushed its ledgers (CORE-038) and joined its workers
// (CORE-026) before main returns. The process never exits with a live worker
// context.
//
// Operator edits to WORKFLOW.md (daemon self-writes are suppressed by the
// CORE-116 registry and never get here): drain, then reload. The same
// admission stop and the same --shutdown-grace bound apply; when the grace
// expires the remaining turns are cancelled and the reload proceeds. A signal
// during a reload drain turns it into a shutdown drain.

// generationStopBackstop bounds how long a SHUTDOWN waits for run() after
// cancelling the generation: the orchestrator's worker join grace (20 s,
// CORE-026) plus headroom for the server and flusher joins. A reload waits
// without a bound (a second generation must never overlap the first).
const generationStopBackstop = 30 * time.Second

// drainControl links main's reload loop to one run() generation.
type drainControl struct {
	beginOnce   sync.Once
	begin       chan struct{}
	drainedOnce sync.Once
	drained     chan struct{}
}

func newDrainControl() *drainControl {
	return &drainControl{begin: make(chan struct{}), drained: make(chan struct{})}
}

// Begin asks the generation to drain. Idempotent.
func (d *drainControl) Begin() { d.beginOnce.Do(func() { close(d.begin) }) }

// markDrained reports that no worker is left running. Idempotent.
func (d *drainControl) markDrained() { d.drainedOnce.Do(func() { close(d.drained) }) }

// forwardDrain runs inside run(): on Begin it asks the orchestrator to drain
// and relays the orchestrator's Drained() back to main. Exits with ctx.
func (d *drainControl) forwardDrain(ctx context.Context, orch *orchestrator.Orchestrator) {
	select {
	case <-d.begin:
	case <-ctx.Done():
		return
	}
	if err := orch.RequestDrain(); err != nil {
		slog.Warn("drain: orchestrator did not accept the drain request", "error", err)
		return
	}
	select {
	case <-orch.Drained():
		d.markDrained()
	case <-ctx.Done():
	}
}

// drainEnd is why awaitDrain returned.
type drainEnd int

const (
	drainEndDrained   drainEnd = iota // no worker left running
	drainEndGrace                     // the grace expired first
	drainEndSignal                    // a (further) signal arrived
	drainEndRunExited                 // run() returned on its own
)

// awaitDrain begins the drain and waits for its end. sig is set for
// drainEndSignal, runErr for drainEndRunExited.
func awaitDrain(dc *drainControl, grace time.Duration, sigCh <-chan os.Signal, runDone <-chan error) (end drainEnd, sig os.Signal, runErr error) {
	dc.Begin()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-dc.drained:
		return drainEndDrained, nil, nil
	case <-timer.C:
		return drainEndGrace, nil, nil
	case s := <-sigCh:
		return drainEndSignal, s, nil
	case err := <-runDone:
		return drainEndRunExited, nil, err
	}
}

// stopGeneration cancels the generation and waits for run() to return. A
// backstop > 0 bounds the wait (shutdown); a signal during the wait stops
// waiting at once. ok is false when run() had not returned.
func stopGeneration(cancel context.CancelFunc, runDone <-chan error, backstop time.Duration, sigCh <-chan os.Signal) (err error, ok bool) {
	cancel()
	var timeout <-chan time.Time
	if backstop > 0 {
		t := time.NewTimer(backstop)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case err = <-runDone:
		return err, true
	case <-timeout:
		slog.Warn("shutdown: generation did not stop within the backstop; exiting anyway", "backstop", backstop.String())
		return nil, false
	case s := <-sigCh:
		slog.Warn("shutdown: another signal while stopping; exiting now", "signal", s)
		return nil, false
	}
}

// shutdownDrain runs the shutdown sequence after the first signal (or after
// a signal that interrupted a reload drain) and returns once the daemon may
// exit. cancel is the top-level cancel (it also ends the generation).
func shutdownDrain(sig os.Signal, dc *drainControl, grace time.Duration, sigCh <-chan os.Signal, runDone <-chan error, cancel context.CancelFunc) {
	slog.Info("shutdown: draining — no new work admitted; waiting for in-flight turns to finish",
		"signal", sig, "grace", grace.String())
	end, sig2, _ := awaitDrain(dc, grace, sigCh, runDone)
	switch end {
	case drainEndRunExited:
		cancel()
		return
	case drainEndDrained:
		slog.Info("shutdown: drain complete, all in-flight turns finished")
	case drainEndGrace:
		slog.Warn("shutdown: drain grace expired, forcing stop — cancelling in-flight turns", "grace", grace.String())
	case drainEndSignal:
		slog.Warn("shutdown: received second signal, forcing immediate stop — cancelling in-flight turns", "signal", sig2)
	}
	if _, ok := stopGeneration(cancel, runDone, generationStopBackstop, sigCh); ok {
		slog.Info("shutdown: generation stopped, exiting")
	}
}

// reloadDrain drains before applying an operator edit to WORKFLOW.md. It
// returns the generation's exit error and shutdown=true when the daemon must
// exit instead of reloading (a signal arrived, or the top-level ctx ended).
func reloadDrain(ctx context.Context, dc *drainControl, grace time.Duration, sigCh <-chan os.Signal, runDone <-chan error, runCancel, cancel context.CancelFunc) (runErr error, shutdown bool) {
	slog.Info("reload: WORKFLOW.md changed — draining in-flight turns before applying it (no new work is admitted meanwhile)",
		"grace", grace.String())
	end, sig, err := awaitDrain(dc, grace, sigCh, runDone)
	switch end {
	case drainEndRunExited:
		runCancel()
		return err, ctx.Err() != nil
	case drainEndSignal:
		shutdownDrain(sig, dc, grace, sigCh, runDone, cancel)
		return nil, true
	case drainEndDrained:
		slog.Info("reload: drain complete, applying WORKFLOW.md")
	case drainEndGrace:
		slog.Warn("reload: drain grace expired, cancelling in-flight turns to apply WORKFLOW.md", "grace", grace.String())
	}
	runErr, ok := stopGeneration(runCancel, runDone, 0, sigCh)
	if !ok { // a signal while waiting for the generation: exit
		cancel()
		return nil, true
	}
	return runErr, ctx.Err() != nil
}

// waitBeforeReload is the pause between two generations (main's
// reloadDelay). It returns stop=true when a signal arrives or ctx ends
// during the wait — no generation is running then, so the daemon exits at
// once instead of starting another generation first (M4-close: this used to
// be a bare time.Sleep that ignored both).
func waitBeforeReload(ctx context.Context, d time.Duration, sigCh <-chan os.Signal) (stop bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return false
	case s := <-sigCh:
		slog.Info("shutdown: signal between generations, exiting", "signal", s)
		return true
	case <-ctx.Done():
		return true
	}
}
