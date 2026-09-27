package prdetector_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/prdetector"
)

func TestParsePRURLs_FindsURLs(t *testing.T) {
	text := `See https://github.com/org/repo/pull/42 and also
https://github.com/org/repo/pull/99 for context.`
	urls := prdetector.ParsePRURLs(text)
	assert.Equal(t, []string{
		"https://github.com/org/repo/pull/42",
		"https://github.com/org/repo/pull/99",
	}, urls)
}

func TestParsePRURLs_Empty(t *testing.T) {
	assert.Nil(t, prdetector.ParsePRURLs("no PR here"))
}

func TestParsePRURLs_DuplicatesDeduped(t *testing.T) {
	text := "https://github.com/a/b/pull/1 and https://github.com/a/b/pull/1"
	urls := prdetector.ParsePRURLs(text)
	assert.Len(t, urls, 1)
}

// fakeGH writes a shell script that prints output and exits 0, returns its path.
// Uses printf '%s' to avoid breakage if the JSON body ever contains GHEOF on its own line.
func fakeGH(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", output)
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	return p
}

func TestCheckPR_OpenPR(t *testing.T) {
	out := `{"state":"OPEN","headRefName":"fix/my-branch","body":"Fixes the bug"}`
	ghPath := fakeGH(t, out)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	pr, err := prdetector.CheckPR(context.Background(), "https://github.com/org/repo/pull/42")
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, "fix/my-branch", pr.Branch)
	assert.Equal(t, "https://github.com/org/repo/pull/42", pr.URL)
}

func TestCheckPR_MergedReturnsNil(t *testing.T) {
	out := `{"state":"MERGED","headRefName":"fix/x","body":""}`
	ghPath := fakeGH(t, out)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	pr, err := prdetector.CheckPR(context.Background(), "https://github.com/org/repo/pull/1")
	require.NoError(t, err)
	assert.Nil(t, pr)
}

func TestCheckPR_GhFailureReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	require.NoError(t, os.WriteFile(ghPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	pr, err := prdetector.CheckPR(context.Background(), "https://github.com/org/repo/pull/1")
	assert.NoError(t, err)
	assert.Nil(t, pr)
}

func TestDetect_FindsOpenPR(t *testing.T) {
	out := `{"state":"OPEN","headRefName":"fix/eng-1","body":"Fixes ENG-1"}`
	ghPath := fakeGH(t, out)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	issue := domain.Issue{
		ID:          "i1",
		Identifier:  "ENG-1",
		Description: strPtr("See https://github.com/org/repo/pull/10"),
	}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, "fix/eng-1", pr.Branch)
	assert.Equal(t, "Fixes ENG-1", pr.Description)
}

func TestDetect_NoPRURLsReturnsNil(t *testing.T) {
	issue := domain.Issue{ID: "i1", Identifier: "ENG-1"}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	assert.Nil(t, pr)
}

func TestDetect_MergedPRReturnsNil(t *testing.T) {
	out := `{"state":"MERGED","headRefName":"x","body":""}`
	ghPath := fakeGH(t, out)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	issue := domain.Issue{
		ID:          "i1",
		Identifier:  "ENG-2",
		Description: strPtr("https://github.com/org/repo/pull/5"),
	}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	assert.Nil(t, pr)
}

func TestDetect_UsesLatestCommentURL(t *testing.T) {
	// PR URL in description is older; PR URL in comment should take precedence.
	// fakeGH always returns OPEN so the "most recently sourced" URL (from comment) is checked first.
	// We use two different PR numbers: description has /pull/1, comment has /pull/2.
	// Fake gh returns OPEN for any URL so whichever is checked first will win.
	// The test verifies that the comment URL (/pull/2) is returned (last-write-wins).
	out := `{"state":"OPEN","headRefName":"fix/latest","body":"from comment"}`
	ghPath := fakeGH(t, out)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	now := time.Now()
	issue := domain.Issue{
		ID:          "i1",
		Identifier:  "ENG-3",
		Description: strPtr("https://github.com/org/repo/pull/1"),
		Comments: []domain.Comment{
			{Body: "See https://github.com/org/repo/pull/2", CreatedAt: &now},
		},
	}
	pr, err := prdetector.Detect(context.Background(), issue)
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, "https://github.com/org/repo/pull/2", pr.URL)
}

func TestFormatPRContext_FullContext(t *testing.T) {
	pr := &prdetector.PRContext{
		URL:         "https://github.com/org/repo/pull/42",
		Branch:      "fix/eng-1",
		Description: "Fixes the bug",
		ReviewComments: []prdetector.ReviewComment{
			{Body: "LGTM", Author: "alice"},
		},
		DiffStat: "1 file changed",
	}
	out := prdetector.FormatPRContext(pr)
	assert.Contains(t, out, "## Open PR Context")
	assert.Contains(t, out, "fix/eng-1")
	assert.Contains(t, out, "Fixes the bug")
	assert.Contains(t, out, "alice: LGTM")
	assert.Contains(t, out, "1 file changed")
}

func TestFormatPRContext_Nil(t *testing.T) {
	assert.Equal(t, "", prdetector.FormatPRContext(nil))
}

func strPtr(s string) *string { return &s }

// ─── FetchPRContext tests ──────────────────────────────────────────────────────

// initGitRepo creates a minimal git repo with one commit on main and a fake
// origin/main ref pointing to that commit, then adds a second commit on top.
// This allows `git diff origin/main` to produce non-empty output.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		cmd := gitexec.Command(context.Background(), dir, args...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")

	// Initial commit — becomes origin/main.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	git("add", ".")
	git("commit", "-m", "initial")
	git("branch", "-M", "main")

	// Simulate origin/main pointing at the initial commit.
	git("update-ref", "refs/remotes/origin/main", "HEAD")

	// A second commit that differs from origin/main.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello world\n"), 0o644))
	git("add", ".")
	git("commit", "-m", "update")

	return dir
}

func TestFetchPRContext_ExcludesBlankReviewBodies(t *testing.T) {
	reviews := `{"reviews":[{"body":"","author":{"login":"alice"}},{"body":"LGTM","author":{"login":"bob"}}]}`
	ghPath := fakeGH(t, reviews)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	pr := &prdetector.PRContext{URL: "https://github.com/org/repo/pull/1"}
	prdetector.FetchPRContext(context.Background(), pr, "", "")
	require.Len(t, pr.ReviewComments, 1)
	assert.Equal(t, "LGTM", pr.ReviewComments[0].Body)
	assert.Equal(t, "bob", pr.ReviewComments[0].Author)
}

func TestFetchPRContext_EmptyWsPathSkipsDiff(t *testing.T) {
	ghPath := fakeGH(t, `{"reviews":[]}`)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	pr := &prdetector.PRContext{URL: "https://github.com/org/repo/pull/1"}
	prdetector.FetchPRContext(context.Background(), pr, "", "")
	assert.Empty(t, pr.DiffStat)
	assert.Empty(t, pr.FullDiff)
}

func TestFetchPRContext_PopulatesDiffStatAndFullDiff(t *testing.T) {
	dir := initGitRepo(t)

	ghPath := fakeGH(t, `{"reviews":[]}`)
	t.Setenv("PATH", filepath.Dir(ghPath)+":"+os.Getenv("PATH"))

	pr := &prdetector.PRContext{URL: "https://github.com/org/repo/pull/42"}
	prdetector.FetchPRContext(context.Background(), pr, dir, "origin/main")
	assert.NotEmpty(t, pr.DiffStat, "DiffStat should be populated from git diff --stat")
	assert.NotEmpty(t, pr.FullDiff, "FullDiff should be populated for small diffs")
}

func TestFetchPRContext_GhFailureIsNonFatal(t *testing.T) {
	// gh exits 1 — FetchPRContext should still populate diff fields if wsPath is set.
	dir := initGitRepo(t)
	ghDir := t.TempDir()
	ghPath := filepath.Join(ghDir, "gh")
	require.NoError(t, os.WriteFile(ghPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", ghDir+":"+os.Getenv("PATH"))

	pr := &prdetector.PRContext{URL: "https://github.com/org/repo/pull/1"}
	prdetector.FetchPRContext(context.Background(), pr, dir, "origin/main")
	assert.Empty(t, pr.ReviewComments, "no review comments when gh fails")
	assert.NotEmpty(t, pr.DiffStat, "DiffStat still populated from local git")
}
