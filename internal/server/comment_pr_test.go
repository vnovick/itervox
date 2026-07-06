package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

// ValidateCommentPRRequest must reject empty submissions, file-less findings,
// negative line numbers, missing bodies, and malformed severity values.
func TestValidateCommentPRRequest_Rejects(t *testing.T) {
	cases := []struct {
		name string
		req  CommentPRRequest
		want string
	}{
		{
			name: "empty submission",
			req:  CommentPRRequest{},
			want: "summary or at least one finding",
		},
		{
			name: "missing path",
			req: CommentPRRequest{
				Findings: []CommentPRFinding{{Line: 1, Severity: "error", Body: "x"}},
			},
			want: "path is required",
		},
		{
			name: "negative line",
			req: CommentPRRequest{
				Findings: []CommentPRFinding{{Path: "a.go", Line: -1, Severity: "info", Body: "x"}},
			},
			want: "line must be >= 0",
		},
		{
			name: "missing body",
			req: CommentPRRequest{
				Findings: []CommentPRFinding{{Path: "a.go", Line: 1, Severity: "info", Body: "  "}},
			},
			want: "body is required",
		},
		{
			name: "missing severity",
			req: CommentPRRequest{
				Findings: []CommentPRFinding{{Path: "a.go", Line: 1, Body: "x"}},
			},
			want: "severity is required",
		},
		{
			name: "bad severity",
			req: CommentPRRequest{
				Findings: []CommentPRFinding{{Path: "a.go", Line: 1, Severity: "panic", Body: "x"}},
			},
			want: "severity must be info, warning, or error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommentPRRequest(tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// ValidateCommentPRRequest must accept summary-only and finding-only shapes,
// and accept severity in any case.
func TestValidateCommentPRRequest_Accepts(t *testing.T) {
	cases := []CommentPRRequest{
		{Summary: "Just a summary, no findings."},
		{
			Findings: []CommentPRFinding{
				{Path: "x.go", Line: 1, Severity: "ERROR", Body: "case-insensitive ok"},
				{Path: "x.go", Line: 0, Severity: "Warning", Body: "file-level ok"},
			},
		},
	}
	for i, req := range cases {
		t.Run("", func(t *testing.T) {
			_ = i
			assert.NoError(t, ValidateCommentPRRequest(req))
		})
	}
}

// RenderCommentPRMarkdown deterministically sorts errors → warnings → info,
// then by path, then by line. Stability across calls is the contract.
func TestRenderCommentPRMarkdown_DeterministicOrder(t *testing.T) {
	req := CommentPRRequest{
		Summary: "Two issues found across two files.",
		Findings: []CommentPRFinding{
			{Path: "z.go", Line: 5, Severity: "info", Body: "trivial"},
			{Path: "a.go", Line: 9, Severity: "error", Body: "nil deref"},
			{Path: "a.go", Line: 1, Severity: "error", Body: "missing import"},
			{Path: "m.go", Line: 0, Severity: "warning", Body: "TODO without ticket"},
		},
	}
	out := RenderCommentPRMarkdown(req)

	// Header + summary first, then Findings section.
	assert.Contains(t, out, "## 🤖 Itervox review")
	assert.Contains(t, out, "Two issues found across two files.")
	assert.Contains(t, out, "### Findings (4)")

	// Errors first (a.go:1 before a.go:9), then warning, then info.
	idxErr1 := strings.Index(out, "missing import")
	idxErr9 := strings.Index(out, "nil deref")
	idxWarn := strings.Index(out, "TODO without ticket")
	idxInfo := strings.Index(out, "trivial")
	require.NotEqual(t, -1, idxErr1)
	require.NotEqual(t, -1, idxErr9)
	require.NotEqual(t, -1, idxWarn)
	require.NotEqual(t, -1, idxInfo)
	assert.Less(t, idxErr1, idxErr9, "a.go:1 (error) before a.go:9 (error): path then line")
	assert.Less(t, idxErr9, idxWarn, "errors before warnings")
	assert.Less(t, idxWarn, idxInfo, "warnings before info")

	// File-level finding (line=0) renders without ":0" suffix.
	assert.Contains(t, out, "`m.go`")
	assert.NotContains(t, out, "m.go:0")

	// Same render twice → byte-for-byte identical (idempotency contract).
	out2 := RenderCommentPRMarkdown(req)
	assert.Equal(t, out, out2)
}

// Summary-only requests render without a Findings section.
func TestRenderCommentPRMarkdown_SummaryOnly(t *testing.T) {
	out := RenderCommentPRMarkdown(CommentPRRequest{Summary: "All clean."})
	assert.Contains(t, out, "All clean.")
	assert.NotContains(t, out, "### Findings")
}

// ─── gaps_11 G-16 — comment target resolution ────────────────────────────────

// commentPRTestServer builds a whitebox *Server for the G-16 target-resolution
// tests: snapshot-injected PausedWithPR, an action token scoped to
// AgentActionCommentPR, and a recording CommentOnIssue fallback.
func commentPRTestServer(t *testing.T, pausedWithPR map[string]string, issueCommented *bool) (*Server, string) {
	t.Helper()
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-9", "run-1", []string{config.AgentActionCommentPR}, "", time.Minute)
	require.NoError(t, err)
	s := New(Config{
		Snapshot: func() StateSnapshot {
			return StateSnapshot{PausedWithPR: pausedWithPR}
		},
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Client: &FuncClient{
			CommentOnIssueFn: func(_ context.Context, identifier, body string) error {
				if issueCommented != nil {
					*issueCommented = true
				}
				return nil
			},
		},
	})
	return s, token
}

func postCommentPR(t *testing.T, s *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"summary":"Review done.","findings":[{"path":"a.go","line":3,"severity":"error","body":"nil deref"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-9/comment_pr", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

// When the issue has a recorded open github.com PR URL, the rendered review
// must be posted on the PR via `gh pr comment <url> --body <body>` — NOT on
// the tracker issue.
func TestHandleAgentCommentPR_PostsToGitHubPRWhenURLKnown(t *testing.T) {
	const prURL = "https://github.com/acme/widgets/pull/42"
	var issueCommented bool
	s, token := commentPRTestServer(t, map[string]string{"ENG-9": prURL}, &issueCommented)

	var ghArgs []string
	s.ghRun = func(_ context.Context, args ...string) ([]byte, error) {
		ghArgs = args
		return []byte(prURL + "#issuecomment-1\n"), nil
	}

	w := postCommentPR(t, s, token)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"target":"github_pr"`)
	assert.Contains(t, w.Body.String(), prURL)
	require.GreaterOrEqual(t, len(ghArgs), 5, "gh must be invoked with pr comment <url> --body <body>")
	assert.Equal(t, []string{"pr", "comment", prURL, "--body"}, ghArgs[:4])
	assert.Contains(t, ghArgs[4], "🤖 Itervox review")
	assert.Contains(t, ghArgs[4], "a.go:3")
	assert.Contains(t, ghArgs[4], "<!-- itervox:managed -->")
	assert.False(t, issueCommented, "comment must land on the PR, not the tracker issue")
}

// A gh failure (missing binary, unauthenticated, API error) must fall back to
// the tracker issue — never a silent drop.
func TestHandleAgentCommentPR_FallsBackToIssueWhenGHFails(t *testing.T) {
	var issueCommented bool
	s, token := commentPRTestServer(t,
		map[string]string{"ENG-9": "https://github.com/acme/widgets/pull/42"}, &issueCommented)
	s.ghRun = func(context.Context, ...string) ([]byte, error) {
		return []byte("gh: Not Found"), errors.New("exit status 1")
	}

	w := postCommentPR(t, s, token)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"target":"tracker_issue"`)
	assert.True(t, issueCommented, "gh failure must fall back to the tracker issue comment")
}

// With no recorded PR URL for the issue, gh must not be invoked at all and
// the comment lands on the tracker issue (the v1 behaviour).
func TestHandleAgentCommentPR_FallsBackWhenNoPRURL(t *testing.T) {
	var issueCommented bool
	s, token := commentPRTestServer(t, nil, &issueCommented)
	ghCalled := false
	s.ghRun = func(context.Context, ...string) ([]byte, error) {
		ghCalled = true
		return nil, nil
	}

	w := postCommentPR(t, s, token)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"target":"tracker_issue"`)
	assert.False(t, ghCalled, "gh must not run when no PR URL is recorded")
	assert.True(t, issueCommented)
}

// A recorded PR URL on a non-github.com host must not be passed to gh; the
// comment falls back to the tracker issue.
func TestHandleAgentCommentPR_NonGitHubPRURLFallsBack(t *testing.T) {
	var issueCommented bool
	s, token := commentPRTestServer(t,
		map[string]string{"ENG-9": "https://gitlab.com/acme/widgets/-/merge_requests/7"}, &issueCommented)
	ghCalled := false
	s.ghRun = func(context.Context, ...string) ([]byte, error) {
		ghCalled = true
		return nil, nil
	}

	w := postCommentPR(t, s, token)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"target":"tracker_issue"`)
	assert.False(t, ghCalled, "non-github PR URLs must not be passed to gh")
	assert.True(t, issueCommented)
}
