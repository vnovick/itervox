package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitexec.Command(context.Background(), dir, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, string(out))
}

// TestEnsureWorkspaceFromUsesStartPointWhenItExists proves stacking actually
// changes what the new branch is based on — the whole point of the feature.
// A commit that exists only on the blocker's branch must be present in the
// stacked worktree.
func TestEnsureWorkspaceFromUsesStartPointWhenItExists(t *testing.T) {
	mgr, root := worktreeManager(t)

	// Give the blocker's branch a commit main does not have.
	gitIn(t, root, "checkout", "-q", "-b", "itervox/eng-1")
	require.NoError(t, os.WriteFile(filepath.Join(root, "blocker-work"), []byte("x"), 0o644))
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "blocker work")
	gitIn(t, root, "checkout", "-q", "main")

	ws, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-2", "itervox/eng-2", "itervox/eng-1")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(ws.Path, "blocker-work"),
		"a stacked worktree must start from the blocker's branch, not base_branch")
}

// TestEnsureWorkspaceFromFallsBackWhenStartPointMissing is the safety
// property. The blocker's branch may legitimately be absent — not yet
// dispatched, worked on another machine, worktree cleared. Stacking is review
// ergonomics, so it must degrade to the normal base rather than fail the
// dispatch and stall the issue.
func TestEnsureWorkspaceFromFallsBackWhenStartPointMissing(t *testing.T) {
	mgr, root := worktreeManager(t)

	ws, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-2", "itervox/eng-2", "itervox/never-existed")
	require.NoError(t, err, "an unresolvable start point must not fail the dispatch")
	require.DirExists(t, ws.Path)
	require.NoFileExists(t, filepath.Join(ws.Path, "blocker-work"),
		"nothing was stacked, so the worktree is based on the normal branch")
	_ = root
}

// TestEnsureWorkspaceEqualsEmptyStartPoint pins that the pre-existing entry
// point is unchanged — invariant 4: no behaviour change for anyone who has
// not opted in.
func TestEnsureWorkspaceEqualsEmptyStartPoint(t *testing.T) {
	mgr, _ := worktreeManager(t)

	plain, err := mgr.EnsureWorkspace(context.Background(), "ENG-3", "itervox/eng-3")
	require.NoError(t, err)
	require.DirExists(t, plain.Path)

	var provider workspace.Provider = mgr
	_, isStacked := provider.(workspace.StackedProvider)
	require.True(t, isStacked, "Manager must satisfy the optional StackedProvider interface")
}

// TestEnsureWorkspaceFromReportsStackedOn (#73): the returned workspace says
// which branch it is stacked on — for a new worktree created from the
// blocker's branch, for a reused worktree whose history contains it, and not
// at all when stacking fell back to base_branch.
func TestEnsureWorkspaceFromReportsStackedOn(t *testing.T) {
	mgr, root := worktreeManager(t)
	gitIn(t, root, "checkout", "-q", "-b", "itervox/eng-1")
	require.NoError(t, os.WriteFile(filepath.Join(root, "blocker-work"), []byte("x"), 0o644))
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "blocker work")
	gitIn(t, root, "checkout", "-q", "main")

	ws, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-2", "itervox/eng-2", "itervox/eng-1")
	require.NoError(t, err)
	require.True(t, ws.CreatedNow)
	require.Equal(t, "itervox/eng-1", ws.StackedOn, "new worktree created from the blocker's branch")

	again, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-2", "itervox/eng-2", "itervox/eng-1")
	require.NoError(t, err)
	require.False(t, again.CreatedNow)
	require.Equal(t, "itervox/eng-1", again.StackedOn, "reused worktree still has the blocker in its history")

	missing, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-3", "itervox/eng-3", "itervox/never-existed")
	require.NoError(t, err)
	require.Empty(t, missing.StackedOn, "fell back to base_branch, so not stacked")

	// A worktree created from main is not stacked on a blocker branch that has
	// commits main lacks, even when that branch is requested on reuse.
	plain, err := mgr.EnsureWorkspace(context.Background(), "ENG-4", "itervox/eng-4")
	require.NoError(t, err)
	require.Empty(t, plain.StackedOn)
	reused, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-4", "itervox/eng-4", "itervox/eng-1")
	require.NoError(t, err)
	require.Empty(t, reused.StackedOn, "the blocker's commits are not in this worktree's history")
}

// TestStackedOnRequiresTheBlockersOwnCommits pins review findings on #73:
// StackedOn reflects the worktree's actual history, not the requested start
// point, and a blocker branch with nothing of its own is not a stack base.
func TestStackedOnRequiresTheBlockersOwnCommits(t *testing.T) {
	t.Run("existing branch checked out as is", func(t *testing.T) {
		mgr, root := worktreeManager(t)
		gitIn(t, root, "checkout", "-q", "-b", "itervox/eng-1")
		require.NoError(t, os.WriteFile(filepath.Join(root, "blocker-work"), []byte("x"), 0o644))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", "blocker work")
		gitIn(t, root, "checkout", "-q", "main")
		// The dependent's branch already exists, based on main (e.g. its
		// worktree was removed after a failed after_create hook).
		gitIn(t, root, "branch", "itervox/eng-5", "main")

		ws, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-5", "itervox/eng-5", "itervox/eng-1")
		require.NoError(t, err)
		require.True(t, ws.CreatedNow)
		require.NoFileExists(t, filepath.Join(ws.Path, "blocker-work"))
		require.Empty(t, ws.StackedOn, "the existing branch was checked out unchanged, so it is not stacked")
	})

	t.Run("blocker branch with no commits of its own", func(t *testing.T) {
		mgr, root := worktreeManager(t)
		gitIn(t, root, "branch", "itervox/eng-1", "main")
		ws, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-2", "itervox/eng-2", "itervox/eng-1")
		require.NoError(t, err)
		require.Empty(t, ws.StackedOn, "a blocker equal to base adds nothing to stack on")
	})

	t.Run("blocker branch behind base", func(t *testing.T) {
		mgr, root := worktreeManager(t)
		gitIn(t, root, "branch", "itervox/eng-1", "main")
		require.NoError(t, os.WriteFile(filepath.Join(root, "later-on-main"), []byte("x"), 0o644))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", "later main work")
		plain, err := mgr.EnsureWorkspace(context.Background(), "ENG-3", "itervox/eng-3")
		require.NoError(t, err)
		require.Empty(t, plain.StackedOn)
		reused, err := mgr.EnsureWorkspaceFrom(context.Background(), "ENG-3", "itervox/eng-3", "itervox/eng-1")
		require.NoError(t, err)
		require.Empty(t, reused.StackedOn, "retargeting to a stale blocker branch would widen the PR")
	})
}
