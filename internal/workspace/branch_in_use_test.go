package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

const prBranch = "user/a-1-some-branch"

// upstreamWithPRBranch is an "origin" with main and A-1's PR branch.
func upstreamWithPRBranch(t *testing.T) string {
	t.Helper()
	upstream := setupUpstreamRepo(t)
	work := t.TempDir()
	for _, args := range [][]string{
		{"clone", "-q", upstream, work},
		{"-C", work, "config", "user.email", "test@test.com"},
		{"-C", work, "config", "user.name", "Test"},
		{"-C", work, "checkout", "-q", "-b", prBranch},
		{"-C", work, "commit", "-q", "--allow-empty", "-m", "a-1 work"},
		{"-C", work, "push", "-q", "origin", prBranch},
	} {
		out, err := gitexec.Command(context.Background(), work, args...).CombinedOutput()
		require.NoError(t, err, "setup: %s: %s", args, out)
	}
	return upstream
}

// prBareManager is the reported layout: worktrees from a shared bare
// clone of upstream under root/.bare.
func prBareManager(t *testing.T, upstream string) (*workspace.Manager, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.CloneURL = upstream
	cfg.Workspace.BaseBranch = "main"
	return workspace.NewManager(cfg), root
}

// TestWorkspaceReusesOwnWorktreeForPRBranch (#193): an issue whose worktree
// already has the PR branch checked out gets that worktree back, and a stale
// registration of its own worktree (directory gone, git metadata left) is
// pruned and re-created instead of being reported as "in use".
func TestWorkspaceReusesOwnWorktreeForPRBranch(t *testing.T) {
	m, root := prBareManager(t, upstreamWithPRBranch(t))
	ctx := context.Background()

	ws, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	require.True(t, ws.CreatedNow)

	again, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err, "the issue's own worktree is reused")
	assert.False(t, again.CreatedNow)
	assert.Equal(t, ws.Path, again.Path)

	require.NoError(t, os.RemoveAll(ws.Path)) // metadata stays in .bare/worktrees
	recreated, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err, "a stale registration of the issue's own worktree must not hold it")
	assert.True(t, recreated.CreatedNow)
	assert.Equal(t, filepath.Join(root, "worktrees", "A-1"), recreated.Path)
	assert.True(t, workspace.BranchCheckedOutAt(recreated.Path, prBranch))
}

// TestEnsureWorkspaceReportsBranchInUseByAnotherIssue (#193): B-1 asking for
// the branch A-1's worktree holds gets a typed BranchInUseError naming A-1,
// before any `git worktree add` runs, and no worktree is left for B-1.
func TestEnsureWorkspaceReportsBranchInUseByAnotherIssue(t *testing.T) {
	m, root := prBareManager(t, upstreamWithPRBranch(t))
	ctx := context.Background()
	_, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)

	_, err = m.EnsureWorkspace(ctx, "B-1", prBranch)
	require.Error(t, err)
	assert.True(t, errors.Is(err, workspace.ErrBranchInUse), "got %v", err)
	var biu *workspace.BranchInUseError
	require.True(t, errors.As(err, &biu))
	assert.Equal(t, prBranch, biu.Branch)
	assert.Equal(t, "A-1", biu.Holder)
	assert.NoDirExists(t, filepath.Join(root, "worktrees", "B-1"))

	// A stale registration of ANOTHER issue's worktree does not hold B-1.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "worktrees", "A-1")))
	ws, err := m.EnsureWorkspace(ctx, "B-1", prBranch)
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
}

func TestBranchCheckedOutAt(t *testing.T) {
	m, root := prBareManager(t, upstreamWithPRBranch(t))
	ws, err := m.EnsureWorkspace(context.Background(), "A-1", prBranch)
	require.NoError(t, err)
	assert.True(t, workspace.BranchCheckedOutAt(ws.Path, prBranch))
	assert.False(t, workspace.BranchCheckedOutAt(ws.Path, "main"))
	out, err := gitexec.Command(context.Background(), ws.Path, "checkout", "-q", "--detach").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.False(t, workspace.BranchCheckedOutAt(ws.Path, prBranch), "a detached worktree frees the branch")
	assert.False(t, workspace.BranchCheckedOutAt(filepath.Join(root, "worktrees", "gone"), prBranch))

	// The repository's own checkout (legacy layout: .git is a directory).
	repo := t.TempDir()
	initGitRepo(t, repo)
	assert.True(t, workspace.BranchCheckedOutAt(repo, "main"))
}

// TestConcurrentBareFetchIsSerialized (#193): parallel dispatches fetch into
// one shared bare clone; git's ref locks made the overlapping fetches fail
// with "cannot lock ref". Every fetch into one bare clone runs alone. A git
// wrapper on PATH records any overlap and then runs the real git.
func TestConcurrentBareFetchIsSerialized(t *testing.T) {
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	upstream := upstreamWithPRBranch(t)
	root := t.TempDir()
	barePath, err := workspace.EnsureBareClone(context.Background(), root, upstream)
	require.NoError(t, err)

	bin := t.TempDir()
	lock := filepath.Join(t.TempDir(), "fetch.lock")
	overlaps := filepath.Join(t.TempDir(), "overlaps")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
*" fetch "*)
  if ! mkdir %[1]q 2>/dev/null; then echo overlap >> %[2]q; fi
  sleep 0.2
  %[3]q "$@"; rc=$?
  rmdir %[1]q 2>/dev/null
  exit $rc ;;
esac
exec %[3]q "$@"
`, lock, overlaps, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const callers = 6
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = workspace.FetchBare(context.Background(), barePath)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "caller %d", i)
	}
	raw, _ := os.ReadFile(overlaps)
	assert.Empty(t, strings.TrimSpace(string(raw)), "fetches into one bare clone overlapped")
}

// TestFindOpenPRURLQueriesWorktreeBranchExplicitly (#193): the post-run PR
// lookup names the worktree's own branch. A branch made from another branch
// of the bare clone tracks it, and an argument-less `gh pr view` follows that
// upstream — to the blocker's PR, which the worker then commented on the
// dependent as its own. The fake gh answers like that: A-1's PR without a
// branch argument, B-1's PR only for B-1's branch.
func TestFindOpenPRURLQueriesWorktreeBranchExplicitly(t *testing.T) {
	m, _ := prBareManager(t, upstreamWithPRBranch(t))
	stacked, err := m.EnsureWorkspaceFrom(context.Background(), "B-1", "user/b-1-work", prBranch)
	require.NoError(t, err)
	// #195 stopped new branches tracking their start point; recreate the
	// pre-#195 layout (the blocker's branch as upstream) by hand so this
	// test still pins the explicit branch argument.
	for _, kv := range [][2]string{{"branch.user/b-1-work.remote", "origin"}, {"branch.user/b-1-work.merge", "refs/heads/" + prBranch}} {
		out, err := gitexec.Command(context.Background(), stacked.Path, "config", kv[0], kv[1]).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}

	dir := t.TempDir()
	script := `#!/bin/sh
for a in "$@"; do [ "$a" = "user/b-1-work" ] && { printf 'https://github.com/o/r/pull/2'; exit 0; }; done
printf 'https://github.com/o/r/pull/1'
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	assert.Equal(t, "https://github.com/o/r/pull/2", workspace.FindOpenPRURL(context.Background(), stacked.Path))
}
