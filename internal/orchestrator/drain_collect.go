package orchestrator

import (
	"context"
	"log/slog"
	"time"
)

// M4-close D1 — the event loop outlives its workers on a forced stop.
//
// cmd/itervox forces a stop (drain grace expired, a second signal, or a
// reload's grace expiring) by cancelling the loop's ctx. Every worker context
// derives from it, so the same cancel SIGKILLs the agent process groups. The
// loop used to exit at once, and each worker's EventWorkerExited was then
// dropped ("exit event dropped (orchestrator exited)"): the killed turn left
// no history row, no retry, no pause, no .partial.md handoff rename.
//
// collectExitsAfterCancel runs after the main loop breaks on ctx and before
// the CORE-038 flush. It admits nothing (State.Draining is forced on, so
// dispatch, reviewers, automations and resumes all refuse) and handles only
// EventWorkerExited; every other event is dropped exactly as it was before,
// preserving the "no trailing mutation after cancel" contract the loop's
// ctx pre-check documents. It ends when no worker is running or the shared
// deadline passes. deliverExit keys off loopExited (closed after this phase),
// not the Run ctx, so the workers' sends reach this phase.

// minWorkerJoinAfterCollect is the smallest join window left after the exit
// collection: a worker that has just delivered its exit still has to return.
const minWorkerJoinAfterCollect = time.Second

func (o *Orchestrator) collectExitsAfterCancel(ctx context.Context, state State, deadline time.Time) State {
	if len(state.Running) == 0 {
		return state
	}
	if !state.Draining {
		state.Draining = true
		o.draining.Store(true)
	}
	o.stoppingAfterCancel = true
	defer func() { o.stoppingAfterCancel = false }()
	hctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	slog.Info("orchestrator: stopping — collecting exits of cancelled workers",
		"running", len(state.Running), "deadline", time.Until(deadline).Round(time.Millisecond).String())
	for len(state.Running) > 0 {
		select {
		case ev := <-o.events:
			if ev.Type != EventWorkerExited {
				slog.Debug("orchestrator: event dropped while stopping", "event", ev.Type, "identifier", ev.Identifier)
				continue
			}
			state = o.handleEvent(hctx, state, ev)
			o.storeSnap(state)
		case <-hctx.Done():
			ids := make([]string, 0, len(state.Running))
			for _, r := range state.Running {
				ids = append(ids, r.Issue.Identifier)
			}
			slog.Warn("orchestrator: stopped before every cancelled worker reported its exit",
				"identifiers", ids)
			return state
		}
	}
	return state
}

// loopExitedCh returns the channel closed once the loop (including the exit
// collection) stops reading o.events; nil before Run.
func (o *Orchestrator) loopExitedCh() <-chan struct{} {
	if p := o.loopExited.Load(); p != nil {
		return *p
	}
	return nil
}
