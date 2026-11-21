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

// splitGitRepo is a real git repository in a temp dir.
func splitGitRepo(t *testing.T) (dir string, git func(args ...string) string) {
	t.Helper()
	dir = t.TempDir()
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@example.com"},
		{"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@example.com"}, {"GIT_CONFIG_GLOBAL", os.DevNull}} {
		t.Setenv(kv[0], kv[1])
	}
	git = func(args ...string) string {
		t.Helper()
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	return dir, git
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func treeFiles(t *testing.T, git func(args ...string) string, tree string) []string {
	t.Helper()
	return strings.Split(git("ls-tree", "-r", "--name-only", tree), "\n")
}

// A path that turned from a file into a directory (or back) builds on
// either side of the split, whichever side the other half went to; before,
// update-index exited 128 ("cannot add to the index - missing --add
// option?" / a directory in the way).
func TestSplitTreeFileDirectoryChanges(t *testing.T) {
	dir, git := splitGitRepo(t)
	writeRepoFile(t, dir, "foo", "file\n")
	writeRepoFile(t, dir, "bar/baz", "in a dir\n")
	writeRepoFile(t, dir, "keep", "k\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("rm", "-q", "foo", "bar/baz")
	writeRepoFile(t, dir, "foo/inner", "now a dir\n")
	writeRepoFile(t, dir, "bar", "now a file\n")
	git("add", "-A")
	git("commit", "-q", "-m", "swap")
	head := git("rev-parse", "HEAD")
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"both halves of each change on one side", []string{"foo", "foo/inner", "bar", "bar/baz"}, []string{"bar", "foo/inner", "keep"}},
		{"only the new directory's file", []string{"foo/inner"}, []string{"bar/baz", "foo/inner", "keep"}},
		{"only the new file over a directory", []string{"bar"}, []string{"bar", "foo", "keep"}},
		{"only the removals", []string{"foo", "bar/baz"}, []string{"keep"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := splitTree(ctx, dir, base, head, tc.paths)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, treeFiles(t, git, tree))
		})
	}
}

// git's own message reaches the error.
func TestSplitTreeErrorCarriesGitStderr(t *testing.T) {
	dir, git := splitGitRepo(t)
	writeRepoFile(t, dir, "a", "a\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	_, err := splitTree(context.Background(), dir, "no-such-ref", "HEAD", []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-ref", "git's stderr names the bad revision: %v", err)
}

// Batches stay within the byte budget, keep --cacheinfo pairs together and
// lose nothing.
func TestArgBatchesByteBudget(t *testing.T) {
	var args []string
	for i := range 3000 {
		args = append(args, "--cacheinfo", "100644,"+strings.Repeat("a", 40)+",dir/sub/file"+strings.Repeat("x", i%50)+".go")
	}
	batches := argBatches(args, 2, splitArgBudget)
	require.Greater(t, len(batches), 1)
	var joined []string
	for _, b := range batches {
		n := 0
		for _, a := range b {
			n += len(a) + 1
		}
		assert.LessOrEqual(t, n, splitArgBudget)
		assert.Zero(t, len(b)%2, "a --cacheinfo pair is never cut")
		assert.Equal(t, "--cacheinfo", b[0])
		joined = append(joined, b...)
	}
	assert.Equal(t, args, joined)
	assert.Equal(t, [][]string{{strings.Repeat("z", 100)}}, argBatches([]string{strings.Repeat("z", 100)}, 1, 10), "an oversized argument is a batch of its own")
	assert.Empty(t, argBatches(nil, 1, 10))
}

// A split commit keeps the agent's whole message (body and trailers) and
// adds one trailer saying which half it is; splitting it again builds the
// same message.
func TestStackSplitMessageKeepsBodyAndTrailers(t *testing.T) {
	msg := "fix: the thing\n\nWhy it was broken.\n\nSigned-off-by: A <a@example.com>\nCo-Authored-By: B <b@example.com>\n"
	got := stackSplitMessage(msg, "code change only")
	assert.Equal(t, "fix: the thing\n\nWhy it was broken.\n\nSigned-off-by: A <a@example.com>\nCo-Authored-By: B <b@example.com>\n"+
		stackSplitTrailer+" code change only\n", got)
	assert.Equal(t, got, stackSplitMessage(got, "code change only"), "idempotent")
	assert.Equal(t, "fix: x\n\nbody\n\n"+stackSplitTrailer+" top\n", stackSplitMessage("fix: x\n\nbody\n", "top"))
	assert.Equal(t, "fix: subject only\n\n"+stackSplitTrailer+" top\n", stackSplitMessage("fix: subject only", "top"),
		"a subject that looks like a trailer stays the subject")
}

// The temporary worktree is removed even when an interrupted add left it
// locked.
func TestRemoveTempWorktreeRemovesLockedWorktree(t *testing.T) {
	dir, git := splitGitRepo(t)
	writeRepoFile(t, dir, "a", "a\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	tmp := t.TempDir()
	wt := filepath.Join(tmp, "wt")
	git("worktree", "add", "--detach", "--quiet", wt, "HEAD")
	git("worktree", "lock", wt)
	removeTempWorktree(dir, wt, tmp)
	assert.NotContains(t, git("worktree", "list", "--porcelain"), wt)
	_, err := os.Stat(tmp)
	assert.True(t, os.IsNotExist(err))
}
