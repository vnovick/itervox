package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// continueReview is a ChangeReviewClient that also continues reviews: it
// records what it was asked to queue and answers err.
type continueReview struct {
	*fakeReview
	mu    sync.Mutex
	calls []ReviewContinueInput
	err   error
	out   ReviewContinueOutcome
}

func (c *continueReview) ContinueFromReview(_ context.Context, _ string, in ReviewContinueInput) (ReviewContinueOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, in)
	return c.out, c.err
}

func (c *continueReview) queued() []ReviewContinueInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ReviewContinueInput(nil), c.calls...)
}

// allSteers records every steering message.
type allSteers struct {
	SessionsClient
	mu   sync.Mutex
	msgs []string
}

func (s *allSteers) SendSessionMessage(_ string, text string, _ bool) (SessionMessageRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, text)
	return SessionMessageRow{ID: "m", Text: text, Status: "queued", QueuedAt: time.Now()}, nil
}

func (s *allSteers) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}

type continueRig struct {
	reviewRig
	cont   *continueReview
	steers *allSteers
}

func newContinueRig(t *testing.T, live bool) continueRig {
	t.Helper()
	base := newReviewRig(t)
	base.changes.run.Live = live
	rig := continueRig{reviewRig: base, cont: &continueReview{fakeReview: base.review,
		out: ReviewContinueOutcome{QueuedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), MovedTo: "Todo"}},
		steers: &allSteers{}}
	rig.srv = New(Config{
		Snapshot:     func() StateSnapshot { return StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		APIToken:     scopesTestBootstrap,
		Tokens:       base.srv.tokens,
		Changes:      base.changes,
		Sessions:     rig.steers,
		ChangeReview: rig.cont,
		GitHubPRs:    base.srv.github,
	})
	return rig
}

func reviewDraft() map[string]any {
	return map[string]any{
		"note": "Overall: handle errors.",
		"comments": []map[string]any{
			{"path": "internal/a.go", "startLine": 12, "line": 14, "body": "Wrap this error.", "quote": "+return nil"},
			{"path": "web/b.ts", "line": 3, "side": "LEFT", "body": "Keep the check."},
		},
		"alsoPostOnPR": true,
	}
}

// TestReviewContinueSteersLiveRunWithOneMessage: with a live run the whole
// draft becomes ONE steering message carrying every comment (file, lines,
// quote, body) and the note; nothing is queued; with alsoPostOnPR the
// comments are also one GitHub review (event COMMENT).
func TestReviewContinueSteersLiveRunWithOneMessage(t *testing.T) {
	rig := newContinueRig(t, true)
	rec := doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-continue", rig.secrets[apitokens.ScopeOperator], reviewDraft())
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp ReviewContinueResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, ReviewDeliverySteered, resp.Agent.Delivery)
	assert.Equal(t, "run-1", resp.Agent.RunID)
	assert.Equal(t, 1, resp.Agent.Messages)

	msgs := rig.steers.sent()
	require.Len(t, msgs, 1, "one steering message for the whole draft")
	m := msgs[0]
	assert.Contains(t, m, "Operator review of your changes (2 comments)")
	assert.Contains(t, m, "Overall: Overall: handle errors.")
	assert.Contains(t, m, "1. internal/a.go, lines 12-14\n```diff\n+return nil\n```\nWrap this error.")
	assert.Contains(t, m, "2. web/b.ts, line 3 (base)\nKeep the check.")
	assert.Empty(t, rig.cont.queued(), "a live run is steered, not queued")

	require.NotNil(t, resp.PR)
	assert.True(t, resp.PR.Posted)
	assert.Equal(t, 2, resp.PR.Comments)
	posts := rig.gh.requests(http.MethodPost)
	require.Len(t, posts, 1)
	assert.Equal(t, "/repos/acme/app/pulls/7/reviews", posts[0].Path)
	assert.Equal(t, "COMMENT", posts[0].Body["event"])
	assert.Equal(t, "Overall: handle errors.", posts[0].Body["body"])
}

// TestReviewContinueQueuesWhenNoRunIsLive: with no live run the review is
// queued for the next run with moveToActive defaulting to true, or false
// when unchecked; a failing PR post is reported next to the queued agent
// delivery without undoing it.
func TestReviewContinueQueuesWhenNoRunIsLive(t *testing.T) {
	rig := newContinueRig(t, false)
	rig.gh.mu.Lock()
	rig.gh.rejectAs = http.StatusUnprocessableEntity
	rig.gh.mu.Unlock()
	rec := doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-continue", rig.secrets[apitokens.ScopeOperator], reviewDraft())
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp ReviewContinueResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, ReviewDeliveryQueued, resp.Agent.Delivery)
	assert.Equal(t, "Todo", resp.Agent.MovedTo)
	require.NotNil(t, resp.Agent.QueuedAt)
	require.NotNil(t, resp.PR)
	assert.False(t, resp.PR.Posted)
	assert.Equal(t, "github_rejected", resp.PR.Error, "the PR failure is reported")
	assert.Empty(t, rig.steers.sent())

	q := rig.cont.queued()
	require.Len(t, q, 1)
	assert.True(t, q[0].MoveToActive, "moveToActive defaults to true")
	assert.Equal(t, "Overall: handle errors.", q[0].Note)
	require.Len(t, q[0].Comments, 2)
	assert.Equal(t, "+return nil", q[0].Comments[0].Quote)
	assert.Equal(t, "LEFT", q[0].Comments[1].Side)

	body := reviewDraft()
	body["moveToActive"] = false
	body["alsoPostOnPR"] = false
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-continue", rig.secrets[apitokens.ScopeOperator], body)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"pr"`, "no PR outcome when not asked")
	q = rig.cont.queued()
	require.Len(t, q, 2)
	assert.False(t, q[1].MoveToActive)
	assert.Len(t, rig.gh.requests(http.MethodPost), 1, "only the first request posted (and was refused)")
}

// TestReviewContinueErrorsAndValidation: body validation, the queue's
// refusals, and the operator scope.
func TestReviewContinueErrorsAndValidation(t *testing.T) {
	assert.Equal(t, accessOperator, routeScopes["POST /api/v1/issues/{identifier}/review-continue"])
	rig := newContinueRig(t, false)
	op := rig.secrets[apitokens.ScopeOperator]
	path := "/api/v1/issues/ENG-1/review-continue"
	for name, tc := range map[string]struct {
		body  any
		field string
	}{
		"empty":     {map[string]any{}, "comments"},
		"bad path":  {map[string]any{"comments": []map[string]any{{"path": "/etc/x", "line": 1, "body": "x"}}}, "comments.path"},
		"no body":   {map[string]any{"comments": []map[string]any{{"path": "a.go", "line": 1}}}, "comments.body"},
		"bad side":  {map[string]any{"comments": []map[string]any{{"path": "a.go", "line": 1, "side": "UP", "body": "x"}}}, "comments.side"},
		"long note": {map[string]any{"note": strings.Repeat("n", maxReviewCommentBytes+1)}, "note"},
	} {
		rec := doJSON(t, rig.srv, http.MethodPost, path, op, tc.body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Contains(t, rec.Body.String(), `"field":"`+tc.field+`"`, name+": "+rec.Body.String())
	}
	many := make([]map[string]any, maxReviewBatchComments+1)
	for i := range many {
		many[i] = map[string]any{"path": "a.go", "line": i + 1, "body": "x"}
	}
	rec := doJSON(t, rig.srv, http.MethodPost, path, op, map[string]any{"comments": many})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rig.cont.queued(), "nothing invalid is queued")

	for _, tc := range []struct {
		err  error
		code int
		want string
	}{
		{ErrReviewContinueInputRequired, http.StatusConflict, "input_required"},
		{ErrReviewContinueIssueClosed, http.StatusConflict, "issue_closed"},
		{ErrReviewContinueNoActiveState, http.StatusConflict, "no_active_state"},
		{ErrReviewContinueIssueNotFound, http.StatusNotFound, "not_found"},
		{errors.New("tracker down"), http.StatusInternalServerError, "review_continue_failed"},
	} {
		rig.cont.mu.Lock()
		rig.cont.err = tc.err
		rig.cont.mu.Unlock()
		rec := doJSON(t, rig.srv, http.MethodPost, path, op, map[string]any{"note": "x"})
		assert.Equal(t, tc.code, rec.Code, tc.want+": "+rec.Body.String())
		assert.Contains(t, rec.Body.String(), tc.want)
	}
	assert.Empty(t, rig.gh.requests(http.MethodPost), "the PR is not posted when the agent delivery failed")

	rec = doJSON(t, rig.srv, http.MethodPost, path, rig.secrets[apitokens.ScopeViewer], map[string]any{"note": "x"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "operator", rec.Header().Get(RequiredScopeHeader))
}

// TestSteerMessagesForReviewFitTheSessionLimit: quotes shrink first; a
// review whose comments alone exceed one message is split into numbered
// parts, each within the session message limit.
func TestSteerMessagesForReviewFitTheSessionLimit(t *testing.T) {
	big := strings.Repeat("q", maxReviewQuoteBytes)
	var comments []ReviewContinueComment
	for i := range 3 {
		c := ReviewContinueComment{ReviewLineComment: ReviewLineComment{Path: "a.go", Line: i + 1, Side: "RIGHT", Body: "short"}, Quote: big}
		comments = append(comments, c)
	}
	msgs := SteerMessagesForReview("", comments)
	require.Len(t, msgs, 1, "quotes are cut so it stays one message")
	assert.LessOrEqual(t, len(msgs[0]), maxSessionMessageBytes)
	assert.Contains(t, msgs[0], "(quote cut)")

	long := strings.Repeat("b", maxReviewCommentBytes-10)
	comments = []ReviewContinueComment{
		{ReviewLineComment: ReviewLineComment{Path: "a.go", Line: 1, Side: "RIGHT", Body: long}},
		{ReviewLineComment: ReviewLineComment{Path: "b.go", Line: 2, Side: "RIGHT", Body: long}},
	}
	msgs = SteerMessagesForReview("note", comments)
	require.Len(t, msgs, 2)
	for i, m := range msgs {
		assert.LessOrEqual(t, len(m), maxSessionMessageBytes)
		assert.True(t, strings.HasPrefix(m, "[Operator review, part "), m[:40])
		assert.Contains(t, m, []string{"a.go, line 1", "b.go, line 2"}[i])
	}
}
