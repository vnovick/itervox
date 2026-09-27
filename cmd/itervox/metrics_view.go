package main

import (
	"net/http"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// metricsViewFunc returns the collector's view source for GET /metrics
// (CORE-045). It is built ONLY from cfgMu-free reads:
//
//   - orch.Snapshot() — snapMu plus the issue-override RLocks; the State copy
//     carries Running, RetryAttempts, AutomationQueue, MaxConcurrentAgents
//     (the tick's snapshot of the runtime value), DispatchPressure,
//     LastTrackerError, PersistWriteErrors and TransportFailureCount;
//   - orch.Readiness() — atomics;
//   - ob.Snapshot() — the outbox's own mutex;
//   - tracker.SharedRateLimitGate().OpenUntil — the gate's own mutex.
//
// It must NOT reuse buildSnapFunc, whose getters (MaxWorkers, ReviewerCfg,
// AutomationsCfg, …) take cfgMu: a scrape would then queue behind every
// settings save. TestMetricsCollectorDoesNotTakeCfgMu holds cfgMu and
// scrapes. adapter is cfg.Tracker.Kind (read-only after startup).
func metricsViewFunc(orch *orchestrator.Orchestrator, ob *outbox.Outbox, adapter string) func() metrics.View {
	return func() metrics.View {
		var entries []outbox.Entry
		if ob != nil {
			entries = ob.Snapshot()
		}
		var until time.Time
		if u, open := tracker.SharedRateLimitGate().OpenUntil(adapter); open {
			until = u
		}
		return buildMetricsView(orch.Snapshot(), orch.Readiness(), entries, adapter, until)
	}
}

// buildMetricsView is the pure mapping behind metricsViewFunc.
func buildMetricsView(s orchestrator.State, r orchestrator.Readiness, entries []outbox.Entry, adapter string, rateLimitedUntil time.Time) metrics.View {
	v := metrics.View{
		TrackerAdapter:          adapter,
		WorkersRunning:          len(s.Running),
		MaxConcurrentAgents:     s.MaxConcurrentAgents,
		RetryQueueLength:        len(s.RetryAttempts),
		AutomationQueueLength:   len(s.AutomationQueue),
		InputRequired:           len(s.InputRequiredIssues),
		Paused:                  len(s.PausedIdentifiers),
		PersistWriteErrors:      s.PersistWriteErrors,
		TransportFailures:       s.TransportFailureCount,
		DispatchTicksObserved:   s.DispatchPressure.TicksObserved,
		DispatchTicksSlotBound:  s.DispatchPressure.TicksSlotBound,
		DispatchTicksDepBound:   s.DispatchPressure.TicksDependencyBound,
		DispatchEligibleWaiting: s.DispatchPressure.EligibleWaiting,
		DispatchBlockedByDep:    s.DispatchPressure.BlockedByDependency,
		OutboxEntries:           len(entries),
		TrackerRateLimitedUntil: rateLimitedUntil,
		TrackerPollFailures:     s.ConsecutivePollFailures,
		LastTrackerErrorAt:      s.LastTrackerError.At,
		LastTrackerErrorKind:    s.LastTrackerError.Kind,
		LoopLastIdle:            r.LastLoopIdle,
	}
	for _, e := range entries {
		if e.Degraded() {
			v.OutboxDegraded++
		}
		if !e.RateLimitedUntil.IsZero() {
			v.OutboxRateLimited++
		}
	}
	return v
}

// newMetricsHandler is the GET /metrics handler cmd/itervox mounts when
// server.metrics.enabled is true.
func newMetricsHandler(orch *orchestrator.Orchestrator, ob *outbox.Outbox, adapter string) http.Handler {
	preinitMetrics(adapter)
	return metrics.Handler(metricsViewFunc(orch, ob, adapter))
}

// preinitMetrics exports the bounded label sets at 0 so rate() works from
// the first scrape.
func preinitMetrics(adapter string) {
	metrics.PreinitWorkerExitReasons(
		string(orchestrator.TerminalSucceeded),
		string(orchestrator.TerminalFailed),
		string(orchestrator.TerminalStalled),
		string(orchestrator.TerminalInputRequired),
		string(orchestrator.TerminalRateLimited),
		string(orchestrator.TerminalCanceledByReconciliation),
	)
	metrics.PreinitTrackerAdapter(adapter)
	metrics.PreinitClientErrors()
}
