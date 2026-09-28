package orchestrator

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// CORE-008 — panic containment for background goroutines.
//
// A panic in any goroutine other than the one that recovers it kills the whole
// daemon: main()'s deferred recover only guards the main goroutine. The
// helpers here give every bounded background task (tracker comment and state
// writes, workspace clears, discard transitions) a recover that logs through
// slog (the redacting handler, so the rotating log file records it), bumps a
// lock-free counter, and then runs a per-site onPanic that publishes the SAME
// terminal event the task would have sent on normal completion. Without that
// last step a recovered goroutine would leave event-loop state stuck — e.g. an
// identifier parked in State.DiscardingIdentifiers forever, because only
// EventDiscardComplete clears it.
//
// Invariants:
//   - Run() is never wrapped: the event loop stays fail-fast.
//   - onPanic never touches State and never reads cfgMu fields; it may only
//     send an OrchestratorEvent, through sendEventBounded.
//   - onPanic runs under its own recover (a panic inside it is logged and
//     counted, never re-raised) and must not block indefinitely.
//
// `make no-bare-go` (scripts/check-no-bare-go.sh) fails the build on any
// production `go` statement that neither defers RecoverGoroutine /
// failFastOnPanic nor appears on its reasoned allowlist.

// goroutinePanics counts panics recovered by RecoverGoroutine (including
// panics inside an onPanic callback). Lock-free; exposed for tests and for a
// future metrics export.
var goroutinePanics atomic.Int64

// onPanicSendBound caps how long onPanic may wait to hand its terminal event
// to a full event queue — the same 30s bound the normal discard path uses.
// A var so tests can shorten it.
var onPanicSendBound = 30 * time.Second

// RecoverGoroutine must be deferred DIRECTLY at the top of a goroutine body
// (`defer orchestrator.RecoverGoroutine(...)`) — recover() only stops a panic
// when called by the deferred function itself. On a panic it logs name,
// identifier, the panic value and the stack, bumps GoroutinePanicCount, and
// then runs onPanic (nil allowed for sites with nothing to reconcile, e.g. a
// one-shot tracker comment write).
func RecoverGoroutine(name, identifier string, onPanic func()) {
	r := recover()
	if r == nil {
		return
	}
	goroutinePanics.Add(1)
	metrics.GoroutinePanic()
	slog.Error("goroutine panic recovered",
		"goroutine", name,
		"identifier", identifier,
		"panic", r,
		"stack", string(debug.Stack()))
	runOnPanic(name, identifier, onPanic)
}

// runOnPanic runs onPanic under its own recover so a faulty reconcile callback
// can never re-crash the process it was meant to keep consistent.
func runOnPanic(name, identifier string, onPanic func()) {
	if onPanic == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			goroutinePanics.Add(1)
			metrics.GoroutinePanic()
			slog.Error("goroutine onPanic callback panicked",
				"goroutine", name,
				"identifier", identifier,
				"panic", r)
		}
	}()
	onPanic()
}

// goSafe launches fn on a new goroutine tracked by wg (Add(1) happens here,
// Done after onPanic has run) with RecoverGoroutine containment. Taking the
// WaitGroup as an argument means a call site cannot launch an untracked
// goroutine; TestEventLoopGoroutinesAreWaitgroupTracked checks that every
// goSafe call in the scanned files passes one of the orchestrator's joined
// WaitGroups.
func goSafe(wg *sync.WaitGroup, name, identifier string, fn func(), onPanic func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer RecoverGoroutine(name, identifier, onPanic)
		fn()
	}()
}

// sendEventBounded delivers ev to the event loop for an onPanic callback. It
// never blocks past onPanicSendBound and returns immediately once the
// orchestrator's Run context is done (Run has stopped reading o.events). The
// outcome — delivered, or dropped under shutdown/backpressure — is logged with
// the identifier so a stuck entry is diagnosable. Returns true on delivery.
func (o *Orchestrator) sendEventBounded(ev OrchestratorEvent, identifier string) bool {
	shutdown := context.Background()
	if p := o.runCtx.Load(); p != nil {
		shutdown = *p
	}
	timer := time.NewTimer(onPanicSendBound)
	defer timer.Stop()
	select {
	case o.events <- ev:
		slog.Info("orchestrator: panic reconcile event delivered",
			"event", ev.Type, "identifier", identifier)
		return true
	case <-shutdown.Done():
		slog.Warn("orchestrator: panic reconcile event dropped: shutting down, identifier may be stuck",
			"event", ev.Type, "identifier", identifier)
	case <-timer.C:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: panic reconcile event dropped: event queue full, identifier may be stuck",
			"event", ev.Type, "identifier", identifier, "bound", onPanicSendBound.String())
	}
	return false
}

// discardCompleteOnPanic is the onPanic for both async discard goroutines: it
// publishes the EventDiscardComplete the goroutine would have sent, so the
// event loop removes identifier from State.DiscardingIdentifiers and the issue
// becomes dispatchable again instead of staying "discarding" forever.
func (o *Orchestrator) discardCompleteOnPanic(identifier string, gen uint64) func() {
	return func() {
		o.sendEventBounded(OrchestratorEvent{Type: EventDiscardComplete, Identifier: identifier, DiscardGen: gen}, identifier)
	}
}
