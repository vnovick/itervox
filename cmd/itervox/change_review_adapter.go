package main

import (
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// changeReviewAdapter backs change review (#238) with the orchestrator:
// snapshot reads only, and feed events through the non-blocking
// RecordFeedEvent. Never the event loop's State.
type changeReviewAdapter struct {
	orch *orchestrator.Orchestrator
}

func (c changeReviewAdapter) IssuePR(identifier string) string {
	return c.orch.IssuePRURL(identifier)
}

func (c changeReviewAdapter) WorktreePath(identifier string) string {
	return c.orch.WorktreePath(identifier)
}

func (c changeReviewAdapter) ReviewOutcome(identifier string) *server.ReviewOutcomeRow {
	if row := reviewRow(c.orch.Snapshot(), identifier); row != nil {
		return row.Outcome
	}
	return nil
}

func (c changeReviewAdapter) RecordReviewFeed(ev server.ReviewFeedEvent) bool {
	return c.orch.RecordFeedEvent(reviewFeedEvent(ev))
}

// reviewFeedEvent is the feed event of a change-review action: the issue,
// the PR, the destination, the review event and a count. No comment text
// exists in ev to carry.
func reviewFeedEvent(ev server.ReviewFeedEvent) orchestrator.FeedEvent {
	kind := orchestrator.FeedReviewCommentPosted
	if ev.Kind == server.FeedKindPRReviewByOperator {
		kind = orchestrator.FeedPRReviewSubmittedByOperator
	}
	return orchestrator.FeedEvent{Kind: kind, Identifier: ev.Identifier, Payload: orchestrator.FeedPayload{
		PRURL: ev.PRURL, Via: ev.Via, RunID: ev.RunID, Verdict: ev.Event, Count: ev.Count,
	}}
}
