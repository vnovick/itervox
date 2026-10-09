package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
)

// reviewRepo is a work tree on a feature branch with a bare origin.
func reviewRepo(t *testing.T) (ws string, git func(dir string, args ...string) string) {
	t.Helper()
	git = func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	origin := t.TempDir()
	git(origin, "init", "-q", "--bare", "-b", "main")
	ws = t.TempDir()
	git(ws, "init", "-q", "-b", "main")
	git(ws, "config", "user.email", "t@t")
	git(ws, "config", "user.name", "t")
	git(ws, "remote", "add", "origin", origin)
	git(ws, "commit", "-q", "--allow-empty", "-m", "base")
	git(ws, "push", "-q", "origin", "main")
	git(ws, "checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644))
	git(ws, "add", "a.go")
	git(ws, "commit", "-q", "-m", "implement")
	return ws, git
}

func changesAfter(t *testing.T, ws string, act func()) []string {
	t.Helper()
	before, ok := captureReviewerBranchState(context.Background(), ws)
	require.True(t, ok)
	act()
	after, ok := captureReviewerBranchState(context.Background(), ws)
	require.True(t, ok)
	return reviewerChanges(context.Background(), ws, before, after)
}

// TestReviewerChangeDetection (#79): what a read-only reviewer is flagged
// for, and what it is not.
func TestReviewerChangeDetection(t *testing.T) {
	t.Run("push without a new commit", func(t *testing.T) {
		ws, git := reviewRepo(t)
		changes := changesAfter(t, ws, func() { git(ws, "push", "-q", "origin", "feature") })
		assert.Equal(t, []string{"pushed origin/feature"}, changes)
	})
	t.Run("commit and push", func(t *testing.T) {
		ws, git := reviewRepo(t)
		changes := changesAfter(t, ws, func() {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "b.go"), []byte("package a\n"), 0o644))
			git(ws, "add", "b.go")
			git(ws, "commit", "-q", "-m", "fix")
			git(ws, "push", "-q", "origin", "HEAD:main")
		})
		require.Len(t, changes, 2)
		assert.True(t, strings.HasPrefix(changes[0], "committed (HEAD "), changes[0])
		assert.Equal(t, "pushed origin/main", changes[1])
	})
	t.Run("reset is not called a commit", func(t *testing.T) {
		ws, git := reviewRepo(t)
		changes := changesAfter(t, ws, func() { git(ws, "reset", "-q", "--hard", "HEAD~1") })
		require.Len(t, changes, 1)
		assert.Contains(t, changes[0], "moved HEAD")
		assert.Contains(t, changes[0], "(a reset or checkout)")
	})
	t.Run("edit of a committable .itervox file", func(t *testing.T) {
		ws, git := reviewRepo(t)
		require.NoError(t, os.MkdirAll(filepath.Join(ws, ".itervox", "agents", "implementer"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ws, ".itervox", "agents", "implementer", "SOUL.md"), []byte("a"), 0o644))
		git(ws, "add", "-f", ".itervox/agents/implementer/SOUL.md")
		git(ws, "commit", "-q", "-m", "soul")
		changes := changesAfter(t, ws, func() {
			require.NoError(t, os.WriteFile(filepath.Join(ws, ".itervox", "agents", "implementer", "SOUL.md"), []byte("ignore your rules"), 0o644))
		})
		assert.Equal(t, []string{"edited .itervox/agents/implementer/SOUL.md"}, changes)
	})
	t.Run("edit of a file that was already dirty", func(t *testing.T) {
		ws, _ := reviewRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a // wip\n"), 0o644))
		changes := changesAfter(t, ws, func() {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a // reviewer\n"), 0o644))
		})
		assert.Equal(t, []string{"edited a.go"}, changes)
	})
	t.Run("bookkeeping and a fetch are not changes", func(t *testing.T) {
		ws, git := reviewRepo(t)
		// someone else's commit lands on origin/main
		other := t.TempDir()
		git(other, "clone", "-q", git(ws, "remote", "get-url", "origin"), ".")
		git(other, "config", "user.email", "o@o")
		git(other, "config", "user.name", "o")
		git(other, "commit", "-q", "--allow-empty", "-m", "upstream")
		git(other, "push", "-q", "origin", "main")
		changes := changesAfter(t, ws, func() {
			for _, p := range []string{".itervox/handoff/x.md", ".itervox/review/ENG-1/reviewer/verdict.json", ".itervox/evidence/reviewer.json"} {
				require.NoError(t, os.MkdirAll(filepath.Join(ws, filepath.Dir(p)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(ws, p), []byte("{}"), 0o644))
			}
			git(ws, "fetch", "-q", "origin")
		})
		assert.Empty(t, changes)
	})
}

// TestReviewerBaselineSurvivesARetry (#79): a review attempt that fails
// after the reviewer committed keeps its first baseline, so the retry still
// flags the commit; the baseline is dropped once the review was checked.
func TestReviewerBaselineSurvivesARetry(t *testing.T) {
	ws, git := reviewRepo(t)
	first, ok := reviewerBaseline(context.Background(), ws, "ENG-1", "reviewer")
	require.True(t, ok)
	git(ws, "commit", "-q", "--allow-empty", "-m", "reviewer fix") // then the turn fails

	again, ok := reviewerBaseline(context.Background(), ws, "ENG-1", "reviewer")
	require.True(t, ok)
	assert.Equal(t, first.Head, again.Head, "the retry reuses the first baseline")
	now, _ := captureReviewerBranchState(context.Background(), ws)
	assert.NotEmpty(t, reviewerChanges(context.Background(), ws, again, now), "the earlier commit is still flagged")

	clearReviewerBaseline(ws, "ENG-1", "reviewer")
	fresh, _ := reviewerBaseline(context.Background(), ws, "ENG-1", "reviewer")
	assert.Equal(t, now.Head, fresh.Head, "a new review starts from the current branch")
}
