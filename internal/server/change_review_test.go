package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/prwatch"
)

const reviewPRURL = "https://github.com/acme/app/pull/7"

// fakeGitHub is an httptest GitHub REST API for one pull request. It
// records every request (method, path, JSON body and Authorization).
type fakeGitHub struct {
	mu       sync.Mutex
	pr       string // GET /repos/acme/app/pulls/7
	files    string // GET /repos/acme/app/pulls/7/files
	reqs     []ghRequest
	rejectAs int // non-zero: POSTs answer this status
}

type ghRequest struct {
	Method, Path, Auth string
	Body               map[string]any
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.reqs = append(f.reqs, ghRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
	pr, files, reject := f.pr, f.files, f.rejectAs
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/7":
		_, _ = io.WriteString(w, pr)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/7/files":
		_, _ = io.WriteString(w, files)
	case r.Method == http.MethodPost && reject != 0:
		w.WriteHeader(reject)
		_, _ = io.WriteString(w, `{"message":"Can not approve your own pull request"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls/7/comments":
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":101,"html_url":"https://github.com/acme/app/pull/7#discussion_r101"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls/7/reviews":
		_, _ = io.WriteString(w, `{"id":202,"html_url":"https://github.com/acme/app/pull/7#pullrequestreview-202"}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) requests(method string) []ghRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ghRequest
	for _, r := range f.reqs {
		if method == "" || r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

const openPRJSON = `{"state":"open","merged":false,"number":7,"html_url":"` + reviewPRURL + `",
 "head":{"sha":"headsha1","ref":"eng-1"},"base":{"sha":"basesha1","ref":"main"},
 "labels":[{"name":"needs-human"},{"name":"docs"}]}`

// fakeReview is a ChangeReviewClient over fixed data that records feed
// events.
type fakeReview struct {
	mu      sync.Mutex
	prURL   string
	path    string
	outcome *ReviewOutcomeRow
	feed    []ReviewFeedEvent
}

func (f *fakeReview) IssuePR(string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prURL
}
func (f *fakeReview) WorktreePath(string) string             { return f.path }
func (f *fakeReview) ReviewOutcome(string) *ReviewOutcomeRow { return f.outcome }
func (f *fakeReview) setPR(u string)                         { f.mu.Lock(); f.prURL = u; f.mu.Unlock() }
func (f *fakeReview) RecordReviewFeed(ev ReviewFeedEvent) bool {
	f.mu.Lock()
	f.feed = append(f.feed, ev)
	f.mu.Unlock()
	return true
}
func (f *fakeReview) events() []ReviewFeedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReviewFeedEvent(nil), f.feed...)
}

// steerSessions records the steering message SendSessionMessage got.
type steerSessions struct {
	SessionsClient
	mu       sync.Mutex
	id, text string
}

func (s *steerSessions) SendSessionMessage(id, text string, _ bool) (SessionMessageRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.text = id, text
	return SessionMessageRow{ID: "m1", Text: text, Status: "queued", QueuedAt: time.Now()}, nil
}

type reviewRig struct {
	srv      *Server
	secrets  map[apitokens.Scope]string
	gh       *fakeGitHub
	review   *fakeReview
	changes  *fakeChanges
	sessions *steerSessions
}

// fakeGitHubToken is built by concatenation so no fixture looks like a
// real credential.
var fakeGitHubToken = "fake" + "-gh-" + "credential"

func newReviewRig(t *testing.T) reviewRig {
	t.Helper()
	gh := &fakeGitHub{pr: openPRJSON, files: `[]`}
	api := httptest.NewServer(gh)
	t.Cleanup(api.Close)
	client := &prwatch.Client{
		HTTP:    api.Client(),
		Token:   func(string) string { return fakeGitHubToken },
		BaseURL: func(string) string { return api.URL },
	}
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	secrets := make(map[apitokens.Scope]string)
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	rig := reviewRig{
		secrets: secrets, gh: gh,
		review:   &fakeReview{prURL: reviewPRURL, path: "/work/ENG-1"},
		changes:  &fakeChanges{run: &RunContextRow{RunID: "run-1", Live: true, Steers: []RunSteerRow{}}},
		sessions: &steerSessions{},
	}
	rig.srv = New(Config{
		Snapshot:     func() StateSnapshot { return StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		APIToken:     scopesTestBootstrap,
		Tokens:       store,
		Changes:      rig.changes,
		Sessions:     rig.sessions,
		ChangeReview: rig.review,
		GitHubPRs:    client,
	})
	return rig
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestLineCommentSentToRunningAgentQuotesFileAndLines (#238): a range
// comment with destination "agent" becomes one steering message to the
// issue's live run naming the file and the lines, quoting the selected
// diff lines, then the comment. The feed event carries no comment text.
func TestLineCommentSentToRunningAgentQuotesFileAndLines(t *testing.T) {
	rig := newReviewRig(t)
	const body = "Rename this: callers read it as a count"
	rec := doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "agent", "path": "internal/foo/bar.go", "startLine": 12, "line": 14,
			"body": body, "quote": "+n := len(xs)\n+total := n\n ctx line"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	assert.Equal(t, "run-1", rig.sessions.id, "sent to the live run")
	msg := rig.sessions.text
	assert.True(t, strings.HasPrefix(msg, "Review comment on internal/foo/bar.go, lines 12-14:\n"), msg)
	assert.Contains(t, msg, "```diff\n+n := len(xs)\n+total := n\n ctx line\n```", "the selected lines are quoted")
	assert.True(t, strings.HasSuffix(msg, body+"\n"), msg)
	assert.Empty(t, rig.gh.requests(""), "an agent comment never reaches GitHub")

	// One line, base side.
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "agent", "path": "a.go", "line": 3, "side": "LEFT", "body": "why removed?"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "Review comment on a.go, line 3 (base):\n\nwhy removed?\n", rig.sessions.text)

	ev := rig.review.events()
	require.Len(t, ev, 2)
	assert.Equal(t, ReviewFeedEvent{Kind: FeedKindReviewCommentPosted, Identifier: "ENG-1", Via: "agent", RunID: "run-1"}, ev[0])

	// No live run: refused, nothing sent.
	rig.changes.run.Live = false
	rig.sessions.text = ""
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "agent", "path": "a.go", "line": 3, "body": "x"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "no_live_run")
	assert.Empty(t, rig.sessions.text)
}

// TestLineCommentPostedAsGitHubReviewComment (#238): destination "pr"
// posts one review comment on the PR's head commit at the file and lines,
// with the daemon's GitHub token, against a fake GitHub API.
func TestLineCommentPostedAsGitHubReviewComment(t *testing.T) {
	rig := newReviewRig(t)
	rec := doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "pr", "path": "a.go", "startLine": 4, "line": 6, "body": "needs a test"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, reviewPRURL, resp["prUrl"])
	assert.EqualValues(t, 101, resp["id"])

	posts := rig.gh.requests(http.MethodPost)
	require.Len(t, posts, 1)
	p := posts[0]
	assert.Equal(t, "/repos/acme/app/pulls/7/comments", p.Path)
	assert.Equal(t, "Bearer "+fakeGitHubToken, p.Auth, "the daemon's existing GitHub credential")
	assert.Equal(t, map[string]any{"path": "a.go", "body": "needs a test", "line": 6.0, "side": "RIGHT",
		"start_line": 4.0, "start_side": "RIGHT", "commit_id": "headsha1"}, p.Body)

	ev := rig.review.events()
	require.Len(t, ev, 1)
	assert.Equal(t, ReviewFeedEvent{Kind: FeedKindReviewCommentPosted, Identifier: "ENG-1", PRURL: reviewPRURL, Via: "github"}, ev[0])

	// GitHub's refusal (a line outside the diff) is passed on as 422.
	rig.gh.mu.Lock()
	rig.gh.rejectAs = http.StatusUnprocessableEntity
	rig.gh.mu.Unlock()
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "pr", "path": "a.go", "line": 99, "body": "x"})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "github_rejected")

	// No PR: 409, nothing posted.
	rig.review.setPR("")
	n := len(rig.gh.requests(""))
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/review-comments", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"destination": "pr", "path": "a.go", "line": 1, "body": "x"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "no_pr")
	assert.Len(t, rig.gh.requests(""), n)
}

// TestPRReviewSubmitsBatchedCommentsAndRespectsMergePolicy (#238): an
// approve with two pending line comments is ONE GitHub review carrying
// both; nothing merges; the response reports the merge-block labels and
// the agent reviewer quorum, which the review does not change.
func TestPRReviewSubmitsBatchedCommentsAndRespectsMergePolicy(t *testing.T) {
	rig := newReviewRig(t)
	rig.review.outcome = &ReviewOutcomeRow{Blocked: true, Approvals: 1, Blocks: 1, Quorum: "any_block"}
	rec := doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/pr-review", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"event": "approve", "body": "LGTM with nits", "comments": []map[string]any{
			{"path": "a.go", "line": 6, "body": "nit one"},
			{"path": "b.go", "startLine": 2, "line": 3, "side": "LEFT", "body": "nit two"},
		}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	posts := rig.gh.requests(http.MethodPost)
	require.Len(t, posts, 1, "one review, not one request per comment")
	assert.Equal(t, "/repos/acme/app/pulls/7/reviews", posts[0].Path)
	b := posts[0].Body
	assert.Equal(t, "APPROVE", b["event"])
	assert.Equal(t, "headsha1", b["commit_id"])
	assert.Equal(t, "LGTM with nits", b["body"])
	assert.Equal(t, []any{
		map[string]any{"path": "a.go", "body": "nit one", "line": 6.0, "side": "RIGHT"},
		map[string]any{"path": "b.go", "body": "nit two", "line": 3.0, "side": "LEFT", "start_line": 2.0, "start_side": "LEFT"},
	}, b["comments"])
	for _, r := range rig.gh.requests("") {
		assert.NotContains(t, r.Path, "/merge", "a review never merges")
		assert.NotEqual(t, http.MethodPut, r.Method)
	}

	var resp PRReviewResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "approve", resp.Event)
	assert.Equal(t, 2, resp.Comments)
	assert.False(t, resp.Merged)
	assert.False(t, resp.MergePolicy.MergesFromHere)
	assert.Equal(t, []string{"needs-human"}, resp.MergePolicy.BlockLabels, "merge_pr's block labels still apply")
	require.NotNil(t, resp.MergePolicy.Quorum)
	assert.True(t, resp.MergePolicy.Quorum.Blocked, "the agent quorum is reported, not overridden")

	ev := rig.review.events()
	require.Len(t, ev, 1)
	assert.Equal(t, ReviewFeedEvent{Kind: FeedKindPRReviewByOperator, Identifier: "ENG-1", PRURL: reviewPRURL,
		Event: "approve", Count: 2}, ev[0])

	// Request changes needs a body or a comment; a closed PR is refused.
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/pr-review", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"event": "request_changes"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rig.gh.mu.Lock()
	rig.gh.pr = strings.Replace(openPRJSON, `"state":"open","merged":false`, `"state":"closed","merged":true`, 1)
	rig.gh.mu.Unlock()
	rec = doJSON(t, rig.srv, http.MethodPost, "/api/v1/issues/ENG-1/pr-review", rig.secrets[apitokens.ScopeOperator],
		map[string]any{"event": "comment", "body": "late"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "pr_closed")
	assert.Len(t, rig.gh.requests(http.MethodPost), 1)
}

// TestReviewRoutesNeedOperatorScope (#238): both change-review routes are
// declared operator; a viewer is refused before anything is sent, an
// operator gets through; reading the diff stays viewer.
func TestReviewRoutesNeedOperatorScope(t *testing.T) {
	assert.Equal(t, accessOperator, routeScopes["POST /api/v1/issues/{identifier}/review-comments"])
	assert.Equal(t, accessOperator, routeScopes["POST /api/v1/issues/{identifier}/pr-review"])
	assert.Equal(t, accessViewer, routeScopes["GET /api/v1/issues/{identifier}/diff"])

	rig := newReviewRig(t)
	comment := map[string]any{"destination": "agent", "path": "a.go", "line": 1, "body": "x"}
	review := map[string]any{"event": "comment", "body": "x"}
	for _, tc := range []struct {
		path string
		body any
		ok   int
	}{
		{"/api/v1/issues/ENG-1/review-comments", comment, http.StatusAccepted},
		{"/api/v1/issues/ENG-1/pr-review", review, http.StatusCreated},
	} {
		rec := doJSON(t, rig.srv, http.MethodPost, tc.path, rig.secrets[apitokens.ScopeViewer], tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.path)
		assert.Equal(t, "operator", rec.Header().Get(RequiredScopeHeader), tc.path)
		rec = doJSON(t, rig.srv, http.MethodPost, tc.path, "", tc.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, tc.path)
	}
	assert.Empty(t, rig.sessions.text, "a viewer's comment never reaches the agent")
	assert.Empty(t, rig.gh.requests(""), "nor GitHub")
	assert.Empty(t, rig.review.events())

	for _, tc := range []struct {
		path string
		body any
		ok   int
	}{
		{"/api/v1/issues/ENG-1/review-comments", comment, http.StatusAccepted},
		{"/api/v1/issues/ENG-1/pr-review", review, http.StatusCreated},
	} {
		rec := doJSON(t, rig.srv, http.MethodPost, tc.path, rig.secrets[apitokens.ScopeOperator], tc.body)
		assert.Equal(t, tc.ok, rec.Code, tc.path+" "+rec.Body.String())
	}
	rec := doReq(t, rig.srv, http.MethodGet, "/api/v1/issues/ENG-1/diff", rig.secrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestDiffSwitchesToPRFilesOnceAPRExists (#238): without a PR the diff is
// the worktree; once the issue has a PR the default is the PR's files from
// GitHub (base...head), the live worktree stays selectable while the run
// is live, and a PR file's patch is served from GitHub's.
func TestDiffSwitchesToPRFilesOnceAPRExists(t *testing.T) {
	rig := newReviewRig(t)
	rig.changes.diff = DiffResponse{Base: "main", MergeBase: "wtbase", Files: []DiffFileRow{{Path: "wip.go", Status: "untracked", Additions: 3}}}
	rig.gh.files = `[{"filename":"a.go","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-x\n+y"},
		{"filename":"gone.go","status":"removed","deletions":4,"patch":"@@ -1,4 +0,0 @@\n-a\n-b\n-c\n-d"}]`
	operator := rig.secrets[apitokens.ScopeOperator]
	read := func(path string) DiffResponse {
		t.Helper()
		rec := doReq(t, rig.srv, http.MethodGet, path, operator)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var d DiffResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		return d
	}

	rig.review.setPR("")
	d := read("/api/v1/issues/ENG-1/diff")
	assert.Equal(t, DiffSourceWorktree, d.Source)
	assert.Equal(t, []string{DiffSourceWorktree}, d.Sources)
	assert.Equal(t, "wip.go", d.Files[0].Path)
	assert.Equal(t, "/work/ENG-1", d.WorktreePath)
	assert.Empty(t, rig.gh.requests(""), "no PR: GitHub is not asked")

	rig.review.setPR(reviewPRURL)
	d = read("/api/v1/issues/ENG-1/diff")
	assert.Equal(t, DiffSourcePR, d.Source, "the PR's files once a PR exists")
	assert.Equal(t, []string{DiffSourcePR, DiffSourceWorktree}, d.Sources, "the live worktree stays available")
	assert.Equal(t, reviewPRURL, d.PRURL)
	assert.Equal(t, "main", d.Base)
	assert.Equal(t, "headsha1", d.HeadSHA)
	require.Len(t, d.Files, 2)
	assert.Equal(t, DiffFileRow{Path: "a.go", Status: "modified", Additions: 1, Deletions: 1}, d.Files[0])
	assert.Equal(t, "deleted", d.Files[1].Status)

	rec := doReq(t, rig.srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?file=a.go", operator)
	require.Equal(t, http.StatusOK, rec.Code)
	var fd FileDiffResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fd))
	assert.Equal(t, DiffSourcePR, fd.Source)
	assert.Equal(t, "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n", fd.Patch)
	assert.Empty(t, rig.changes.files, "a PR file never runs git")

	d = read("/api/v1/issues/ENG-1/diff?source=worktree")
	assert.Equal(t, DiffSourceWorktree, d.Source)
	assert.Equal(t, "wip.go", d.Files[0].Path)
	assert.Equal(t, reviewPRURL, d.PRURL)

	// The run ends: the PR is the only source offered.
	rig.changes.run.Live = false
	d = read("/api/v1/issues/ENG-1/diff")
	assert.Equal(t, []string{DiffSourcePR}, d.Sources)

	// GitHub down: the default falls back to the worktree and says why;
	// an explicit ?source=pr reports the failure.
	rig.gh.mu.Lock()
	rig.gh.pr = `not json`
	rig.gh.mu.Unlock()
	rig.srv.github = &prwatch.Client{HTTP: &http.Client{Timeout: time.Second},
		BaseURL: func(string) string { return "http://127.0.0.1:1" }}
	d = read("/api/v1/issues/ENG-1/diff")
	assert.Equal(t, DiffSourceWorktree, d.Source)
	assert.Equal(t, "github_failed", d.PRError)
	rec = doReq(t, rig.srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?source=pr", operator)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
