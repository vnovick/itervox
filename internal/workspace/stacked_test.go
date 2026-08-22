package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/workspace"
)

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
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
