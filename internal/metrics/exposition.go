package metrics

import (
	"bufio"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// View is the gauge-and-snapshot half of the metrics (see the package doc).
// cmd/itervox builds it from orchestrator.Snapshot(), the outbox snapshot and
// the tracker rate-limit gate; it must not take cfgMu.
type View struct {
	// TrackerAdapter labels the rate-limit gauge (cfg.Tracker.Kind).
	TrackerAdapter string

	WorkersRunning        int
	MaxConcurrentAgents   int
	RetryQueueLength      int
	AutomationQueueLength int
	InputRequired         int
	Paused                int

	// PersistWriteErrors counts failed ledger writes (CORE-038). Per
	// orchestrator generation: a WORKFLOW.md reload resets it, which
	// Prometheus' rate() treats as a counter reset.
	PersistWriteErrors int64
	// TransportFailures counts retry-exhausted agent runs classified as
	// transport failures. Per generation, like PersistWriteErrors.
	TransportFailures uint64

	// Dispatch pressure (session-scoped, per generation).
	DispatchTicksObserved   int64
	DispatchTicksSlotBound  int64
	DispatchTicksDepBound   int64
	DispatchEligibleWaiting int
	DispatchBlockedByDep    int

	// Write-ahead outbox.
	OutboxEntries     int
	OutboxDegraded    int
	OutboxRateLimited int

	// TrackerRateLimitedUntil is the tracker rate-limit gate's reset while
	// it is open, zero while closed.
	TrackerRateLimitedUntil time.Time
	// TrackerPollFailures is the current run of consecutive
	// non-rate-limited poll failures.
	TrackerPollFailures int
	// LastTrackerErrorAt / LastTrackerErrorKind mirror
	// State.LastTrackerError; zero At means none recorded.
	LastTrackerErrorAt   time.Time
	LastTrackerErrorKind string

	// LoopLastIdle is when the event loop last finished an iteration; zero
	// before it starts.
	LoopLastIdle time.Time
}

// ContentType is the Prometheus text exposition format, version 0.0.4.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves WriteText(view()) on GET/HEAD. It performs no auth: the
// caller mounts it behind the bearer-token middleware.
func Handler(view func() View) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		_ = WriteText(w, view())
	})
}

type writer struct {
	w   *bufio.Writer
	err error
}

func (x *writer) str(parts ...string) {
	for _, p := range parts {
		if x.err != nil {
			return
		}
		_, x.err = x.w.WriteString(p)
	}
}

func (x *writer) header(name, typ, help string) {
	x.str("# HELP ", name, " ", escapeHelp(help), "\n", "# TYPE ", name, " ", typ, "\n")
}

type label struct{ k, v string }

func (x *writer) sample(name string, value float64, labels ...label) {
	x.str(name)
	if len(labels) > 0 {
		x.str("{")
		for i, l := range labels {
			if i > 0 {
				x.str(",")
			}
			x.str(l.k, `="`, escapeLabel(l.v), `"`)
		}
		x.str("}")
	}
	x.str(" ", strconv.FormatFloat(value, 'g', -1, 64), "\n")
}

func (x *writer) gauge(name, help string, value float64, labels ...label) {
	x.header(name, "gauge", help)
	x.sample(name, value, labels...)
}

func (x *writer) counter(name, help string, value float64) {
	x.header(name, "counter", help)
	x.sample(name, value)
}

func (x *writer) counterVec(name, help string, c *counterVec) {
	x.header(name, "counter", help)
	for _, s := range c.samples() {
		labels := make([]label, len(c.labels))
		for i, k := range c.labels {
			labels[i] = label{k, s.values[i]}
		}
		x.sample(name, float64(s.value), labels...)
	}
}

func unixSeconds(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

// WriteText writes every family in the Prometheus text exposition format.
func WriteText(out io.Writer, v View) error {
	x := &writer{w: bufio.NewWriter(out)}

	x.gauge("itervox_workers_running", "Agent workers currently running.", float64(v.WorkersRunning))
	x.gauge("itervox_workers_max", "Configured agent.max_concurrent_agents.", float64(v.MaxConcurrentAgents))
	x.gauge("itervox_retry_queue_length", "Issues waiting in the retry queue.", float64(v.RetryQueueLength))
	x.gauge("itervox_automation_queue_length", "Entries in the automation queue.", float64(v.AutomationQueueLength))
	x.gauge("itervox_input_required", "Issues waiting for human input.", float64(v.InputRequired))
	x.gauge("itervox_paused_issues", "Issues paused by the daemon or an operator.", float64(v.Paused))

	x.counterVec("itervox_worker_exits_total", "Worker exits processed by the event loop, by terminal reason.", workerExits)
	x.counterVec("itervox_tracker_requests_total", "Tracker HTTP calls after in-call retries, by adapter and outcome.", trackerRequests)
	x.counter("itervox_goroutine_panics_total", "Panics recovered on daemon goroutines.", float64(goroutinePanics.Load()))
	x.counter("itervox_events_dropped_total", "Orchestrator events that could not be delivered to the event loop.", float64(eventsDropped.Load()))
	x.counterVec("itervox_client_errors_total", "Web dashboard error reports received on POST /api/v1/client-errors, by kind and outcome.", clientErrors)
	x.counter("itervox_persist_write_errors_total", "Failed runtime ledger writes (resets on WORKFLOW.md reload).", float64(v.PersistWriteErrors))
	x.counter("itervox_transport_failures_total", "Retry-exhausted agent runs classified as transport failures (resets on reload).", float64(v.TransportFailures))

	name := "itervox_dispatch_ticks_total"
	x.header(name, "counter", "Poll ticks observed by dispatch-pressure accounting, by binding constraint (resets on reload).")
	x.sample(name, float64(v.DispatchTicksObserved), label{"bound", "any"})
	x.sample(name, float64(v.DispatchTicksDepBound), label{"bound", "dependency"})
	x.sample(name, float64(v.DispatchTicksSlotBound), label{"bound", "slot"})
	x.gauge("itervox_dispatch_eligible_waiting", "Eligible issues waiting for a slot on the last tick.", float64(v.DispatchEligibleWaiting))
	x.gauge("itervox_dispatch_blocked_by_dependency", "Candidates held by a dependency gate on the last tick.", float64(v.DispatchBlockedByDep))

	name = "itervox_outbox_entries"
	x.header(name, "gauge", "Write-ahead outbox entries awaiting delivery, by state (degraded and rate_limited are subsets of pending).")
	x.sample(name, float64(v.OutboxDegraded), label{"state", "degraded"})
	x.sample(name, float64(v.OutboxEntries), label{"state", "pending"})
	x.sample(name, float64(v.OutboxRateLimited), label{"state", "rate_limited"})

	x.gauge("itervox_tracker_rate_limited_until_seconds",
		"Unix time the tracker rate-limit gate lifts; 0 while the gate is closed.",
		unixSeconds(v.TrackerRateLimitedUntil), label{"adapter", v.TrackerAdapter})
	x.gauge("itervox_tracker_poll_consecutive_failures",
		"Consecutive non-rate-limited candidate poll failures.", float64(v.TrackerPollFailures))
	name = "itervox_tracker_last_error_timestamp_seconds"
	x.header(name, "gauge", "Unix time of the most recent tracker failure, by kind; absent when none is recorded.")
	if !v.LastTrackerErrorAt.IsZero() {
		x.sample(name, unixSeconds(v.LastTrackerErrorAt), label{"kind", v.LastTrackerErrorKind})
	}
	x.gauge("itervox_event_loop_last_idle_timestamp_seconds",
		"Unix time the orchestrator event loop last finished an iteration; 0 before it starts.", unixSeconds(v.LoopLastIdle))

	if x.err != nil {
		return x.err
	}
	return x.w.Flush()
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
func escapeLabel(s string) string { return labelEscaper.Replace(s) }
