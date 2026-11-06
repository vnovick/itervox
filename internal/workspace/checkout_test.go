package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/workspace"
)

// checkoutRig is a bare remote and a clone of it on main.
func checkoutRig(t *testing.T) (remote, clone string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var -> /private/var
	require.NoError(t, err)
	remote, clone = filepath.Join(root, "remote.git"), filepath.Join(root, "clone")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	git(t, root, "init", "-q", "-b", "main", clone)
	for _, kv := range [][2]string{{"user.name", "T"}, {"user.email", "t@example.invalid"}, {"commit.gpgsign", "false"}} {
		git(t, clone, "config", kv[0], kv[1])
	}
	writeFile(t, clone, "README.md", "x\n")
	git(t, clone, "add", "README.md")
	git(t, clone, "commit", "-q", "-m", "base")
	git(t, clone, "remote", "add", "origin", remote)
	git(t, clone, "push", "-q", "origin", "main")
	return remote, clone
}

// #252: in place, the issue branch is created in the checkout; a fresh
// checkout continues the branch a previous run pushed; nothing is ever
// removed.
func TestCheckoutProviderInPlaceContinuesOriginBranch(t *testing.T) {
	ctx := context.Background()
	remote, clone := checkoutRig(t)
	p := &workspace.CheckoutProvider{Dir: clone}

	ws, err := p.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.Equal(t, clone, ws.Path)
	assert.False(t, ws.CreatedNow, "the checkout was not created by itervox")
	assert.Equal(t, "itervox/eng-1", workspace.GetCurrentBranch(ctx, clone))
	writeFile(t, clone, "work.txt", "w\n")
	git(t, clone, "add", "work.txt")
	git(t, clone, "commit", "-q", "-m", "work")
	git(t, clone, "push", "-q", "origin", "itervox/eng-1")
	_, err = p.EnsureWorkspace(ctx, "ENG-1", "main")
	require.Error(t, err, "never works on a default branch")

	fresh := filepath.Join(filepath.Dir(clone), "fresh")
	git(t, filepath.Dir(clone), "clone", "-q", remote, fresh)
	p2 := &workspace.CheckoutProvider{Dir: fresh}
	_, err = p2.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(fresh, "work.txt"), "continued from origin's branch")
	require.NoError(t, p2.RemoveWorkspace(ctx, "ENG-1", "itervox/eng-1"))
	assert.DirExists(t, fresh)
}

// #252: --worktree puts the issue branch in a worktree under .itervox/run,
// which ExcludeRunDir keeps out of `git status`.
func TestCheckoutProviderWorktreeIsExcludedFromStatus(t *testing.T) {
	ctx := context.Background()
	_, clone := checkoutRig(t)
	require.NoError(t, workspace.ExcludeRunDir(ctx, clone))
	require.NoError(t, workspace.ExcludeRunDir(ctx, clone), "idempotent")
	p := &workspace.CheckoutProvider{Dir: clone, Worktree: true}

	ws, err := p.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
	assert.Equal(t, filepath.Join(clone, ".itervox", "run", "worktrees", "ENG-1"), ws.Path)
	assert.Equal(t, "itervox/eng-1", workspace.GetCurrentBranch(ctx, ws.Path))
	assert.Equal(t, "main", workspace.GetCurrentBranch(ctx, clone), "the checkout keeps its branch")
	assert.Empty(t, strings.TrimSpace(git(t, clone, "status", "--porcelain")))
	again, err := p.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.False(t, again.CreatedNow, "reused")

	exclude, err := os.ReadFile(filepath.Join(clone, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(exclude), "/.itervox/run/"))
	root, err := workspace.CheckoutRoot(ctx, filepath.Join(clone, ".git", ".."))
	require.NoError(t, err)
	assert.Equal(t, clone, root)
}
