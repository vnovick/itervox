package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/prwatch"
)

// review_continue.go — review and continue: the operator's whole draft
// review of an issue's diff (line and range comments plus an overall note)
// goes to the agent in one action.
//
//   - A live run gets it as steering: one message carrying every comment
//     (file and lines, the quoted diff lines, the comment) and the note,
//     split into consecutive parts only when it exceeds the session message
//     limit.
//   - With no live run it is queued for the issue's next worker run, which
//     finds it as a handoff file (<ISO8601>_operator-review.md); the issue
//     is then dispatched, by default after the board's Dispatch move to the
//     first active state, or in place without a tracker write.
//   - With alsoPostOnPR and a PR the comments are also posted as ONE GitHub
//     review (event "comment") through the PR-review path. A PR failure is
//     reported next to the agent outcome; it never undoes it.
//
// Operator scope (route_scopes.go). Comment text is never logged and never
// put on the feed.

// CapabilityReviewContinue is advertised in StateSnapshot.Capabilities when
// the daemon serves POST .../review-continue.
const CapabilityReviewContinue = "review_continue"

// Agent delivery kinds of a review-continue response.
const (
	ReviewDeliverySteered = "steered"
	ReviewDeliveryQueued  = "queued"
)

// Errors a ReviewContinueClient returns.
var (
	ErrReviewContinueRunLive       = errors.New("the issue has a live run")
	ErrReviewContinueInputRequired = errors.New("the issue is waiting for input")
	ErrReviewContinueIssueClosed   = errors.New("the issue is closed")
	ErrReviewContinueNoActiveState = errors.New("no active state is configured")
	ErrReviewContinueIssueNotFound = errors.New("issue not found")
)

// ReviewContinueComment is one comment of a review-continue request.
type ReviewContinueComment struct {
	ReviewLineComment
	// Quote is the selected diff lines, quoted to the agent.
	Quote string `json:"quote,omitempty"`
}

// ReviewContinueRequest is POST /api/v1/issues/{identifier}/review-continue.
type ReviewContinueRequest struct {
	Comments []ReviewContinueComment `json:"comments,omitempty"`
	Note     string                  `json:"note,omitempty"`
	// MoveToActive moves an issue outside the active states to the first
	// active state before its run (the board's Dispatch). Defaults to true.
	MoveToActive *bool `json:"moveToActive,omitempty"`
	// AlsoPostOnPR also posts the comments as one PR review. Defaults to
	// false.
	AlsoPostOnPR bool `json:"alsoPostOnPR,omitempty"`
}

// ReviewContinueInput is what the daemon queues for the next run.
type ReviewContinueInput struct {
	Note         string
	Comments     []ReviewContinueComment
	MoveToActive bool
}

// ReviewContinueOutcome is how a queued review will reach its run.
type ReviewContinueOutcome struct {
	QueuedAt time.Time
	// MovedTo is the active state the issue was moved to; "" when it was not
	// moved (already active, or moveToActive=false).
	MovedTo string
	// Resumed is set when the issue was paused and was resumed for it.
	Resumed bool
}

// ReviewContinueClient queues a review for the issue's next worker run and
// dispatches the issue. cmd/itervox adapts the orchestrator; safe off the
// event loop.
type ReviewContinueClient interface {
	ContinueFromReview(ctx context.Context, identifier string, in ReviewContinueInput) (ReviewContinueOutcome, error)
}

// ReviewAgentOutcome is the agent half of a review-continue response.
type ReviewAgentOutcome struct {
	// Delivery is "steered" (the live run got it) or "queued" (the next run
	// will).
	Delivery string `json:"delivery"`
	RunID    string `json:"runId,omitempty"`
	// Messages is how many steering messages carried it.
	Messages int        `json:"messages,omitempty"`
	QueuedAt *time.Time `json:"queuedAt,omitempty"`
	MovedTo  string     `json:"movedTo,omitempty"`
	Resumed  bool       `json:"resumed,omitempty"`
}

// ReviewPROutcome is the PR half of a review-continue response.
type ReviewPROutcome struct {
	Posted   bool   `json:"posted"`
	PRURL    string `json:"prUrl,omitempty"`
	URL      string `json:"url,omitempty"`
	Comments int    `json:"comments,omitempty"`
	// Error is why it was not posted ("no_pr", "pr_closed",
	// "github_rejected", ...); Message explains it.
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// ReviewContinueResponse is the 202 body of POST .../review-continue.
type ReviewContinueResponse struct {
	Agent ReviewAgentOutcome `json:"agent"`
	PR    *ReviewPROutcome   `json:"pr,omitempty"`
}

// validateReviewContinue normalizes req and reports the first invalid field.
func validateReviewContinue(req *ReviewContinueRequest) (field string, err error) {
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case len(req.Note) > maxReviewCommentBytes:
		return "note", fmt.Errorf("note exceeds %d KiB", maxReviewCommentBytes/1024)
	case len(req.Comments) > maxReviewBatchComments:
		return "comments", fmt.Errorf("a review carries at most %d comments", maxReviewBatchComments)
	case req.Note == "" && len(req.Comments) == 0:
		return "comments", errors.New("a review needs a comment or a note")
	}
	for i := range req.Comments {
		if f, err := normalizeLineComment(&req.Comments[i].ReviewLineComment); err != nil {
			return "comments." + f, fmt.Errorf("comment %d: %w", i, err)
		}
		if len(req.Comments[i].Quote) > maxReviewQuoteBytes {
			req.Comments[i].Quote = cutUTF8(req.Comments[i].Quote, maxReviewQuoteBytes) + "\n… (quote cut)"
		}
	}
	return "", nil
}

// cutUTF8 cuts s to at most n bytes on a rune boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// reviewSteerPiece renders one comment for a steering message; quote is
// cut to at most quoteRoom bytes (0 drops it).
func reviewSteerPiece(i int, c ReviewContinueComment, quoteRoom int) string {
	head := fmt.Sprintf("%d. %s, %s\n", i+1, c.Path, lineSpan(c.ReviewLineComment))
	quote := strings.TrimRight(c.Quote, "\n")
	if quoteRoom <= 0 || strings.TrimSpace(quote) == "" {
		return head + c.Body + "\n"
	}
	if len(quote) > quoteRoom {
		quote = cutUTF8(quote, quoteRoom) + "\n… (quote cut)"
	}
	fence := "```"
	for strings.Contains(quote, fence) {
		fence += "`"
	}
	return head + fence + "diff\n" + quote + "\n" + fence + "\n" + c.Body + "\n"
}

// SteerMessagesForReview renders a review as steering: one message with the
// note and every comment, quotes shortened to fit the session message
// limit, and split into numbered parts only when the comments alone do not
// fit one message.
func SteerMessagesForReview(note string, comments []ReviewContinueComment) []string {
	limit := maxSessionMessageBytes - 128 // room for a part header
	header := fmt.Sprintf("Operator review of your changes (%s). Address each comment:\n", pluralComments(len(comments)))
	if note != "" {
		header += "\nOverall: " + note + "\n"
	}
	render := func(quoteRoom int) []string {
		pieces := make([]string, len(comments))
		for i, c := range comments {
			pieces[i] = reviewSteerPiece(i, c, quoteRoom)
		}
		return pieces
	}
	fits := func(pieces []string) bool {
		n := len(header)
		for _, p := range pieces {
			n += len(p) + 1
		}
		return n <= limit
	}
	pieces := render(maxReviewQuoteBytes)
	for room := maxReviewQuoteBytes / 2; !fits(pieces) && room >= 64; room /= 2 {
		pieces = render(room)
	}
	if !fits(pieces) {
		pieces = render(0)
	}
	// Pack the pieces into as few messages as the limit allows.
	var msgs []string
	cur := header
	for _, p := range pieces {
		if len(cur)+len(p)+1 > limit && cur != "" {
			msgs = append(msgs, cur)
			cur = ""
		}
		if len(p)+1 > limit {
			p = cutUTF8(p, limit-32) + "\n… (cut)\n"
		}
		cur += "\n" + p
	}
	if strings.TrimSpace(cur) != "" {
		msgs = append(msgs, cur)
	}
	if len(msgs) > 1 {
		for i := range msgs {
			msgs[i] = fmt.Sprintf("[Operator review, part %d of %d]\n", i+1, len(msgs)) + msgs[i]
		}
	}
	return msgs
}

func pluralComments(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return fmt.Sprintf("%d comments", n)
}

// handleReviewContinue serves POST /api/v1/issues/{identifier}/review-continue.
func (s *Server) handleReviewContinue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "identifier")
	var req ReviewContinueRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if field, err := validateReviewContinue(&req); err != nil {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", err.Error(), field)
		return
	}
	moveToActive := req.MoveToActive == nil || *req.MoveToActive

	agent, ok := s.deliverReviewToAgent(w, r.Context(), id, req, moveToActive)
	if !ok {
		return
	}
	resp := ReviewContinueResponse{Agent: agent}
	if req.AlsoPostOnPR {
		resp.PR = s.postReviewOnPR(r.Context(), id, req)
	}
	via := ReviewDestinationAgent
	s.recordReviewFeed(ReviewFeedEvent{Kind: FeedKindReviewCommentPosted, Identifier: id, Via: via,
		RunID: agent.RunID, Count: len(req.Comments)})
	slog.Info("change review: review sent to the agent", "identifier", id, "delivery", agent.Delivery,
		"comments", len(req.Comments), "moved_to", agent.MovedTo, "also_pr", req.AlsoPostOnPR,
		"pr_posted", resp.PR != nil && resp.PR.Posted)
	writeJSON(w, http.StatusAccepted, resp)
}

// deliverReviewToAgent steers the live run, else queues the review for
// the next run. It writes the error response and returns false on failure.
func (s *Server) deliverReviewToAgent(w http.ResponseWriter, ctx context.Context, id string, req ReviewContinueRequest, moveToActive bool) (ReviewAgentOutcome, bool) {
	// Twice at most: a run that starts between the live check and the
	// queueing is steered instead.
	for range 2 {
		if runID := s.issueRunID(id); runID != "" && s.sessions != nil {
			out, err := s.steerReview(runID, req)
			if err == nil {
				return out, true
			}
			if !errors.Is(err, ErrSessionNotFound) {
				writeSteerError(w, err)
				return out, false
			}
			// The run ended meanwhile: queue it for the next one.
		}
		cont, ok := s.changeReview.(ReviewContinueClient)
		if s.changeReview == nil || !ok {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "review and continue is not available")
			return ReviewAgentOutcome{}, false
		}
		out, err := cont.ContinueFromReview(ctx, id, ReviewContinueInput{
			Note: req.Note, Comments: req.Comments, MoveToActive: moveToActive})
		switch {
		case err == nil:
			at := out.QueuedAt
			return ReviewAgentOutcome{Delivery: ReviewDeliveryQueued, QueuedAt: &at, MovedTo: out.MovedTo, Resumed: out.Resumed}, true
		case errors.Is(err, ErrReviewContinueRunLive):
			continue
		case errors.Is(err, ErrReviewContinueInputRequired):
			writeError(w, http.StatusConflict, "input_required",
				"the agent is waiting for input: reply with POST /api/v1/issues/{identifier}/provide-input")
		case errors.Is(err, ErrReviewContinueIssueClosed):
			writeError(w, http.StatusConflict, "issue_closed",
				"the issue is in a terminal state: move it to an active state (moveToActive) to continue it")
		case errors.Is(err, ErrReviewContinueNoActiveState):
			writeError(w, http.StatusConflict, "no_active_state", "no active state is configured to move the issue to")
		case errors.Is(err, ErrReviewContinueIssueNotFound):
			writeError(w, http.StatusNotFound, "not_found", "issue not found")
		default:
			writeClientError(w, "review_continue_failed", issueError(id, err))
		}
		return ReviewAgentOutcome{}, false
	}
	writeError(w, http.StatusConflict, "run_changed", "the issue's run changed while sending; try again")
	return ReviewAgentOutcome{}, false
}

// steerReview sends the review to the live run as steering.
func (s *Server) steerReview(runID string, req ReviewContinueRequest) (ReviewAgentOutcome, error) {
	msgs := SteerMessagesForReview(req.Note, req.Comments)
	for i, m := range msgs {
		if _, err := s.sessions.SendSessionMessage(runID, m, true); err != nil {
			if i > 0 {
				// Part of it was delivered: report that, not a clean retry.
				return ReviewAgentOutcome{}, fmt.Errorf("%w (after %d of %d parts)", err, i, len(msgs))
			}
			return ReviewAgentOutcome{}, err
		}
	}
	return ReviewAgentOutcome{Delivery: ReviewDeliverySteered, RunID: runID, Messages: len(msgs)}, nil
}

func writeSteerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSessionInputRequired):
		writeError(w, http.StatusConflict, "input_required",
			"the agent is waiting for input: reply with POST /api/v1/issues/{identifier}/provide-input")
	default:
		writeBusyOr404(w, err, "no_live_run", "the issue's run has ended")
	}
}

// postReviewOnPR posts the review's comments as one PR review (event
// "comment"). Every failure is reported in the outcome.
func (s *Server) postReviewOnPR(ctx context.Context, id string, req ReviewContinueRequest) *ReviewPROutcome {
	prURL, ref, ok := s.issuePRRef(id)
	if !ok {
		return &ReviewPROutcome{Error: "no_pr", Message: "the issue has no GitHub pull request"}
	}
	out := &ReviewPROutcome{PRURL: prURL}
	pr, err := s.github.PR(ctx, ref)
	if err != nil {
		out.Error, out.Message = githubErrorCode(err)
		return out
	}
	if pr.Merged || strings.EqualFold(pr.State, "closed") {
		out.Error, out.Message = "pr_closed", "the pull request is closed or merged"
		return out
	}
	review := prwatch.NewReview{Event: prwatch.ReviewComment, Body: req.Note, CommitID: pr.Head.SHA,
		Comments: make([]prwatch.LineComment, 0, len(req.Comments))}
	for _, c := range req.Comments {
		review.Comments = append(review.Comments, githubLineComment(c.ReviewLineComment))
	}
	created, err := s.github.CreateReview(ctx, ref, review)
	if err != nil {
		out.Error, out.Message = githubErrorCode(err)
		return out
	}
	s.recordReviewFeed(ReviewFeedEvent{Kind: FeedKindPRReviewByOperator, Identifier: id, PRURL: prURL,
		Event: "comment", Count: len(review.Comments)})
	out.Posted, out.URL, out.Comments = true, created.HTMLURL, len(review.Comments)
	return out
}

// githubErrorCode is writeGitHubError's code and message for an outcome
// body.
func githubErrorCode(err error) (string, string) {
	if ae, ok := prwatch.IsAPIError(err); ok && (ae.Status == http.StatusUnprocessableEntity || ae.Status == http.StatusForbidden) {
		return "github_rejected", "GitHub refused it: " + truncateForReason(ae.Message)
	}
	if errors.Is(err, prwatch.ErrNotFound) {
		return "pr_not_found", "the pull request is not readable with the daemon's GitHub credentials"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "github_timeout", "GitHub did not answer in time"
	}
	return "github_failed", "the GitHub request failed"
}
