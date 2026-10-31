package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// changeReviewAdapter backs change review (#238) with the orchestrator:
// snapshot reads only, and feed events through the non-blocking
// RecordFeedEvent. Never the event loop's State.
type changeReviewAdapter struct {
	orch *orchestrator.Orchestrator
	// tr reads the issue a review continues; moveState is the board's
	// Dispatch move (orchestratorAdapter.UpdateIssueState). Both nil in
	// tests that do not continue reviews.
	tr        tracker.Tracker
	moveState func(ctx context.Context, identifier, state string) error
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

// queuedReviewRows lists the operator reviews waiting for a run, oldest
// first: counts only, never the review text.
func queuedReviewRows(m map[string]orchestrator.ReviewContinuation) []server.QueuedReviewRow {
	if len(m) == 0 {
		return nil
	}
	rows := make([]server.QueuedReviewRow, 0, len(m))
	for id, rc := range m {
		rows = append(rows, server.QueuedReviewRow{Identifier: id, Comments: len(rc.Comments),
			HasNote: strings.TrimSpace(rc.Note) != "", MoveToActive: rc.MoveToActive, QueuedAt: rc.QueuedAt})
	}
	slices.SortFunc(rows, func(a, b server.QueuedReviewRow) int {
		if c := a.QueuedAt.Compare(b.QueuedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Identifier, b.Identifier)
	})
	return rows
}

// ContinueFromReview implements server.ReviewContinueClient: it queues the
// review for the issue's next worker run (through the event loop), resumes
// a paused issue, and with in.MoveToActive moves an issue outside the
// active states to the first one through the same path as the board's
// Dispatch. A failed move takes the queued review back.
func (c changeReviewAdapter) ContinueFromReview(ctx context.Context, identifier string, in server.ReviewContinueInput) (server.ReviewContinueOutcome, error) {
	if c.tr == nil || c.moveState == nil {
		return server.ReviewContinueOutcome{}, errors.New("review and continue is not wired")
	}
	issue, err := c.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		if errors.Is(err, tracker.ErrNotFound) {
			return server.ReviewContinueOutcome{}, server.ErrReviewContinueIssueNotFound
		}
		return server.ReviewContinueOutcome{}, fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return server.ReviewContinueOutcome{}, server.ErrReviewContinueIssueNotFound
	}
	active, terminal, completion := c.orch.TrackerStatesCfg()
	isActive := slices.ContainsFunc(active, func(s string) bool { return strings.EqualFold(s, issue.State) })
	move := in.MoveToActive && !isActive
	if move && len(active) == 0 {
		return server.ReviewContinueOutcome{}, server.ErrReviewContinueNoActiveState
	}
	if !move && !isActive && domain.IsTerminalState(issue.State, terminal) && !strings.EqualFold(issue.State, completion) {
		return server.ReviewContinueOutcome{}, server.ErrReviewContinueIssueClosed
	}
	comments := make([]orchestrator.ReviewContinueComment, 0, len(in.Comments))
	for _, cm := range in.Comments {
		comments = append(comments, orchestrator.ReviewContinueComment{Path: cm.Path, Line: cm.Line,
			StartLine: cm.StartLine, Side: cm.Side, StartSide: cm.StartSide, Body: cm.Body, Quote: cm.Quote})
	}
	// An issue in an active state is dispatched by the normal tick either
	// way; only an issue left where it is takes the in-place path.
	queuedAt, err := c.orch.QueueReviewContinuation(ctx, orchestrator.ReviewContinuation{
		IssueID: issue.ID, Identifier: identifier, Note: in.Note, Comments: comments,
		MoveToActive: move || isActive,
	})
	switch {
	case errors.Is(err, orchestrator.ErrReviewContinueRunLive):
		return server.ReviewContinueOutcome{}, server.ErrReviewContinueRunLive
	case errors.Is(err, orchestrator.ErrReviewContinueInputRequired):
		return server.ReviewContinueOutcome{}, server.ErrReviewContinueInputRequired
	case err != nil:
		return server.ReviewContinueOutcome{}, err
	}
	out := server.ReviewContinueOutcome{QueuedAt: queuedAt}
	if _, paused := c.orch.Snapshot().PausedIdentifiers[identifier]; paused {
		out.Resumed = c.orch.ResumeIssue(identifier) == nil
	}
	if move {
		mctx := server.WithIssueStatusSource(ctx, server.IssueStatusSourceDashboard)
		if err := c.moveState(mctx, identifier, active[0]); err != nil {
			_ = c.orch.DropReviewContinuation(context.WithoutCancel(ctx), identifier, queuedAt)
			return server.ReviewContinueOutcome{}, fmt.Errorf("move the issue to %q: %w", active[0], err)
		}
		out.MovedTo = active[0]
	}
	c.orch.Refresh()
	return out, nil
}
