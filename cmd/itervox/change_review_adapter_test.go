package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// TestReviewFeedEventCarriesNoCommentText (#238): change-review feed
// events map to their feed kinds with the issue, the PR, the destination,
// the review event and a count only.
func TestReviewFeedEventCarriesNoCommentText(t *testing.T) {
	ev := reviewFeedEvent(server.ReviewFeedEvent{Kind: server.FeedKindReviewCommentPosted, Identifier: "ENG-1", Via: "agent", RunID: "run-1"})
	assert.Equal(t, orchestrator.FeedEvent{Kind: orchestrator.FeedReviewCommentPosted, Identifier: "ENG-1",
		Payload: orchestrator.FeedPayload{Via: "agent", RunID: "run-1"}}, ev)

	ev = reviewFeedEvent(server.ReviewFeedEvent{Kind: server.FeedKindPRReviewByOperator, Identifier: "ENG-1",
		PRURL: "https://github.com/acme/app/pull/7", Event: "approve", Count: 2})
	assert.Equal(t, orchestrator.FeedPRReviewSubmittedByOperator, ev.Kind)
	assert.Equal(t, orchestrator.FeedPayload{PRURL: "https://github.com/acme/app/pull/7", Verdict: "approve", Count: 2}, ev.Payload)
	assert.Equal(t, server.FeedKindReviewCommentPosted, orchestrator.FeedReviewCommentPosted, "kind names agree")
	assert.Equal(t, server.FeedKindPRReviewByOperator, orchestrator.FeedPRReviewSubmittedByOperator)
}
