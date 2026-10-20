package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// Synchronous compositions of the off-loop tick (#187), for tests that drive
// one tick (or one reconcile / reply check) by hand and inspect the State it
// returns. Production runs the same pieces with the poll and the claim reads
// on goroutines (startTick, EventTrackerPolled, EventClaimChecked).

// onTick runs one whole tick on the calling goroutine: the prelude, the poll
// (under its read deadline) and the poll's application, with dispatch picks'
// claim reads inline under the iteration's read deadline.
func (o *Orchestrator) onTick(ctx context.Context, state State) State {
	ctx, end := o.beginLoopIteration(ctx)
	defer end()
	now := time.Now()
	state = o.tickPrelude(ctx, state, now)
	req := o.planTrackerPoll(&state, now)
	if req == nil {
		return state
	}
	o.armTrackerPoll(&state, req)
	res := o.runTrackerPoll(ctx, req)
	return o.applyTrackerPoll(ctx, state, res, claimsInline)
}

// ReconcileTrackerStates fetches current states for all running issues and
// reconciles them (applyTrackerStates). If the fetch fails, workers are kept
// and the error is logged.
func ReconcileTrackerStates(ctx context.Context, state State, tr tracker.Tracker, events chan OrchestratorEvent, cleanup CleanupWorker, logBuf ...*logbuffer.Buffer) State {
	var buf *logbuffer.Buffer
	if len(logBuf) > 0 {
		buf = logBuf[0]
	}
	ids := runningRunIDs(state)
	if len(ids) == 0 {
		return state
	}
	refreshed, err := fetchTrackerStates(ctx, tr, ids)
	if err != nil {
		slog.Warn("reconciliation: tracker refresh failed, keeping workers", "error", err)
		return state
	}
	return applyTrackerStates(state, ids, refreshed, events, cleanup, buf)
}

// checkTrackerReplies selects, reads and applies one tick's tracker-reply
// checks on the calling goroutine.
func (o *Orchestrator) checkTrackerReplies(ctx context.Context, state State) State {
	targets := selectReplyTargets(o, &state, time.Now())
	if len(targets) == 0 {
		return state
	}
	req := &trackerPollRequest{Replies: targets}
	res := &trackerPollResult{Req: req}
	res.ReplyDetails, res.ReplyErrs = o.readReplyTargets(ctx, targets)
	return o.applyReplyChecks(ctx, state, res)
}
