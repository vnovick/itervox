package orchestrator

import (
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// ErrNoWorkerFailure is returned by AckFailures when the issue has no
// worker_failed / worker_stalled entry in the RecentFailures ring (CORE-175).
var ErrNoWorkerFailure = errors.New("orchestrator: issue has no recent worker failure")

// isAckableFailure reports whether f is an attention item the operator can
// acknowledge: a worker failure or stall of an issue.
func isAckableFailure(f FailureRecord) bool {
	return f.Identifier != "" && (f.Kind == FailureKindWorkerFailed || f.Kind == FailureKindWorkerStalled)
}

// AckFailures acknowledges identifier's worker failures that occurred at or
// before upTo (CORE-175). It checks the published snapshot (never State)
// for such a failure, then hands the ack to the event loop; a full event
// channel is ErrBusy. Allowed while draining: an ack starts no work.
// Safe from any goroutine.
func (o *Orchestrator) AckFailures(identifier string, upTo time.Time) error {
	found := false
	for _, f := range o.Snapshot().RecentFailures {
		if f.Identifier == identifier && isAckableFailure(f) {
			found = true
			break
		}
	}
	if !found {
		return ErrNoWorkerFailure
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventAckFailures, Identifier: identifier, AckUpTo: upTo}:
		return nil
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: failure-ack event channel full", "identifier", identifier)
		return ErrBusy
	}
}

// applyFailureAck records ack (keeping the latest upTo per identifier).
// Event loop only.
func applyFailureAck(state *State, identifier string, upTo time.Time) {
	if identifier == "" || upTo.IsZero() {
		return
	}
	if state.FailureAcks == nil {
		state.FailureAcks = make(map[string]time.Time)
	}
	if prev, ok := state.FailureAcks[identifier]; !ok || upTo.After(prev) {
		state.FailureAcks[identifier] = upTo
	}
	pruneFailureAcks(state)
}

// pruneFailureAcks drops acks for identifiers with no worker failure left in
// the RecentFailures ring — nothing remains for them to hide. Event loop only.
func pruneFailureAcks(state *State) {
	if len(state.FailureAcks) == 0 {
		return
	}
	live := make(map[string]struct{}, len(state.RecentFailures))
	for _, f := range state.RecentFailures {
		if isAckableFailure(f) {
			live[f.Identifier] = struct{}{}
		}
	}
	for id := range state.FailureAcks {
		if _, ok := live[id]; !ok {
			delete(state.FailureAcks, id)
		}
	}
}

// SeedFailureAcks installs the acks a previous run() generation left
// (M6-close V3), next to SeedRecentFailures. Must be called before Run; a
// call after Run started is ignored. Acks whose issue has no failure in the
// seeded ring are dropped at startup.
func (o *Orchestrator) SeedFailureAcks(acks map[string]time.Time) {
	if o.started.Load() {
		return
	}
	o.seedFailureAcks = maps.Clone(acks)
}
