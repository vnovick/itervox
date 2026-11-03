package orchestrator

import (
	"log/slog"
	"sort"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// #213 — the event feed: a bounded, append-only record of notable events a
// client that was away (a closed Mac app window, a phone, a relay) reads
// with a cursor through GET /api/v1/feed?since=<seq>.
//
// Invariants:
//   - State.Feed and State.FeedSeq are event-loop state. Only the loop
//     assigns seq (appendFeed); in-loop producers call o.emitFeed from the
//     handlers that already observe the transition, off-loop producers call
//     RecordFeedEvent, a NON-blocking send of EventFeedRecorded (a dropped
//     event is counted, never re-sent).
//   - Payloads are small and structured: identifiers, enums, profile and
//     backend names, a PR URL, an operator's pause reason. Never agent
//     output, prompt text or a guardrail command line. Free text (the pause
//     reason) is redacted before it is truncated (sanitizeFailureMessage).
//   - The ring is append-only. It is trimmed only by re-slicing from the
//     front (never by writing into an index below len), so a snapshot that
//     shares the slice header (State.Clone does not copy it) can never
//     observe a write — see cloneAllowlist["State.Feed"].
//   - Bounded to FeedCap events and FeedMaxAge, and persisted to feed.jsonl
//     through the CORE-038 ledger writer (feed_persist.go).

// Feed event kinds.
const (
	FeedRunStarted        = "run_started"
	FeedRunFinished       = "run_finished"
	FeedRunFailed         = "run_failed"
	FeedInputRequired     = "input_required"
	FeedInputAnswered     = "input_answered"
	FeedReviewVerdict     = "review_verdict"
	FeedPROpened          = "pr_opened"
	FeedPRMerged          = "pr_merged"
	FeedGuardrailBlocked  = "guardrail_blocked"
	FeedGuardrailApproved = "guardrail_approved"
	FeedLessonProposed    = "lesson_proposed"
	FeedDispatchPaused    = "dispatch_paused"
	FeedDispatchResumed   = "dispatch_resumed"
)

// FeedCap and FeedMaxAge bound the feed: the newest FeedCap events, none
// older than FeedMaxAge.
const (
	FeedCap    = 5000
	FeedMaxAge = 7 * 24 * time.Hour
)

// FeedMaxLimit caps one feed read; FeedDefaultLimit is used when the caller
// asks for none.
const (
	FeedDefaultLimit = 200
	FeedMaxLimit     = 1000
)

// EventFeedRecorded carries one feed event from an off-loop producer
// (RecordFeedEvent). The loop assigns its seq.
const EventFeedRecorded EventType = "FeedRecorded"

// FeedPayload is the small, redacted payload of a feed event. Every field
// is optional; which ones are set depends on the kind.
type FeedPayload struct {
	// RunID names the run (run_*, input_required).
	RunID string `json:"runId,omitempty"`
	// RunKind is "worker", "reviewer" or "automation".
	RunKind string `json:"runKind,omitempty"`
	Profile string `json:"profile,omitempty"`
	Backend string `json:"backend,omitempty"`
	// Outcome is the run's terminal reason (run_finished, run_failed), the
	// lesson outcome (lesson_proposed) or the quorum outcome
	// (review_verdict).
	Outcome string `json:"outcome,omitempty"`
	// Verdict is a reviewer's normalized verdict (review_verdict).
	Verdict string `json:"verdict,omitempty"`
	// PRURL is the pull request (pr_opened, pr_merged).
	PRURL string `json:"prUrl,omitempty"`
	// Kind is the guardrail kind (guardrail_blocked).
	Kind string `json:"kind,omitempty"`
	// Via is how an input or approval was answered ("dashboard",
	// "slack" (#230), "agent", "tracker", "dismiss").
	Via string `json:"via,omitempty"`
	// MemoryID is the proposed lesson's entry (lesson_proposed).
	MemoryID string `json:"memoryId,omitempty"`
	// Reason is the operator's redacted pause reason (dispatch_paused).
	Reason string `json:"reason,omitempty"`
	// Until is when a pause expires (dispatch_paused); zero = until resumed.
	Until time.Time `json:"until,omitzero"`
}

// FeedEvent is one feed entry. Seq increases by one per event and never
// repeats, across restarts (feed.jsonl keeps the last seq).
type FeedEvent struct {
	Seq        int64       `json:"seq"`
	TS         time.Time   `json:"ts"`
	Kind       string      `json:"kind"`
	Identifier string      `json:"identifier,omitempty"`
	Payload    FeedPayload `json:"payload,omitzero"`
}

// FeedPage is one cursor read of the feed.
type FeedPage struct {
	Events []FeedEvent `json:"events"`
	// NextSeq is the cursor for the next read: the seq of the last event
	// returned, or the caller's cursor (capped at HeadSeq) when none was.
	NextSeq int64 `json:"nextSeq"`
	// HeadSeq is the newest seq the daemon has assigned.
	HeadSeq int64 `json:"headSeq"`
	// OldestSeq is the oldest seq still in the window (0 when empty).
	OldestSeq int64 `json:"oldestSeq"`
	// Truncated is true when events after the caller's cursor have left
	// the window (or the cursor is ahead of the daemon's head, e.g. the feed
	// file was removed): the client must resynchronize from /api/v1/state.
	Truncated bool `json:"truncated"`
	// HasMore is true when the limit cut the page: read again from NextSeq.
	HasMore bool `json:"hasMore"`
}

// sanitizeFeedEvent bounds every field: labels are capped, free text is
// redacted then capped. Never trusts a producer to have done it.
func sanitizeFeedEvent(ev FeedEvent) FeedEvent {
	ev.Kind = sanitizeFailureField(ev.Kind)
	ev.Identifier = sanitizeFailureField(ev.Identifier)
	p := &ev.Payload
	for _, f := range []*string{&p.RunID, &p.RunKind, &p.Profile, &p.Backend, &p.Outcome, &p.Verdict, &p.Kind, &p.Via, &p.MemoryID} {
		*f = sanitizeFailureField(*f)
	}
	p.PRURL = sanitizeFailureMessage(p.PRURL)
	p.Reason = sanitizeFailureMessage(p.Reason)
	return ev
}

// appendFeed assigns ev the next seq, appends it and trims the ring to
// FeedCap events younger than FeedMaxAge. Event loop only. Returns the
// stored event.
func appendFeed(state *State, ev FeedEvent, now time.Time) FeedEvent {
	ev = sanitizeFeedEvent(ev)
	if ev.TS.IsZero() {
		ev.TS = now
	}
	ev.TS = ev.TS.UTC()
	state.FeedSeq++
	ev.Seq = state.FeedSeq
	state.Feed = append(state.Feed, ev)
	trimFeed(state, now)
	return ev
}

// trimFeed drops the events over FeedCap and those older than FeedMaxAge.
// Re-slices only (see the append-only invariant above).
func trimFeed(state *State, now time.Time) {
	drop := max(0, len(state.Feed)-FeedCap)
	cutoff := now.Add(-FeedMaxAge)
	for drop < len(state.Feed) && state.Feed[drop].TS.Before(cutoff) {
		drop++
	}
	if drop == 0 {
		return
	}
	if drop == len(state.Feed) {
		state.Feed = nil
		return
	}
	state.Feed = state.Feed[drop:]
}

// emitFeed appends one event and persists the feed. Event loop only.
func (o *Orchestrator) emitFeed(state *State, kind, identifier string, payload FeedPayload) {
	now := time.Now()
	appendFeed(state, FeedEvent{Kind: kind, Identifier: identifier, Payload: payload}, now)
	o.saveFeedToDisk(state)
}

// feedRunPayload is the payload of a run event from its entry (nil-safe).
func feedRunPayload(entry *RunEntry) FeedPayload {
	if entry == nil {
		return FeedPayload{}
	}
	kind := entry.Kind
	if kind == "" {
		kind = "worker"
	}
	return FeedPayload{RunID: entry.RunID, RunKind: kind, Profile: entry.ProfileName, Backend: entry.Backend}
}

// emitRunExitFeed records a worker exit: input_required for a run that
// stopped to ask, run_failed for a run that did not finish its work, and
// run_finished otherwise (success, a user cancel, reconciliation). The
// payload is the run's identity and terminal reason only. Event loop.
func (o *Orchestrator) emitRunExitFeed(state *State, live, exit *RunEntry, userCancelled bool) {
	if exit == nil {
		return
	}
	p := feedRunPayload(live)
	if live == nil {
		p = feedRunPayload(exit)
	}
	reason := exit.TerminalReason
	p.Outcome = string(reason)
	kind := FeedRunFailed
	switch {
	case userCancelled:
		kind, p.Outcome = FeedRunFinished, "cancelled"
	case reason == TerminalSucceeded || reason == TerminalCanceledByReconciliation:
		kind = FeedRunFinished
	case reason == TerminalInputRequired:
		kind = FeedInputRequired
	}
	o.emitFeed(state, kind, exit.Issue.Identifier, p)
}

// RecordFeedEvent hands one feed event to the event loop without blocking.
// Safe from any goroutine. The loop assigns seq and ts (ev.TS, when set, is
// kept). Returns false when the loop has exited or the channel is full: the
// event is then counted and dropped, never re-sent.
func (o *Orchestrator) RecordFeedEvent(ev FeedEvent) bool {
	if p := o.loopExited.Load(); p != nil {
		select {
		case <-*p:
			return o.dropFeedEvent(ev, "event loop has exited")
		default:
		}
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventFeedRecorded, Identifier: ev.Identifier, feed: &ev}:
		return true
	default:
		return o.dropFeedEvent(ev, "event channel full")
	}
}

func (o *Orchestrator) dropFeedEvent(ev FeedEvent, why string) bool {
	metrics.EventDropped()
	slog.Warn("orchestrator: feed event dropped ("+why+")", "kind", ev.Kind, "identifier", ev.Identifier)
	return false
}

// applyFeedRecorded handles EventFeedRecorded. Event loop only.
func (o *Orchestrator) applyFeedRecorded(state *State, ev OrchestratorEvent) {
	if ev.feed == nil || ev.feed.Kind == "" {
		return
	}
	appendFeed(state, *ev.feed, time.Now())
	o.saveFeedToDisk(state)
}

// readFeed is the cursor read over a feed ring whose newest seq is head.
func readFeed(feed []FeedEvent, head, since int64, limit int) FeedPage {
	if limit <= 0 {
		limit = FeedDefaultLimit
	}
	limit = min(limit, FeedMaxLimit)
	since = max(since, 0)
	page := FeedPage{Events: []FeedEvent{}, HeadSeq: head}
	if len(feed) > 0 {
		page.OldestSeq = feed[0].Seq
	}
	switch {
	case since > head:
		page.Truncated = true // the cursor is from a feed this daemon no longer has
	case len(feed) == 0:
		page.Truncated = since < head
	default:
		page.Truncated = since < page.OldestSeq-1
	}
	// Seqs ascend (a malformed line skipped on load can leave a gap).
	start := sort.Search(len(feed), func(i int) bool { return feed[i].Seq > since })
	end := min(start+limit, len(feed))
	page.Events = append(page.Events, feed[start:end]...)
	page.HasMore = end < len(feed)
	page.NextSeq = min(since, head)
	if n := len(page.Events); n > 0 {
		page.NextSeq = page.Events[n-1].Seq
	}
	return page
}

// Feed returns the events after since (at most limit; 0 = the default)
// from the last published snapshot. Safe from any goroutine; it copies only
// the requested slice, never the whole State.
func (o *Orchestrator) Feed(since int64, limit int) FeedPage {
	o.snapMu.RLock()
	feed, head := o.lastSnap.Feed, o.lastSnap.FeedSeq
	o.snapMu.RUnlock()
	return readFeed(feed, head, since, limit)
}

// emitVerdictFeed records a finished reviewer's normalized verdict (never
// its reasons, which are agent text) and, once the review is complete, the
// quorum outcome. Event loop only.
func (o *Orchestrator) emitVerdictFeed(state *State, identifier, profile string, done bool) {
	p := FeedPayload{Profile: profile, RunKind: "reviewer"}
	for _, v := range state.ReviewVerdicts[identifier] {
		if v.Profile == profile {
			p.Verdict = v.Verdict
		}
	}
	if out, ok := state.ReviewOutcomes[identifier]; ok && done {
		p.Outcome = map[bool]string{true: "blocked", false: "passed"}[out.Blocked]
	}
	o.emitFeed(state, FeedReviewVerdict, identifier, p)
}
