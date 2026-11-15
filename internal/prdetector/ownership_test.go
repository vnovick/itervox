package prdetector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/prdetector"
)

// a1PRView is what `gh pr view` reports for A-1's open pull request: its own
// branch, title and body, none of which mention B-1.
const a1PRView = `{"state":"OPEN","headRefName":"user/a-1-some-branch","title":"A-1: add the thing","body":"Implements A-1."}`

// TestOpenPRLookupIgnoresUnrelatedPR (#193): B-1's comments carry the URL of
// A-1's open pull request (the "🔗 Pull request created" comment the worker
// used to post when `gh pr view` in B-1's worktree, stacked on A-1's branch,
// resolved A-1's PR through the branch's upstream). That PR is not B-1's: its
// branch, title and body never name B-1, so its branch must not be reused.
func TestOpenPRLookupIgnoresUnrelatedPR(t *testing.T) {
	ghPath := fakeGH(t, a1PRView)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	now := time.Now()
	branch := "user/b-1-other-work"
	issue := domain.Issue{
		ID:         "b1",
		Identifier: "B-1",
		Title:      "Other work",
		BranchName: &branch,
		Comments: []domain.Comment{
			{Body: "🔗 Pull request created: https://github.com/org/repo/pull/7", CreatedAt: &now},
		},
	}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	assert.Nil(t, pr, "A-1's PR must not be treated as B-1's")
}

// TestOpenPRLookupAcceptsOwnPR: the same lookup still continues a PR that is
// the issue's own, by any one of the ownership signals.
func TestOpenPRLookupAcceptsOwnPR(t *testing.T) {
	ghPath := fakeGH(t, a1PRView)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	issue := domain.Issue{
		ID:          "a1",
		Identifier:  "A-1",
		Description: strPtr("Continue https://github.com/org/repo/pull/7"),
	}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, "user/a-1-some-branch", pr.Branch)
	assert.Equal(t, "A-1: add the thing", pr.Title)
}

func TestPROwnedByIssue(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name  string
		pr    prdetector.PRContext
		issue domain.Issue
		want  bool
	}{
		{"branch equals tracker branch", prdetector.PRContext{Branch: "feature/x"},
			domain.Issue{Identifier: "ENG-9", BranchName: str("feature/x")}, true},
		{"branch equals itervox default", prdetector.PRContext{Branch: "itervox/12"},
			domain.Issue{Identifier: "#12"}, true},
		{"branch names identifier", prdetector.PRContext{Branch: "vlad/eng-12-fix"},
			domain.Issue{Identifier: "ENG-12"}, true},
		{"branch names a longer identifier", prdetector.PRContext{Branch: "vlad/eng-123-fix"},
			domain.Issue{Identifier: "ENG-12"}, false},
		{"branch names a prefixed identifier", prdetector.PRContext{Branch: "vlad/xeng-12-fix"},
			domain.Issue{Identifier: "ENG-12"}, false},
		{"numeric identifier is not matched inside a branch", prdetector.PRContext{Branch: "fix-12-things"},
			domain.Issue{Identifier: "#12"}, false},
		{"title names identifier", prdetector.PRContext{Branch: "x", Title: "[ENG-12] fix"},
			domain.Issue{Identifier: "ENG-12"}, true},
		{"title names identifier case-insensitively", prdetector.PRContext{Branch: "x", Title: "eng-12: fix"},
			domain.Issue{Identifier: "ENG-12"}, true},
		{"body names a longer identifier", prdetector.PRContext{Branch: "x", Description: "Fixes ENG-123"},
			domain.Issue{Identifier: "ENG-12"}, false},
		{"body closes the GitHub issue", prdetector.PRContext{Branch: "x", Description: "Fixes #12."},
			domain.Issue{Identifier: "#12"}, true},
		{"body names a longer GitHub issue", prdetector.PRContext{Branch: "x", Description: "Fixes #123"},
			domain.Issue{Identifier: "#12"}, false},
		{"body links the tracker issue", prdetector.PRContext{Branch: "x", Description: "See https://linear.app/t/issue/ENG-12/x"},
			domain.Issue{Identifier: "ENG-12", URL: str("https://linear.app/t/issue/ENG-12/x")}, true},
		{"nothing names the issue", prdetector.PRContext{Branch: "user/a-1-some-branch", Title: "A-1", Description: "Implements A-1"},
			domain.Issue{Identifier: "B-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := tc.pr
			assert.Equal(t, tc.want, prdetector.OwnedByIssue(&pr, tc.issue))
		})
	}
}
