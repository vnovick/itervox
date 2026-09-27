package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// CORE-046 — State.RecentFailures.
//
// Failures used to be scattered over per-object surfaces (retry rows, history
// rows, issue.error, queue errorMessage, outbox lastError, per-issue ERROR
// buffer lines, HEARTBEAT's single "Last error"). RecentFailures is one
// bounded, time-ordered record an operator can scan.
//
// Invariants:
//   - Only the event loop mutates State.RecentFailures. In-loop producers
//     (worker exit, poll failure, discard completion) call
//     appendRecentFailure on the state they already own; off-loop producers
//     (ledger writer, outbox flusher, recovered panics, the web client-error
//     handler) call RecordFailure, a NON-blocking send of
//     EventFailureRecorded. A dropped event bumps a counter and is never
//     re-sent through the channel.
//   - Messages come from classified errors only — never prompt text, agent
//     stdout or RunEntry failure text — and are redacted
//     (logging.RedactString) BEFORE they are truncated to
//     failureMessageMaxBytes, so a cut can never leave half a secret behind.
//   - OccurredAt is set by the producer, RecordedAt by the loop on append.
//     The ring is kept in RecordedAt (append) order and evicts the oldest
//     recorded entry; the dashboard sorts by OccurredAt. An off-loop producer
//     can deliver an older failure after a newer one — it is still the newest
//     ring entry.
//   - A failure identical (kind, identifier, source, message) to the newest
//     entry coalesces into it (Count++), so a retrying ledger write cannot
//     flush every other failure out of the ring. A poll failure
//     (FailureKindTrackerPoll) coalesces by (kind, identifier, source)
//     whatever its text and wherever its entry sits: the entry moves to the
//     newest slot with the latest message and the running count, so a
//     sustained outage whose error text varies (rotating address, request
//     id) is ONE entry, never a ring flush (BH-M2-2).
//   - Session-scoped: not persisted across a process restart. cmd/itervox
//     carries the ring across WORKFLOW.md reloads (each reload builds a new
//     Orchestrator) via SeedRecentFailures.

// FailureKind classifies a RecentFailures entry.
type FailureKind string

// Failure kinds recorded in State.RecentFailures.
const (
	// FailureKindWorkerFailed — a worker run ended TerminalFailed (not a
	// context cancellation).
	FailureKindWorkerFailed FailureKind = "worker_failed"
	// FailureKindWorkerStalled — stall detection killed a worker.
	FailureKindWorkerStalled FailureKind = "worker_stalled"
	// FailureKindTrackerPoll — a non-rate-limited candidate poll failed.
	FailureKindTrackerPoll FailureKind = "tracker_poll"
	// FailureKindTrackerWrite — a failed-state/discard tracker move failed.
	FailureKindTrackerWrite FailureKind = "tracker_write"
	// FailureKindPersist — a runtime ledger write failed (CORE-038).
	FailureKindPersist FailureKind = "persist"
	// FailureKindOutbox — a write-ahead-outbox delivery failed (rate-limit
	// deferrals excluded).
	FailureKindOutbox FailureKind = "outbox"
	// FailureKindPanic — a background goroutine panicked and was recovered.
	FailureKindPanic FailureKind = "panic"
	// FailureKindClient — the web dashboard reported an error (CORE-048).
	FailureKindClient FailureKind = "client"
	// FailureKindAutomation — an automation could not be dispatched, e.g.
	// pr_merged after a successful merge_pr (M6-close BH-M6-2).
	FailureKindAutomation FailureKind = "automation"
)

// RecentFailuresCap bounds State.RecentFailures.
const RecentFailuresCap = 100

// failureMessageMaxBytes bounds one entry's message (after redaction).
const failureMessageMaxBytes = 1024

// FailureRecord is one RecentFailures entry. It holds no reference fields,
// so State.Clone copies the slice by value.
type FailureRecord struct {
	Kind FailureKind
	// Identifier is the issue the failure concerns; empty when none.
	Identifier string
	// Source narrows the producer: ledger name, outbox entry kind, goroutine
	// name, client-error kind, tracker op.
	Source string
	// Message is redacted and at most failureMessageMaxBytes.
	Message string
	// OccurredAt is when the producer saw the failure (latest repeat when
	// Count > 1).
	OccurredAt time.Time
	// RecordedAt is when the event loop appended (or last coalesced) it.
	RecordedAt time.Time
	// Count is how many consecutive identical failures this entry stands for.
	Count int
}

// sanitizeFailureMessage redacts msg and then bounds it to
// failureMessageMaxBytes on a UTF-8 boundary.
func sanitizeFailureMessage(msg string) string {
	msg = strings.TrimSpace(logging.RedactString(msg))
	if len(msg) <= failureMessageMaxBytes {
		return msg
	}
	const ellipsis = "…"
	cut := failureMessageMaxBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + ellipsis
}

// appendRecentFailure appends f to state.RecentFailures. Event loop only.
func appendRecentFailure(state *State, f FailureRecord, now time.Time) {
	f.Message = sanitizeFailureMessage(f.Message)
	f.Identifier = sanitizeFailureField(f.Identifier)
	f.Source = sanitizeFailureField(f.Source)
	if f.OccurredAt.IsZero() {
		f.OccurredAt = now
	}
	f.RecordedAt = now
	f.Count = max(f.Count, 1)
	if coalescesBySource(f.Kind) {
		for i, e := range state.RecentFailures {
			if e.Kind != f.Kind || e.Identifier != f.Identifier || e.Source != f.Source {
				continue
			}
			f.Count += e.Count
			if e.OccurredAt.After(f.OccurredAt) {
				f.OccurredAt = e.OccurredAt
			}
			next := make([]FailureRecord, 0, len(state.RecentFailures))
			next = append(next, state.RecentFailures[:i]...)
			next = append(next, state.RecentFailures[i+1:]...)
			state.RecentFailures = append(next, f)
			return
		}
	}
	if n := len(state.RecentFailures); n > 0 {
		last := &state.RecentFailures[n-1]
		if last.Kind == f.Kind && last.Identifier == f.Identifier &&
			last.Source == f.Source && last.Message == f.Message {
			last.Count += f.Count
			last.RecordedAt = now
			if f.OccurredAt.After(last.OccurredAt) {
				last.OccurredAt = f.OccurredAt
			}
			return
		}
	}
	// Copy-on-append: never write into a backing array a published snapshot
	// could share (Clone copies, but keep the ring self-contained anyway).
	next := make([]FailureRecord, 0, min(len(state.RecentFailures)+1, RecentFailuresCap))
	start := max(0, len(state.RecentFailures)+1-RecentFailuresCap)
	next = append(next, state.RecentFailures[start:]...)
	state.RecentFailures = append(next, f)
}

// coalescesBySource reports whether kind coalesces by (kind, identifier,
// source) regardless of message — see the invariants above.
func coalescesBySource(kind FailureKind) bool {
	return kind == FailureKindTrackerPoll
}

// sanitizeFailureField bounds a short label field (identifier, source).
func sanitizeFailureField(s string) string {
	const maxLen = 128
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RecordFailure hands f to the event loop without blocking. Safe from any
// goroutine; the caller must not hold cfgMu. Returns false when the event
// channel was full: the failure is then counted (FailureEventsDropped and
// itervox_events_dropped_total) and logged, and must NOT be re-sent.
func (o *Orchestrator) RecordFailure(f FailureRecord) bool {
	if f.OccurredAt.IsZero() {
		f.OccurredAt = time.Now()
	}
	// BH-M2-4: once the loop has exited nothing drains o.events, so a send
	// that fits the buffer would "succeed" into a dead channel. Count it as
	// the drop it is. (A send that races the exit — between the loop's last
	// receive and the close, i.e. the shutdown persistence flush — can still
	// land in the buffer unread and uncounted; that window is bounded by the
	// flush.)
	if p := o.loopExited.Load(); p != nil {
		select {
		case <-*p:
			return o.dropFailure(f, "event loop has exited")
		default:
		}
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventFailureRecorded, Identifier: f.Identifier, Failure: &f}:
		return true
	default:
		return o.dropFailure(f, "event channel full")
	}
}

// dropFailure counts and logs a failure record that could not reach the
// event loop. It is never re-sent.
func (o *Orchestrator) dropFailure(f FailureRecord, why string) bool {
	o.failureEventsDropped.Add(1)
	metrics.EventDropped() // CORE-045
	slog.Warn("orchestrator: failure record dropped ("+why+")",
		"kind", f.Kind, "identifier", f.Identifier, "source", f.Source)
	return false
}

// FailureEventsDropped is how many RecordFailure calls found the event
// channel full since this orchestrator was created.
func (o *Orchestrator) FailureEventsDropped() int64 {
	return o.failureEventsDropped.Load()
}

// SeedRecentFailures installs the ring a previous run() generation left
// behind, so a WORKFLOW.md reload (which builds a new Orchestrator) does not
// wipe it. Must be called before Run; later calls are ignored.
func (o *Orchestrator) SeedRecentFailures(entries []FailureRecord) {
	if o.started.Load() {
		return
	}
	if len(entries) > RecentFailuresCap {
		entries = entries[len(entries)-RecentFailuresCap:]
	}
	o.seedFailures = append([]FailureRecord(nil), entries...)
}

// withPanicFailure wraps a goSafe/RecoverGoroutine onPanic so the recovered
// panic also lands in RecentFailures. The panic value is deliberately not in
// the message (it can carry arbitrary data); the daemon log has it with the
// stack.
func (o *Orchestrator) withPanicFailure(name, identifier string, onPanic func()) func() {
	return func() {
		// The site's own reconcile event first: it un-sticks state, the ring
		// entry is informational.
		if onPanic != nil {
			onPanic()
		}
		o.RecordFailure(FailureRecord{
			Kind:       FailureKindPanic,
			Identifier: identifier,
			Source:     name,
			Message:    fmt.Sprintf("goroutine %q panicked and was recovered; see the daemon log for the stack", name),
		})
	}
}

// errWorkerPanic marks a worker exit caused by a recovered panic in
// runWorker, so the ring can say so without quoting the panic value.
var errWorkerPanic = errors.New("worker panic")

// recordWorkerFailure appends the ring entry for a failed or stalled worker
// exit. The message is a classification only — the exit error carries agent
// failure text (stderr, possibly prompt fragments), which stays on the retry
// row and the per-issue log.
func (o *Orchestrator) recordWorkerFailure(state *State, ev OrchestratorEvent, attempt int, now time.Time) {
	if ev.RunEntry == nil {
		return
	}
	issue := ev.RunEntry.Issue
	switch ev.RunEntry.TerminalReason {
	case TerminalStalled:
		appendRecentFailure(state, FailureRecord{
			Kind:       FailureKindWorkerStalled,
			Identifier: issue.Identifier,
			Message:    fmt.Sprintf("worker stalled on attempt %d (no agent output within the stall timeout)", attempt+1),
			OccurredAt: now,
		}, now)
	case TerminalRateLimited:
		// CORE-051: classified by the typed limit signal, never its text.
		appendRecentFailure(state, FailureRecord{
			Kind:       FailureKindWorkerFailed,
			Identifier: issue.Identifier,
			Message:    fmt.Sprintf("worker run hit a vendor usage limit on attempt %d; see the issue log for details", attempt+1),
			OccurredAt: now,
		}, now)
	case TerminalFailed:
		if ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
			return // stopped by the orchestrator, not a failure
		}
		appendRecentFailure(state, FailureRecord{
			Kind:       FailureKindWorkerFailed,
			Identifier: issue.Identifier,
			Message:    fmt.Sprintf("worker run failed on attempt %d (%s); see the issue log for details", attempt+1, o.classifyWorkerFailure(ev.Error)),
			OccurredAt: now,
		}, now)
	}
}

// classifyWorkerFailure names the failure class without quoting its text.
func (o *Orchestrator) classifyWorkerFailure(err error) string {
	if err == nil {
		return "agent error"
	}
	if errors.Is(err, errWorkerPanic) {
		return "worker panicked"
	}
	msg := err.Error()
	o.cfgMu.RLock()
	transportPatterns := append([]string(nil), o.cfg.Agent.TransportErrorPatterns...)
	o.cfgMu.RUnlock()
	switch {
	case o.isRateLimitFailureCfg(msg):
		return "rate limited"
	case IsTransportFailure(msg, transportPatterns):
		return "transport error"
	default:
		return "agent error"
	}
}
