package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workspace"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
	return string(out)
}

// newWorktreeConfig builds a worktree-mode config rooted at root.
func newWorktreeConfig(root string) *config.Config {
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	return cfg
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// stackedRepo builds a root repo on main plus a worktree for ENG-1 branched
// from main, then advances main so the worktree's base is stale — the exact
// shape a merged blocker leaves behind.
func stackedRepo(t *testing.T) (*workspace.Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	writeFile(t, root, "base.txt", "v1\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base v1")

	cfg := newWorktreeConfig(root)
	mgr := workspace.NewManager(cfg)
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)

	// The dependent branch adds its own commit.
	writeFile(t, ws.Path, "dependent.txt", "work\n")
	git(t, ws.Path, "add", ".")
	git(t, ws.Path, "commit", "-m", "dependent work")

	// main advances — the blocker merged.
	writeFile(t, root, "base.txt", "v2\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base v2 (blocker merged)")

	return mgr, root, ws.Path
}

// TestRestackReplaysOntoUpdatedBase is issue #60's core mechanic: after a
// blocker merges, the dependent branch is replayed onto the new base and keeps
// its own commit.
func TestRestackReplaysOntoUpdatedBase(t *testing.T) {
	mgr, _, wtPath := stackedRepo(t)

	outcome, err := mgr.RestackWorktree(context.Background(), "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err)
	assert.Equal(t, workspace.RestackRebased, outcome)

	// The dependent's own work survives...
	assert.FileExists(t, filepath.Join(wtPath, "dependent.txt"))
	// ...and the merged base is now present.
	body, readErr := os.ReadFile(filepath.Join(wtPath, "base.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "v2\n", string(body), "the rebased branch must carry the merged base content")
}

// TestRestackIsANoOpWhenAlreadyCurrent pins that an up-to-date branch is not
// rewritten. Replaying it anyway would change commit hashes and invalidate an
// open PR's review state for no benefit.
func TestRestackIsANoOpWhenAlreadyCurrent(t *testing.T) {
	mgr, _, wtPath := stackedRepo(t)
	ctx := context.Background()

	_, err := mgr.RestackWorktree(ctx, "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err)
	headAfterFirst := git(t, wtPath, "rev-parse", "HEAD")

	outcome, err := mgr.RestackWorktree(ctx, "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err)
	assert.Equal(t, workspace.RestackUpToDate, outcome)
	assert.Equal(t, headAfterFirst, git(t, wtPath, "rev-parse", "HEAD"),
		"a second restack must not rewrite commits")
}

// TestRestackAbortsOnConflictAndLeavesBranchIntact is the safety property that
// matters most: a conflict must leave the branch byte-identical to before, so a
// failed restack can never lose or mangle work.
func TestRestackAbortsOnConflictAndLeavesBranchIntact(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	writeFile(t, root, "shared.txt", "original\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "shared v1")

	mgr := workspace.NewManager(newWorktreeConfig(root))
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)

	// Both sides edit the same line — a guaranteed conflict.
	writeFile(t, ws.Path, "shared.txt", "dependent edit\n")
	git(t, ws.Path, "add", ".")
	git(t, ws.Path, "commit", "-m", "dependent edit")
	headBefore := git(t, ws.Path, "rev-parse", "HEAD")

	writeFile(t, root, "shared.txt", "base edit\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base edit")

	outcome, err := mgr.RestackWorktree(context.Background(), "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err, "a conflict is an outcome, not an error")
	assert.Equal(t, workspace.RestackConflict, outcome)

	assert.Equal(t, headBefore, git(t, ws.Path, "rev-parse", "HEAD"),
		"an aborted rebase must leave HEAD exactly where it was")
	body, _ := os.ReadFile(filepath.Join(ws.Path, "shared.txt"))
	assert.Equal(t, "dependent edit\n", string(body),
		"the dependent's content must survive a failed restack untouched")
	// A worktree left mid-rebase would break every later operation on it.
	assert.NoDirExists(t, filepath.Join(ws.Path, ".git", "rebase-merge"))
}

// TestRestackSkipsDirtyWorktree pins that uncommitted work is never rewritten.
// It is unpushed and unbacked-up; refusing is the only safe answer.
func TestRestackSkipsDirtyWorktree(t *testing.T) {
	mgr, _, wtPath := stackedRepo(t)
	writeFile(t, wtPath, "in-progress.txt", "half done\n")

	outcome, err := mgr.RestackWorktree(context.Background(), "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err)
	assert.Equal(t, workspace.RestackSkippedDirty, outcome)
	assert.FileExists(t, filepath.Join(wtPath, "in-progress.txt"),
		"untracked work must still be there")
}

// TestRestackRequiresABaseRef pins that an empty base is rejected rather than
// silently rebasing onto whatever git resolves "" to.
func TestRestackRequiresABaseRef(t *testing.T) {
	mgr, _, _ := stackedRepo(t)
	_, err := mgr.RestackWorktree(context.Background(), "ENG-1", "itervox/eng-1", "  ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a base ref")
}
