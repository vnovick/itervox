package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/prwatch"
)

// change_review.go — #238: acting on the diff of #215 without leaving
// Itervox. A line or range comment goes either to the running agent as a
// steering message quoting the file and lines (through the #178 session
// steering path), or to the issue's pull request as a GitHub review
// comment. An operator review (approve / request changes / comment) is ONE
// GitHub review carrying the batched line comments.
//
// Invariants:
//   - Both routes are operator scope (route_scopes.go); reading the diff
//     stays viewer.
//   - GitHub is reached only through PRReviewAPI (the PR-event watcher's
//     client and token source): no second credential path.
//   - Nothing here merges, records a reviewer verdict or changes the
//     reviewer quorum. The merge gate (merge_pr: required checks, block
//     labels, strategy) and agent.review_quorum are untouched; the review
//     response only reports them.
//   - Comment text is never logged and never put on the feed: the feed
//     events carry the issue, the PR URL, the destination and a count.

// CapabilityChangeReview is advertised in StateSnapshot.Capabilities when
// the daemon serves the change-review routes (#238).
const CapabilityChangeReview = "change_review"

// Diff sources (GET .../diff?source=).
const (
	DiffSourceWorktree = "worktree"
	DiffSourcePR       = "pr"
)

// Review comment destinations.
const (
	ReviewDestinationAgent = "agent"
	ReviewDestinationPR    = "pr"
)

// Bounds on operator review input.
const (
	maxReviewCommentBytes  = 8 * 1024
	maxReviewQuoteBytes    = 4 * 1024
	maxReviewPathBytes     = 512
	maxReviewBatchComments = 50
)

// ReviewFeedEvent is one change-review feed event: never the comment text.
type ReviewFeedEvent struct {
	// Kind is "review_comment_posted" or "pr_review_submitted_by_operator".
	Kind       string
	Identifier string
	PRURL      string
	// Via is the comment's destination: "agent" or "github".
	Via   string
	RunID string
	// Event is the review event (approve, request_changes, comment).
	Event string
	// Count is the number of line comments a review carried.
	Count int
}

// Feed kinds of the change-review events.
const (
	FeedKindReviewCommentPosted = "review_comment_posted"
	FeedKindPRReviewByOperator  = "pr_review_submitted_by_operator"
)

// ChangeReviewClient is what the change-review routes read from the
// daemon. cmd/itervox adapts the orchestrator; every method is safe off the
// event loop.
type ChangeReviewClient interface {
	// IssuePR is the issue's pull request URL ("" for none).
	IssuePR(identifier string) string
	// WorktreePath is the issue's worktree directory on the daemon's host
	// ("" for none), for the dashboard's open-in-editor links.
	WorktreePath(identifier string) string
	// ReviewOutcome is the agent reviewers' quorum result (nil for none).
	ReviewOutcome(identifier string) *ReviewOutcomeRow
	// RecordReviewFeed records a feed event without blocking.
	RecordReviewFeed(ev ReviewFeedEvent) bool
}

// PRReviewAPI is the GitHub surface change review uses; *prwatch.Client
// implements it.
type PRReviewAPI interface {
	PR(ctx context.Context, ref prwatch.PRRef) (prwatch.PullRequest, error)
	PRFiles(ctx context.Context, ref prwatch.PRRef) ([]prwatch.PRFile, error)
	CreateReviewComment(ctx context.Context, ref prwatch.PRRef, commitID string, lc prwatch.LineComment) (prwatch.Created, error)
	CreateReview(ctx context.Context, ref prwatch.PRRef, r prwatch.NewReview) (prwatch.Created, error)
}

// ReviewLineComment is one line or range comment. Line is the last line of
// the range, StartLine its first (0 for one line); Side and StartSide are
// "RIGHT" (the new file, the default) or "LEFT" (the base file).
type ReviewLineComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"startLine,omitempty"`
	Side      string `json:"side,omitempty"`
	StartSide string `json:"startSide,omitempty"`
	Body      string `json:"body"`
}

// ReviewCommentRequest is POST /api/v1/issues/{identifier}/review-comments.
type ReviewCommentRequest struct {
	ReviewLineComment
	// Destination is "agent" (the running session) or "pr" (the PR).
	Destination string `json:"destination"`
	// Quote is the selected diff lines, quoted in the agent's message.
	Quote string `json:"quote,omitempty"`
}

// PRReviewRequest is POST /api/v1/issues/{identifier}/pr-review.
type PRReviewRequest struct {
	// Event is "approve", "request_changes" or "comment".
	Event    string              `json:"event"`
	Body     string              `json:"body,omitempty"`
	Comments []ReviewLineComment `json:"comments,omitempty"`
}

// ReviewMergePolicyRow reports the merge policy an operator review leaves
// in place: Itervox never merges from a review, matching block labels
// still refuse merge_pr, and the agent reviewer quorum is unchanged.
type ReviewMergePolicyRow struct {
	MergesFromHere bool              `json:"mergesFromHere"`
	BlockLabels    []string          `json:"blockLabels"`
	Quorum         *ReviewOutcomeRow `json:"quorum"`
}

// PRReviewResponse is the 201 body of POST .../pr-review.
type PRReviewResponse struct {
	PRURL       string               `json:"prUrl"`
	ID          int64                `json:"id"`
	URL         string               `json:"url,omitempty"`
	Event       string               `json:"event"`
	Comments    int                  `json:"comments"`
	Merged      bool                 `json:"merged"`
	MergePolicy ReviewMergePolicyRow `json:"mergePolicy"`
}

var reviewEvents = map[string]string{
	"approve":         prwatch.ReviewApprove,
	"request_changes": prwatch.ReviewRequestChanges,
	"comment":         prwatch.ReviewComment,
}

// normalizeLineComment validates c and fills its defaults.
func normalizeLineComment(c *ReviewLineComment) (field string, err error) {
	c.Path = strings.TrimSpace(c.Path)
	c.Body = strings.TrimSpace(c.Body)
	switch {
	case c.Path == "" || len(c.Path) > maxReviewPathBytes || strings.ContainsAny(c.Path, "\x00\n\r") || strings.HasPrefix(c.Path, "/"):
		return "path", errors.New("path must be a repository-relative file path")
	case c.Line < 1:
		return "line", errors.New("line must be at least 1")
	case c.StartLine < 0:
		return "startLine", errors.New("startLine must not be negative")
	case c.Body == "":
		return "body", errors.New("body is required")
	case len(c.Body) > maxReviewCommentBytes:
		return "body", fmt.Errorf("body exceeds %d KiB", maxReviewCommentBytes/1024)
	}
	c.Side = strings.ToUpper(strings.TrimSpace(c.Side))
	c.StartSide = strings.ToUpper(strings.TrimSpace(c.StartSide))
	c.Side = cmp.Or(c.Side, prwatch.SideRight)
	c.StartSide = cmp.Or(c.StartSide, c.Side)
	for _, side := range []string{c.Side, c.StartSide} {
		if side != prwatch.SideRight && side != prwatch.SideLeft {
			return "side", errors.New("side must be RIGHT or LEFT")
		}
	}
	if c.StartLine == c.Line && c.StartSide == c.Side {
		c.StartLine = 0
	}
	if c.StartLine > 0 && c.StartSide == c.Side && c.StartLine > c.Line {
		return "startLine", errors.New("startLine must not be after line")
	}
	return "", nil
}

// githubLineComment is c in GitHub's review-comment shape.
func githubLineComment(c ReviewLineComment) prwatch.LineComment {
	lc := prwatch.LineComment{Path: c.Path, Body: c.Body, Line: c.Line, Side: c.Side}
	if c.StartLine > 0 {
		lc.StartLine, lc.StartSide = c.StartLine, c.StartSide
	}
	return lc
}

// lineSpan describes c's lines: "line 12", "lines 12-14", with "(base)" for
// the LEFT side.
func lineSpan(c ReviewLineComment) string {
	side := func(s string) string {
		if s == prwatch.SideLeft {
			return " (base)"
		}
		return ""
	}
	switch {
	case c.StartLine == 0:
		return fmt.Sprintf("line %d%s", c.Line, side(c.Side))
	case c.StartSide == c.Side:
		return fmt.Sprintf("lines %d-%d%s", c.StartLine, c.Line, side(c.Side))
	default:
		return fmt.Sprintf("lines %d%s to %d%s", c.StartLine, side(c.StartSide), c.Line, side(c.Side))
	}
}

// SteerMessageForComment is the steering message a line comment becomes:
// the file and lines, the quoted diff lines (cut to fit the session
// message limit), then the comment.
func SteerMessageForComment(c ReviewLineComment, quote string) string {
	head := fmt.Sprintf("Review comment on %s, %s:\n\n", c.Path, lineSpan(c))
	tail := c.Body + "\n"
	quote = strings.TrimRight(quote, "\n")
	room := min(maxReviewQuoteBytes, maxSessionMessageBytes-len(head)-len(tail)-64)
	if len(quote) > room {
		cut := max(room, 0)
		for cut > 0 && !utf8.RuneStart(quote[cut]) {
			cut--
		}
		quote = quote[:cut] + "\n… (quote cut)"
	}
	if room <= 0 || strings.TrimSpace(quote) == "" {
		return head + tail
	}
	fence := "```"
	for strings.Contains(quote, fence) {
		fence += "`"
	}
	return head + fence + "diff\n" + quote + "\n" + fence + "\n\n" + tail
}

// issueRunID is identifier's live run, "" when it has none.
func (s *Server) issueRunID(identifier string) string {
	if s.changes == nil {
		return ""
	}
	if run, ok := s.changes.RunContext(identifier); ok && run.Live {
		return run.RunID
	}
	return ""
}

// issuePRRef is identifier's pull request when change review can reach it.
func (s *Server) issuePRRef(identifier string) (string, prwatch.PRRef, bool) {
	if s.changeReview == nil || s.github == nil {
		return "", prwatch.PRRef{}, false
	}
	u := s.changeReview.IssuePR(identifier)
	ref, ok := prwatch.ParsePRURL(u)
	return u, ref, ok
}

func (s *Server) recordReviewFeed(ev ReviewFeedEvent) {
	if s.changeReview != nil {
		s.changeReview.RecordReviewFeed(ev)
	}
}

// writeGitHubError maps a PRReviewAPI error to its response. GitHub's own
// refusal message is passed on; the request body never is.
func writeGitHubError(w http.ResponseWriter, err error) {
	if ae, ok := prwatch.IsAPIError(err); ok && (ae.Status == http.StatusUnprocessableEntity || ae.Status == http.StatusForbidden) {
		writeError(w, http.StatusUnprocessableEntity, "github_rejected", "GitHub refused it: "+truncateForReason(ae.Message))
		return
	}
	if errors.Is(err, prwatch.ErrNotFound) {
		writeError(w, http.StatusNotFound, "pr_not_found", "the pull request is not readable with the daemon's GitHub credentials")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "github_timeout", "GitHub did not answer in time")
		return
	}
	writeError(w, http.StatusBadGateway, "github_failed", "the GitHub request failed")
}

// openPRHead reads the PR and returns it when it is still open; it writes
// the error response otherwise.
func (s *Server) openPRHead(w http.ResponseWriter, ctx context.Context, ref prwatch.PRRef) (prwatch.PullRequest, bool) {
	pr, err := s.github.PR(ctx, ref)
	if err != nil {
		writeGitHubError(w, err)
		return pr, false
	}
	if pr.Merged || strings.EqualFold(pr.State, "closed") {
		writeError(w, http.StatusConflict, "pr_closed", "the pull request is closed or merged")
		return pr, false
	}
	return pr, true
}

// handleReviewComment serves POST /api/v1/issues/{identifier}/review-comments.
func (s *Server) handleReviewComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "identifier")
	var req ReviewCommentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if field, err := normalizeLineComment(&req.ReviewLineComment); err != nil {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", err.Error(), field)
		return
	}
	switch req.Destination {
	case ReviewDestinationAgent:
		s.sendCommentToAgent(w, id, req)
	case ReviewDestinationPR:
		s.postCommentOnPR(w, r, id, req.ReviewLineComment)
	default:
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", `destination must be "agent" or "pr"`, "destination")
	}
}

func (s *Server) sendCommentToAgent(w http.ResponseWriter, id string, req ReviewCommentRequest) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	runID := s.issueRunID(id)
	if runID == "" {
		writeError(w, http.StatusConflict, "no_live_run", "the issue has no running agent to send the comment to")
		return
	}
	msg, err := s.sessions.SendSessionMessage(runID, SteerMessageForComment(req.ReviewLineComment, req.Quote), true)
	switch {
	case err == nil:
	case errors.Is(err, ErrSessionInputRequired):
		writeError(w, http.StatusConflict, "input_required",
			"the agent is waiting for input: reply with POST /api/v1/issues/{identifier}/provide-input")
		return
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusConflict, "no_live_run", "the issue's run has ended")
		return
	default:
		writeBusyOr404(w, err, "no_live_run", "the issue's run has ended")
		return
	}
	s.recordReviewFeed(ReviewFeedEvent{Kind: FeedKindReviewCommentPosted, Identifier: id, Via: ReviewDestinationAgent, RunID: runID})
	slog.Info("change review: line comment sent to the running agent", "identifier", id, "run_id", runID)
	writeJSON(w, http.StatusAccepted, map[string]any{"destination": ReviewDestinationAgent, "runId": runID, "message": msg})
}

func (s *Server) postCommentOnPR(w http.ResponseWriter, r *http.Request, id string, c ReviewLineComment) {
	prURL, ref, ok := s.issuePRRef(id)
	if !ok {
		writeError(w, http.StatusConflict, "no_pr", "the issue has no GitHub pull request")
		return
	}
	pr, ok := s.openPRHead(w, r.Context(), ref)
	if !ok {
		return
	}
	created, err := s.github.CreateReviewComment(r.Context(), ref, pr.Head.SHA, githubLineComment(c))
	if err != nil {
		writeGitHubError(w, err)
		return
	}
	s.recordReviewFeed(ReviewFeedEvent{Kind: FeedKindReviewCommentPosted, Identifier: id, PRURL: prURL, Via: "github"})
	slog.Info("change review: line comment posted on the pull request", "identifier", id, "pr_url", prURL)
	writeJSON(w, http.StatusCreated, map[string]any{"destination": ReviewDestinationPR, "prUrl": prURL,
		"id": created.ID, "url": created.HTMLURL})
}

// handlePRReview serves POST /api/v1/issues/{identifier}/pr-review: one
// GitHub review with the batched line comments. It never merges.
func (s *Server) handlePRReview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "identifier")
	var req PRReviewRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	event, ok := reviewEvents[strings.ToLower(strings.TrimSpace(req.Event))]
	if !ok {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "event must be approve, request_changes or comment", "event")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	switch {
	case len(req.Body) > maxReviewCommentBytes:
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "body exceeds 8 KiB", "body")
		return
	case len(req.Comments) > maxReviewBatchComments:
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("a review carries at most %d comments", maxReviewBatchComments), "comments")
		return
	case event != prwatch.ReviewApprove && req.Body == "" && len(req.Comments) == 0:
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "request changes and comment need a body or a line comment", "body")
		return
	}
	review := prwatch.NewReview{Event: event, Body: req.Body, Comments: make([]prwatch.LineComment, 0, len(req.Comments))}
	for i := range req.Comments {
		if field, err := normalizeLineComment(&req.Comments[i]); err != nil {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("comment %d: %s", i, err), "comments."+field)
			return
		}
		review.Comments = append(review.Comments, githubLineComment(req.Comments[i]))
	}
	prURL, ref, ok := s.issuePRRef(id)
	if !ok {
		writeError(w, http.StatusConflict, "no_pr", "the issue has no GitHub pull request")
		return
	}
	pr, ok := s.openPRHead(w, r.Context(), ref)
	if !ok {
		return
	}
	review.CommitID = pr.Head.SHA
	created, err := s.github.CreateReview(r.Context(), ref, review)
	if err != nil {
		writeGitHubError(w, err)
		return
	}
	verb := strings.ToLower(event)
	s.recordReviewFeed(ReviewFeedEvent{Kind: FeedKindPRReviewByOperator, Identifier: id, PRURL: prURL,
		Event: verb, Count: len(review.Comments)})
	slog.Info("change review: pull request review submitted", "identifier", id, "pr_url", prURL,
		"event", verb, "comments", len(review.Comments))
	writeJSON(w, http.StatusCreated, PRReviewResponse{
		PRURL: prURL, ID: created.ID, URL: created.HTMLURL, Event: verb, Comments: len(review.Comments),
		MergePolicy: ReviewMergePolicyRow{BlockLabels: s.matchingBlockLabels(pr), Quorum: s.changeReview.ReviewOutcome(id)},
	})
}

// matchingBlockLabels are the PR's labels that refuse merge_pr under the
// configured agent.merge_block_labels (or the defaults).
func (s *Server) matchingBlockLabels(pr prwatch.PullRequest) []string {
	blocked := s.mergeBlockLabels
	if len(blocked) == 0 {
		blocked = DefaultMergeBlockLabels()
	}
	out := []string{}
	for _, l := range pr.Labels {
		for _, b := range blocked {
			if strings.EqualFold(l.Name, b) {
				out = append(out, l.Name)
				break
			}
		}
	}
	return out
}
