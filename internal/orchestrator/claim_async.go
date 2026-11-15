package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// #187 — the dispatch claim check (#116) reads the tracker off the loop.
//
// The poll's dispatch loop picks the issues it would start and reserves each
// one's slot in State.ClaimChecks (AvailableSlots counts the reservations,
// IneligibleReason reports claim_check_pending), then reads them all on one
// goroutine and returns. EventClaimChecked brings the reads back; the loop
// releases the reservations, re-checks each pick's eligibility against the
// state it has by then (an issue paused, discarded or started by another path
// meanwhile is skipped) and runs the same admission the inline check ran:
// not found → forget locally; read failed → not now; claimCheck → dispatch.
// #116's guarantee holds unchanged: no worker starts without a tracker read
// by ID taken after the pick, and an issue the read shows out of an active
// state is not started.

// IneligibleClaimCheckPending is the ineligible reason of an issue a poll
// picked whose claim read is still in flight off the loop.
const IneligibleClaimCheckPending = "claim_check_pending"

// claimMode says where a dispatch pick's claim read runs.
type claimMode int

const (
	// claimsOffLoop: picks reserve a slot and are read off the loop (Run).
	claimsOffLoop claimMode = iota
	// claimsInline: picks are read on the calling goroutine under the loop
	// iteration's read deadline (the synchronous tick tests drive).
	claimsInline
)

// claimRead is one pick and its claim read.
type claimRead struct {
	Issue    domain.Issue
	Detailed *domain.Issue
	Err      error
}

// claimBatchResult is one off-loop claim-read batch.
type claimBatchResult struct {
	Generation uint64
	Reads      []claimRead
}

// reserveClaimCheck holds a dispatch slot for issue while its claim read is
// in flight. Event loop.
func reserveClaimCheck(state *State, issue domain.Issue, gen uint64, now time.Time) {
	if state.ClaimChecks == nil {
		state.ClaimChecks = make(map[string]ClaimCheckPending)
	}
	state.ClaimChecks[issue.ID] = ClaimCheckPending{Identifier: issue.Identifier, StartedAt: now, Generation: gen}
}

// startClaimChecks reads picks (already reserved under gen) off the loop.
// Event loop.
func (o *Orchestrator) startClaimChecks(ctx context.Context, state *State, picks []domain.Issue, gen uint64) {
	if len(picks) == 0 {
		return
	}
	state.TrackerPoll.ClaimGeneration = gen
	budget := o.trackerReadBudget()
	goSafe(&o.trackerReadsWg, "claim-check", "", func() {
		res := &claimBatchResult{Generation: gen, Reads: make([]claimRead, 0, len(picks))}
		cctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		for _, issue := range picks {
			detailed, err := o.tracker.FetchIssueDetail(cctx, issue.ID)
			res.Reads = append(res.Reads, claimRead{Issue: issue, Detailed: detailed, Err: err})
		}
		o.sendTrackerReadEvent(ctx, OrchestratorEvent{Type: EventClaimChecked, ClaimBatch: res})
	}, func() {
		// Release the reservations even when the batch panics.
		res := &claimBatchResult{Generation: gen, Reads: make([]claimRead, 0, len(picks))}
		for _, issue := range picks {
			res.Reads = append(res.Reads, claimRead{Issue: issue, Err: errors.New("orchestrator: claim check panicked")})
		}
		o.sendEventBounded(OrchestratorEvent{Type: EventClaimChecked, ClaimBatch: res}, "")
	})
}

// applyClaimBatch settles one off-loop claim-read batch. Event loop.
func (o *Orchestrator) applyClaimBatch(ctx context.Context, state State, res *claimBatchResult, now time.Time) State {
	if res == nil {
		return state
	}
	reads := make([]claimRead, 0, len(res.Reads))
	for _, r := range res.Reads {
		pending, ok := state.ClaimChecks[r.Issue.ID]
		if !ok || pending.Generation != res.Generation {
			// The watchdog released this pick; a later poll picks it again.
			continue
		}
		delete(state.ClaimChecks, r.Issue.ID)
		reads = append(reads, r)
	}
	dispatched, held := 0, 0
	for _, r := range reads {
		if AvailableSlots(state) <= 0 {
			slog.Debug("orchestrator: no slots left for a claim-checked pick; a later poll picks it again",
				"identifier", r.Issue.Identifier)
			continue
		}
		clearBackendHold(&state, r.Issue.Identifier)
		if reason := IneligibleReason(r.Issue, state, o.cfg); reason != "" {
			slog.Info("orchestrator: issue no longer eligible after its claim read, skipping",
				"identifier", r.Issue.Identifier, "reason", reason)
			continue
		}
		d, h := o.admitClaimRead(ctx, &state, r.Issue, r.Detailed, r.Err, now)
		if d {
			dispatched++
		} else if h {
			held++
		}
	}
	o.forgetLoggedHolds(state)
	if len(reads) > 0 {
		slog.Info("orchestrator: claim-checked dispatch complete",
			"picked", len(reads), "dispatched", dispatched, "held", held,
			"running", len(state.Running), "slots_remaining", AvailableSlots(state))
	}
	return state
}

// admitClaimRead starts issue on its claim read (detailed, err): the #116
// claim check, the input-required recovery, then dispatch. It reports whether
// a worker started, or the dispatch gate held the issue. Event loop.
func (o *Orchestrator) admitClaimRead(ctx context.Context, state *State, issue domain.Issue, detailed *domain.Issue, err error, now time.Time) (dispatched, held bool) {
	if errors.Is(err, tracker.ErrNotFound) || (err == nil && detailed == nil) {
		slog.Info("orchestrator: claim check: the tracker no longer has the issue; not starting it",
			"identifier", issue.Identifier, "path", admitDispatch)
		o.forgetLocally(issue.ID)
		return false, false
	}
	if err != nil {
		slog.Warn("orchestrator: claim check: tracker read failed, not starting the issue now",
			"identifier", issue.Identifier, "error", err)
		return false, false
	}
	claimed, verdict := o.claimCheck(ctx, state, issue, detailed, admitDispatch, func(v domain.Issue) string {
		return IneligibleReason(v, *state, o.cfg)
	}, now)
	if verdict != claimAdmit {
		return false, false
	}
	issue = claimed
	// Guard: if the latest comment is an unresolved Itervox input-required
	// comment, restore the issue to InputRequiredIssues instead of dispatching
	// a fresh worker. This recovers from daemon restarts / state loss.
	// #252: `itervox run --answer/--approve` brings the answer itself.
	if entry := recoverInputRequired(issue, detailed); entry != nil && !o.oneShotAnswers() {
		state.InputRequiredIssues[issue.Identifier] = entry
		// Recovery path: no live RunEntry — the previous worker is gone
		// (daemon restart / state loss). B1 self-reentry guard does not
		// apply here; pass nil so the dispatch proceeds normally.
		o.dispatchMatchingInputRequiredAutomations(ctx, state, issue, entry, now, nil)
		slog.Info("orchestrator: recovered input-required from tracker comment",
			"identifier", issue.Identifier)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLine("INFO",
				"worker: awaiting user input (recovered from tracker comment)"))
		}
		return false, false
	}
	_, wasRunning := state.Running[issue.ID]
	*state = o.dispatch(ctx, *state, issue, 0)
	// CORE-173 b: a hold (or any other refusal) is not a dispatch.
	if _, running := state.Running[issue.ID]; running && !wasRunning {
		return true, false
	}
	_, isHeld := state.BackendLimitedHolds[issue.Identifier]
	return false, isHeld
}
