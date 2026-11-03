package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// #187 — the poll tick's tracker reads run off the event loop, under a deadline.
//
// The tick used to read the tracker synchronously inside the event loop with
// the daemon's root context, which has no deadline: one tick could spend
// minutes in a slow or rate-limited tracker (a 30 s HTTP timeout per attempt,
// up to four 60 s rate-limit waits per call, one call per page), and while it
// did no event was handled — finished workers were not recorded, operator
// actions queued up, the dashboard froze.
//
// Now a tick splits in three:
//
//  1. On the loop: the prelude (cfg snapshot, watchdogs, breakers, due
//     retries, stall detection) and the poll's work order (planTrackerPoll).
//  2. Off the loop: runTrackerPoll reads the running issues' states, the
//     candidate list and the input-required reply checks under one deadline
//     (trackerReadBudget) and sends EventTrackerPolled. At most one poll is in
//     flight; a tick that finds one still running skips its own.
//  3. On the loop: applyTrackerPoll applies the result with the state the loop
//     has by then (reconcile, dispatch, janitors). Dispatch picks' #116 claim
//     reads go off the loop too (claim_async.go).
//
// The reads that stay on the loop (a due retry's state read, a pending input
// resume's detail read, a pending review's read, an automation queue entry's
// read, a reviewer dispatch's list read) share one per-iteration deadline
// (loopReadBudget) through loopReadCtx, so no loop iteration can block on the
// tracker for longer than that.

// Tracker read deadlines.
const (
	// trackerReadBudgetMultiplier: an off-loop poll may take up to three poll
	// intervals. A large paginated fetch can legitimately outlast one
	// interval; a poll older than three is stale enough that cutting it and
	// polling afresh is better than waiting on it.
	trackerReadBudgetMultiplier = 3
	// trackerReadBudgetFloor keeps a short polling.interval_ms (tests,
	// demos) from starving a real request: one full HTTP attempt (the Linear
	// client's 30 s timeout) always fits.
	trackerReadBudgetFloor = 30 * time.Second
	// trackerReadBudgetCap bounds a long polling.interval_ms: a poll never
	// runs past two minutes, whatever the interval.
	trackerReadBudgetCap = 2 * time.Minute
	// loopReadBudgetCap bounds the tracker reads one event-loop iteration may
	// make in total. Every one of them blocks the loop, so the bound is the
	// floor of an off-loop read, not its cap.
	loopReadBudgetCap = 30 * time.Second
	// trackerReadWatchdogGrace is how long past its deadline an off-loop
	// read may stay unanswered before the loop releases its latch (a tracker
	// that ignores its context).
	trackerReadWatchdogGrace = 30 * time.Second
	// SlowLoopIterationWarn is how long one event-loop iteration may run
	// before it is logged as slow.
	SlowLoopIterationWarn = 10 * time.Second
)

// trackerReadBudget is the deadline of one off-loop tracker poll (and of one
// off-loop claim-read batch): min(max(3 × polling.interval_ms, 30 s), 2 min).
func (o *Orchestrator) trackerReadBudget() time.Duration {
	if o.trackerReadBudgetOverride > 0 {
		return o.trackerReadBudgetOverride
	}
	interval := time.Duration(o.cfg.Polling.IntervalMs) * time.Millisecond
	return min(max(trackerReadBudgetMultiplier*interval, trackerReadBudgetFloor), trackerReadBudgetCap)
}

// loopReadBudget is the total deadline of the tracker reads one event-loop
// iteration makes.
func (o *Orchestrator) loopReadBudget() time.Duration {
	return min(o.trackerReadBudget(), loopReadBudgetCap)
}

// SlowPollThreshold is how long a poll may run before it is reported slow:
// the poll interval plus a margin of half an interval (at least a second).
func SlowPollThreshold(interval time.Duration) time.Duration {
	return interval + max(interval/2, time.Second)
}

// loopIterationKey carries the current loop iteration's read deadline.
type loopIterationKey struct{}

// loopIteration is one event-loop iteration's shared tracker-read deadline.
// ended is set when the iteration finishes, so a context that outlives it (a
// worker's context derived from the loop's) stops carrying the deadline.
type loopIteration struct {
	deadline time.Time
	ended    atomic.Bool
}

func liveLoopIteration(ctx context.Context) *loopIteration {
	it, _ := ctx.Value(loopIterationKey{}).(*loopIteration)
	if it == nil || it.ended.Load() {
		return nil
	}
	return it
}

// beginLoopIteration attaches a read deadline loopReadBudget from now to ctx,
// unless ctx already carries a live one. The returned func ends it.
func (o *Orchestrator) beginLoopIteration(ctx context.Context) (context.Context, func()) {
	if liveLoopIteration(ctx) != nil {
		return ctx, func() {}
	}
	it := &loopIteration{deadline: time.Now().Add(o.loopReadBudget())}
	return context.WithValue(ctx, loopIterationKey{}, it), func() { it.ended.Store(true) }
}

// loopReadCtx bounds an on-loop tracker read by the current iteration's
// deadline. Outside a live iteration (a worker goroutine, an HTTP handler)
// ctx is returned unchanged.
func loopReadCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if it := liveLoopIteration(ctx); it != nil {
		return context.WithDeadline(ctx, it.deadline)
	}
	return ctx, func() {}
}

// TrackerPollState is the event-loop state of the off-loop tracker poll.
type TrackerPollState struct {
	// InFlight is the single-flight latch: a poll's goroutine is running.
	InFlight bool
	// StartedAt is when the in-flight poll started; zero when none is.
	StartedAt time.Time
	// Generation numbers the polls; a result whose generation is not the
	// in-flight one (the watchdog released it) is dropped.
	Generation uint64
	// SlowWarned is whether the in-flight poll was already logged as slow.
	SlowWarned bool
	// ClaimGeneration numbers the off-loop claim-read batches
	// (ClaimCheckPending.Generation).
	ClaimGeneration uint64
	// LastFinishedAt / LastDuration describe the last poll that finished.
	LastFinishedAt time.Time
	LastDuration   time.Duration
	// EndedDuringPoll holds the issue IDs whose run ended while the poll
	// was in flight. The poll's candidate list may predate the state move
	// the run made, so those issues are not dispatched from it.
	EndedDuringPoll map[string]struct{}
}

// ClaimCheckPending is one dispatch pick whose claim read is in flight.
type ClaimCheckPending struct {
	Identifier string
	StartedAt  time.Time
	Generation uint64
}

// replyTarget is one input-required entry whose tracker comments the poll
// reads. entry is the loop's pointer, carried through so the apply step can
// tell the entry it was read for from a newer one; the poll goroutine never
// dereferences it.
type replyTarget struct {
	Identifier string
	IssueID    string
	entry      *InputRequiredEntry
}

// trackerPollRequest is one poll's immutable work order, built on the loop.
type trackerPollRequest struct {
	Generation uint64
	StartedAt  time.Time
	Budget     time.Duration
	// Shed: the tracker budget is in the write reserve (#42-E); read the
	// running issues' states only.
	Shed bool
	// Running is runningRunIDs at the poll's start.
	Running map[string]string
	Replies []replyTarget
}

// trackerPollResult is what one poll read. Built by the poll goroutine;
// applied by the loop.
type trackerPollResult struct {
	Req *trackerPollRequest

	RefreshRan bool
	Refreshed  []domain.Issue
	RefreshErr error

	FetchRan bool
	Issues   []domain.Issue
	FetchErr error

	// ReplyDetails / ReplyErrs are keyed by issue ID.
	ReplyDetails map[string]*domain.Issue
	ReplyErrs    map[string]error

	FinishedAt time.Time
}

// transportFailed reports whether any read of the poll hit a transport
// failure (tracker_breaker.go).
func (r *trackerPollResult) transportFailed() bool {
	if isTrackerTransportError(r.RefreshErr) || isTrackerTransportError(r.FetchErr) {
		return true
	}
	for _, err := range r.ReplyErrs {
		if isTrackerTransportError(err) {
			return true
		}
	}
	return false
}

// planTrackerPoll builds this tick's poll work order, or returns nil when no
// poll starts: the breaker is open, or a poll is still in flight. Stamps the
// reply checks' LastReplyCheckAt (they may be dropped, never re-read early).
// Event loop.
func (o *Orchestrator) planTrackerPoll(state *State, now time.Time) *trackerPollRequest {
	if state.TrackerPoll.InFlight {
		o.notePollStillRunning(state, now)
		return nil
	}
	if trackerReadsBlocked(*state, now) {
		slog.Debug("orchestrator: tracker breaker open, skipping this tick's poll",
			"open_until", state.TrackerBreaker.OpenUntil)
		o.publishPollDegraded(*state)
		return nil
	}
	shed := shouldShedPollingReads(o.tracker, o.rateLimitReservePercent())
	req := &trackerPollRequest{
		Generation: state.TrackerPoll.Generation + 1,
		StartedAt:  now,
		Budget:     o.trackerReadBudget(),
		Shed:       shed,
		Running:    runningRunIDs(*state),
	}
	if !shed {
		req.Replies = selectReplyTargets(o, state, now)
	}
	return req
}

// selectReplyTargets picks this tick's tracker-reply checks (bounded, least
// recently checked first) and stamps each one's LastReplyCheckAt before the
// read: a failing read must still advance the entry's place in the queue, or
// a permanently unreachable issue would monopolise the budget. Event loop.
func selectReplyTargets(o *Orchestrator, state *State, now time.Time) []replyTarget {
	if len(state.InputRequiredIssues) == 0 {
		return nil
	}
	selected := selectTrackerReplyCheckBatch(state.InputRequiredIssues, trackerReplyCheckPerTickBudget)
	out := make([]replyTarget, 0, len(selected))
	for _, identifier := range selected {
		entry := state.InputRequiredIssues[identifier]
		if entry == nil {
			continue
		}
		entry.LastReplyCheckAt = now
		o.warnIfQuestionStuck(identifier, entry)
		out = append(out, replyTarget{Identifier: identifier, IssueID: entry.IssueID, entry: entry})
	}
	return out
}

// armTrackerPoll sets the single-flight latch for req. Event loop.
func (o *Orchestrator) armTrackerPoll(state *State, req *trackerPollRequest) {
	state.TrackerPoll.InFlight = true
	state.TrackerPoll.StartedAt = req.StartedAt
	state.TrackerPoll.Generation = req.Generation
	state.TrackerPoll.SlowWarned = false
	state.TrackerPoll.EndedDuringPoll = nil
	o.pollStartedNano.Store(req.StartedAt.UnixNano())
}

// startTrackerPoll arms the single-flight latch and runs req off the loop.
// Event loop.
func (o *Orchestrator) startTrackerPoll(ctx context.Context, state *State, req *trackerPollRequest) {
	o.armTrackerPoll(state, req)
	failed := &trackerPollResult{Req: req, FetchRan: true, FetchErr: errors.New("orchestrator: tracker poll panicked")}
	goSafe(&o.trackerReadsWg, "tracker-poll", "", func() {
		res := o.runTrackerPoll(ctx, req)
		o.sendTrackerReadEvent(ctx, OrchestratorEvent{Type: EventTrackerPolled, TrackerPoll: res})
	}, func() {
		// The latch must clear even when the poll panics.
		failed.FinishedAt = time.Now()
		o.sendEventBounded(OrchestratorEvent{Type: EventTrackerPolled, TrackerPoll: failed}, "")
	})
}

// runTrackerPoll performs one poll's tracker reads under req.Budget. It never
// reads or writes State: it takes the immutable work order and returns data.
// Shutdown (ctx cancelled) cancels the in-flight reads.
func (o *Orchestrator) runTrackerPoll(ctx context.Context, req *trackerPollRequest) *trackerPollResult {
	res := &trackerPollResult{Req: req}
	pctx, cancel := context.WithTimeout(ctx, req.Budget)
	defer cancel()
	if len(req.Running) > 0 {
		res.RefreshRan = true
		res.Refreshed, res.RefreshErr = fetchTrackerStates(pctx, o.tracker, req.Running)
	}
	if !req.Shed {
		res.FetchRan = true
		res.Issues, res.FetchErr = o.tracker.FetchCandidateIssues(pctx)
		if res.FetchErr == nil && len(req.Replies) > 0 {
			res.ReplyDetails, res.ReplyErrs = o.readReplyTargets(pctx, req.Replies)
		}
	}
	res.FinishedAt = time.Now()
	return res
}

// fetchTrackerStates reads the current states of the issues in ids.
func fetchTrackerStates(ctx context.Context, tr tracker.Tracker, ids map[string]string) ([]domain.Issue, error) {
	keys := slices.Sorted(maps.Keys(ids))
	return tr.FetchIssueStatesByIDs(ctx, keys)
}

// readReplyTargets reads the reply targets' details: one batched request
// where the tracker supports it (#42), then an authoritative single read for
// each id the batch did not return (absence from a batch is not deletion).
// Off the loop.
func (o *Orchestrator) readReplyTargets(ctx context.Context, targets []replyTarget) (map[string]*domain.Issue, map[string]error) {
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.IssueID)
	}
	details := tracker.PrefetchDetails(ctx, o.tracker, ids)
	if details == nil {
		details = make(map[string]*domain.Issue, len(ids))
	}
	errs := make(map[string]error)
	for _, id := range ids {
		if details[id] != nil {
			continue
		}
		detailed, err := o.tracker.FetchIssueDetail(ctx, id)
		if err != nil {
			errs[id] = err
			continue
		}
		details[id] = detailed
	}
	return details, errs
}

// sendTrackerReadEvent hands an off-loop read's result to the loop. It blocks
// until the loop takes it or the daemon stops: the result clears a latch, so
// it is never dropped while the loop runs.
func (o *Orchestrator) sendTrackerReadEvent(ctx context.Context, ev OrchestratorEvent) {
	select {
	case o.events <- ev:
	case <-ctx.Done():
	}
}

// acceptTrackerPoll checks res against the latch and clears it. It returns
// false for a stale result (the watchdog released its poll), which the
// caller drops. Times the poll, reports a slow one, and feeds the breaker.
// Event loop.
func (o *Orchestrator) acceptTrackerPoll(state *State, res *trackerPollResult, now time.Time) bool {
	if res == nil || res.Req == nil {
		return false
	}
	if !state.TrackerPoll.InFlight || state.TrackerPoll.Generation != res.Req.Generation {
		slog.Debug("orchestrator: dropping a stale tracker poll result",
			"generation", res.Req.Generation, "current", state.TrackerPoll.Generation)
		return false
	}
	finished := res.FinishedAt
	if finished.IsZero() {
		finished = now
	}
	duration := finished.Sub(res.Req.StartedAt)
	if threshold := SlowPollThreshold(o.pollInterval()); duration > threshold {
		o.logger().Warn("orchestrator: tracker poll was slow",
			"duration", duration.Round(time.Millisecond).String(),
			"poll_interval", o.pollInterval().String(),
			"threshold", threshold.String())
	}
	state.TrackerPoll.InFlight = false
	state.TrackerPoll.StartedAt = time.Time{}
	state.TrackerPoll.SlowWarned = false
	state.TrackerPoll.LastFinishedAt = finished
	state.TrackerPoll.LastDuration = duration
	o.pollStartedNano.Store(0)
	o.observeTrackerTransport(state, res.transportFailed(), now)
	return true
}

// pollInterval is polling.interval_ms (read-only after startup).
func (o *Orchestrator) pollInterval() time.Duration {
	return time.Duration(o.cfg.Polling.IntervalMs) * time.Millisecond
}

// notePollStillRunning logs, once per poll, a poll that has run past
// SlowPollThreshold. Event loop.
func (o *Orchestrator) notePollStillRunning(state *State, now time.Time) {
	p := &state.TrackerPoll
	if !p.InFlight || p.SlowWarned {
		return
	}
	running := now.Sub(p.StartedAt)
	threshold := SlowPollThreshold(o.pollInterval())
	if running <= threshold {
		slog.Debug("orchestrator: previous tracker poll still running, skipping this tick's poll",
			"running_for", running.Round(time.Millisecond).String())
		return
	}
	p.SlowWarned = true
	o.logger().Warn("orchestrator: tracker poll is running longer than the poll interval; the event loop keeps running",
		"running_for", running.Round(time.Millisecond).String(),
		"poll_interval", o.pollInterval().String(),
		"deadline", o.trackerReadBudget().String())
}

// noteRunEndedDuringPoll records that issueID's run ended while a poll was in
// flight (see TrackerPollState.EndedDuringPoll). Event loop.
func noteRunEndedDuringPoll(state *State, issueID string) {
	if !state.TrackerPoll.InFlight || issueID == "" {
		return
	}
	if state.TrackerPoll.EndedDuringPoll == nil {
		state.TrackerPoll.EndedDuringPoll = make(map[string]struct{})
	}
	state.TrackerPoll.EndedDuringPoll[issueID] = struct{}{}
}

// reclaimStuckTrackerReads is the off-loop reads' watchdog: a poll or claim
// batch unanswered past its deadline plus trackerReadWatchdogGrace (a tracker
// that ignores its context) has its latch released, so the loop can poll and
// dispatch again. A late result is then dropped by its generation. Event loop.
func (o *Orchestrator) reclaimStuckTrackerReads(state *State, now time.Time) {
	limit := o.trackerReadBudget() + trackerReadWatchdogGrace
	if p := &state.TrackerPoll; p.InFlight && now.Sub(p.StartedAt) > limit {
		o.logger().Warn("orchestrator: tracker poll watchdog fired, releasing the poll",
			"started_at", p.StartedAt, "limit", limit.String())
		p.InFlight = false
		p.StartedAt = time.Time{}
		p.SlowWarned = false
		p.Generation++ // the abandoned poll's late result is stale
		o.pollStartedNano.Store(0)
		*state = o.recordPollFailure(*state, fmt.Errorf("orchestrator: tracker poll exceeded its deadline: %w", context.DeadlineExceeded), now)
		o.observeTrackerTransport(state, true, now)
	}
	for id, pending := range state.ClaimChecks {
		if now.Sub(pending.StartedAt) <= limit {
			continue
		}
		slog.Warn("orchestrator: claim check watchdog fired, releasing the pick",
			"identifier", pending.Identifier, "started_at", pending.StartedAt)
		delete(state.ClaimChecks, id)
	}
}
