package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
)

// The claim check (#116). List reads may come from the local tracker store,
// which can be up to one sync behind the tracker. Just before a worker is
// started for an issue, the issue is read once more from the tracker by ID
// and the start goes ahead only if it is still eligible there: an issue a
// person closed, moved out of an active state or deleted since the last pull
// is not started. Every admission path runs it (dispatch, retries, reviewers,
// automations, input resumes, the backend-fallback re-dispatch); a path that
// already read the issue by ID for its own reasons passes that read in, so
// the check costs no extra request there.
//
// A read by ID also refreshes the store's copy (the store applies every by-ID
// result), so the next tick sees the person's change too.

// Admission paths, for logs and status history.
const (
	admitDispatch    = "dispatch"
	admitRetry       = "retry"
	admitReviewer    = "reviewer"
	admitAutomation  = "automation"
	admitInputResume = "input_resume"
	admitFallback    = "backend_fallback"
)

// Status-history reasons recorded by the claim check.
const (
	// ClaimReasonChangedInTracker: the tracker's state differed from the
	// local copy when the issue was about to start.
	ClaimReasonChangedInTracker = "claim_check"
	// ClaimReasonPendingWriteDropped: a queued local state write was dropped
	// because a person changed the issue in the tracker after it was queued.
	ClaimReasonPendingWriteDropped = "pending_write_dropped"
)

// claimRead reads issue's current state from the tracker by ID. found is
// false when the tracker no longer returns it (deleted, or moved out of the
// tracked scope). ok is false when the read failed; the caller does not start
// the issue then, because a change it cannot see might be pending.
func (o *Orchestrator) claimRead(ctx context.Context, issue domain.Issue) (fresh domain.Issue, found, ok bool) {
	issues, err := o.tracker.FetchIssueStatesByIDs(ctx, []string{issue.ID})
	if err != nil {
		slog.Warn("orchestrator: claim check: tracker read failed, not starting the issue now",
			"identifier", issue.Identifier, "error", err)
		return domain.Issue{}, false, false
	}
	for _, iss := range issues {
		if iss.ID == issue.ID {
			return mergeFresh(issue, iss), true, true
		}
	}
	return domain.Issue{}, false, true
}

// mergeFresh is local with the tracker-owned fields of a by-ID read. A state
// read may carry only the ID, state and update time, so other fields keep the
// local copy's values where the read has none.
func mergeFresh(local, fresh domain.Issue) domain.Issue {
	out := local
	out.State = fresh.State
	if fresh.UpdatedAt != nil {
		out.UpdatedAt = fresh.UpdatedAt
	}
	if fresh.Title != "" {
		out.Title = fresh.Title
	}
	if fresh.Labels != nil {
		out.Labels = fresh.Labels
	}
	if fresh.BlockedBy != nil {
		out.BlockedBy = fresh.BlockedBy
	}
	return out
}

// claimView is the issue as the orchestrator should see it, given fresh, the
// tracker's current copy: pending local state writes that a person's later
// change superseded are dropped (the person's change wins), and a write that
// is still pending is laid over the tracker's state, as on every tick. local
// is the copy the decision was about to be made from; a difference from the
// tracker is logged and recorded in the issue's status history. Event loop.
func (o *Orchestrator) claimView(state *State, local, fresh domain.Issue, path string, now time.Time) domain.Issue {
	view := fresh
	if o.outbox != nil {
		for _, entry := range o.outbox.PendingFor(fresh.ID) {
			if entry.Kind != outbox.KindUpdateState {
				continue
			}
			drop, reason := outbox.ReconcileVerdict(entry, fresh.State, fresh.UpdatedAt)
			if !drop {
				continue
			}
			o.outbox.Drop(entry.ID, reason)
			if reason != "superseded_by_tracker" {
				continue
			}
			o.logger().Warn("orchestrator: claim check: a newer change in the tracker wins over a pending local write; the write is dropped",
				"issue_identifier", fresh.Identifier, "pending_state", entry.TargetState,
				"tracker_state", fresh.State, "path", path)
			appendIssueStatusChange(state, IssueStatusChange{
				Identifier: fresh.Identifier, FromState: entry.TargetState, ToState: fresh.State,
				Source: StatusSourceTrackerObserved, Reason: ClaimReasonPendingWriteDropped, At: now,
			})
		}
		if target, pending := lastPendingUpdateStateTarget(o.outbox.PendingFor(fresh.ID)); pending {
			view.State = target
		}
	}
	if local.State != "" && !strings.EqualFold(local.State, view.State) {
		slog.Info("orchestrator: claim check: the issue changed in the tracker since the last pull",
			"identifier", fresh.Identifier, "local_state", local.State, "tracker_state", view.State, "path", path)
		appendIssueStatusChange(state, IssueStatusChange{
			Identifier: fresh.Identifier, FromState: local.State, ToState: view.State,
			Source: StatusSourceTrackerObserved, Reason: ClaimReasonChangedInTracker, At: now,
		})
	}
	return view
}

// claimCheck reads issue from the tracker (or uses fresh, the caller's own
// read by ID in this admission) and reports whether it may start: eligible
// returns "" for an issue that may, or the reason it may not. It returns the
// issue to start, with the tracker's current state. Event loop.
func (o *Orchestrator) claimCheck(ctx context.Context, state *State, issue domain.Issue, fresh *domain.Issue, path string, eligible func(domain.Issue) string, now time.Time) (domain.Issue, bool) {
	var current domain.Issue
	switch {
	case fresh != nil:
		current = mergeFresh(issue, *fresh)
	case o.tracker == nil:
		// No tracker to check against (an orchestrator built without one).
		current = issue
	default:
		read, found, ok := o.claimRead(ctx, issue)
		if !ok {
			return issue, false
		}
		if !found {
			slog.Info("orchestrator: claim check: the tracker no longer has the issue; not starting it",
				"identifier", issue.Identifier, "path", path)
			return issue, false
		}
		current = read
	}
	view := o.claimView(state, issue, current, path, now)
	if reason := eligible(view); reason != "" {
		slog.Info("orchestrator: claim check: not starting the issue, it is no longer eligible in the tracker",
			"identifier", view.Identifier, "tracker_state", view.State, "reason", reason, "path", path)
		return view, false
	}
	return view, true
}

// claimActive admits an issue in an active state: a worker's dispatch, retry
// or re-dispatch.
func claimActive(state *State) func(domain.Issue) string {
	return func(iss domain.Issue) string {
		if !isActiveState(iss.State, *state) {
			return "not_active"
		}
		return ""
	}
}

// claimNotTerminal admits an issue anywhere but a terminal state. A reviewer
// is also admitted in a terminal tracker.completion_state, where a finished
// run leaves the issue for review.
func (o *Orchestrator) claimNotTerminal(state *State, reviewer bool) func(domain.Issue) string {
	return func(iss domain.Issue) string {
		if !isTerminalState(iss.State, *state) {
			return ""
		}
		if reviewer {
			if _, _, completion := o.TrackerStatesCfg(); completion != "" && strings.EqualFold(iss.State, completion) {
				return ""
			}
		}
		return "terminal_state"
	}
}
