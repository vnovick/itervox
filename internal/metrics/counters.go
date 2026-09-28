package metrics

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Tracker request outcomes for TrackerRequest.
const (
	// TrackerOutcomeOK is an HTTP response that was not a rate limit (any
	// status: a 4xx/5xx is the adapter's to interpret).
	TrackerOutcomeOK = "ok"
	// TrackerOutcomeRateLimited is a call that ended in a typed rate-limit
	// error (in-call retries exhausted or the wait budget exceeded).
	TrackerOutcomeRateLimited = "rate_limited"
	// TrackerOutcomeError is a transport failure (no usable response).
	TrackerOutcomeError = "error"
)

// counterVec is a counter family keyed by a small, bounded set of label
// values. Label values must come from constants (reasons, adapters,
// outcomes) — never issue identifiers.
type counterVec struct {
	mu     sync.Mutex
	labels []string
	vals   map[string]*atomic.Uint64 // key: label values joined by \x00
}

func newCounterVec(labels ...string) *counterVec {
	return &counterVec{labels: labels, vals: map[string]*atomic.Uint64{}}
}

func (c *counterVec) get(values ...string) *atomic.Uint64 {
	key := strings.Join(values, "\x00")
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vals[key]
	if !ok {
		v = &atomic.Uint64{}
		c.vals[key] = v
	}
	return v
}

type counterSample struct {
	values []string
	value  uint64
}

// samples returns every series sorted by label values, for stable output.
func (c *counterVec) samples() []counterSample {
	c.mu.Lock()
	out := make([]counterSample, 0, len(c.vals))
	for k, v := range c.vals {
		out = append(out, counterSample{values: strings.Split(k, "\x00"), value: v.Load()})
	}
	c.mu.Unlock()
	slices.SortFunc(out, func(a, b counterSample) int {
		return slices.Compare(a.values, b.values)
	})
	return out
}

// Client error report outcomes for ClientError (CORE-048).
const (
	// ClientErrorOutcomeAccepted — the report was handed to the event loop.
	ClientErrorOutcomeAccepted = "accepted"
	// ClientErrorOutcomeRateLimited — refused by the route's rate limit.
	ClientErrorOutcomeRateLimited = "rate_limited"
	// ClientErrorOutcomeDropped — the event channel was full (503).
	ClientErrorOutcomeDropped = "dropped"
)

// ClientErrorKinds is the bounded label set for ClientError: anything else a
// client sends is counted (and reported) as "other".
var ClientErrorKinds = []string{"render", "error", "unhandledrejection", "schema", "other"}

var (
	clientErrors    = newCounterVec("kind", "outcome")
	workerExits     = newCounterVec("reason")
	trackerRequests = newCounterVec("adapter", "outcome")
	goroutinePanics atomic.Uint64
	eventsDropped   atomic.Uint64
)

// WorkerExit counts one worker exit processed by the event loop, labelled by
// its terminal reason (succeeded, failed, stalled, input_required, …).
func WorkerExit(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	workerExits.get(reason).Add(1)
}

// TrackerRequest counts one tracker HTTP call (after its in-call retries)
// by adapter ("linear", "github") and outcome (TrackerOutcome*).
func TrackerRequest(adapter, outcome string) {
	trackerRequests.get(adapter, outcome).Add(1)
}

// GoroutinePanic counts one panic recovered on a daemon goroutine. The site
// is in the panic's own log line; the counter is unlabelled so an alert can
// fire on any increase without knowing every site in advance.
func GoroutinePanic() { goroutinePanics.Add(1) }

// EventDropped counts one orchestrator event that could not be delivered to
// the event loop (a full channel on a non-blocking send, or a bounded send
// that timed out). Unlabelled for the same reason as GoroutinePanic; the
// dropping site logs which event it was.
func EventDropped() { eventsDropped.Add(1) }

// ClientError counts one web client error report (CORE-048) by kind (one
// of ClientErrorKinds) and outcome (ClientErrorOutcome*).
func ClientError(kind, outcome string) {
	clientErrors.get(kind, outcome).Add(1)
}

// PreinitClientErrors exports every kind × outcome series at 0.
func PreinitClientErrors() {
	for _, k := range ClientErrorKinds {
		for _, o := range []string{ClientErrorOutcomeAccepted, ClientErrorOutcomeDropped, ClientErrorOutcomeRateLimited} {
			clientErrors.get(k, o)
		}
	}
}

// PreinitWorkerExitReasons exports reasons at 0 before the first exit, so a
// rate() over the family works from the first scrape.
func PreinitWorkerExitReasons(reasons ...string) {
	for _, r := range reasons {
		workerExits.get(r)
	}
}

// PreinitTrackerAdapter exports adapter's request series at 0 for every
// outcome.
func PreinitTrackerAdapter(adapter string) {
	if adapter == "" {
		return
	}
	for _, o := range []string{TrackerOutcomeOK, TrackerOutcomeRateLimited, TrackerOutcomeError} {
		trackerRequests.get(adapter, o)
	}
}

// TrackerRequestCount reads one tracker-request counter. A cross-package
// test reader (tracker tests); kept in production code (deadcode allowlist).
func TrackerRequestCount(adapter, outcome string) uint64 {
	return trackerRequests.get(adapter, outcome).Load()
}

// EventsDroppedCount reads the dropped-event counter. A cross-package test
// reader (orchestrator tests); kept in production code (deadcode allowlist).
func EventsDroppedCount() uint64 { return eventsDropped.Load() }

// ClientErrorCount reads one client-error counter. A cross-package test
// reader (server tests); kept in production code (deadcode allowlist).
func ClientErrorCount(kind, outcome string) uint64 { return clientErrors.get(kind, outcome).Load() }
