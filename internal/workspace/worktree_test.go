package workspace_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

func TestSlugifyIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ENG-123", "eng-123"},
		{"eng_123", "eng-123"},
		{"My Issue #7", "my-issue-7"},
		{"  spaces  ", "spaces"},
		{"FEAT/login", "feat-login"},
		{"ABC", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, workspace.SlugifyIdentifier(tc.in))
		})
	}
}

func TestResolveWorktreeBranch_UsesBranchNameWhenSet(t *testing.T) {
	branch := "fix/my-bug"
	result := workspace.ResolveWorktreeBranch(&branch, "ENG-123")
	assert.Equal(t, "fix/my-bug", result)
}

func TestResolveWorktreeBranch_SkipsDefaultBranches(t *testing.T) {
	for _, def := range []string{"main", "master", "develop"} {
		def := def
		t.Run(def, func(t *testing.T) {
			result := workspace.ResolveWorktreeBranch(&def, "ENG-123")
			assert.Equal(t, "itervox/eng-123", result)
		})
	}
}

func TestResolveWorktreeBranch_NilBranchFallsBack(t *testing.T) {
	result := workspace.ResolveWorktreeBranch(nil, "ENG-123")
	assert.Equal(t, "itervox/eng-123", result)
}

func TestResolveWorktreeBranch_EmptyBranchFallsBack(t *testing.T) {
	empty := ""
	result := workspace.ResolveWorktreeBranch(&empty, "ENG-123")
	assert.Equal(t, "itervox/eng-123", result)
}

// initGitRepo creates a git repo with an initial commit in dir.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := gitexec.Command(context.Background(), dir, args...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git setup failed: %s", string(out))
	}
}

func worktreeManager(t *testing.T) (*workspace.Manager, string) {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	return workspace.NewManager(cfg), root
}

func TestEnsureWorktree_CreatesWorktree(t *testing.T) {
	mgr, root := worktreeManager(t)
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
	assert.Equal(t, filepath.Join(root, "worktrees", "ENG-1"), ws.Path)
	assert.DirExists(t, ws.Path)

	// Verify the worktree is on the expected branch
	cmd := gitexec.Command(context.Background(), ws.Path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "itervox/eng-1", strings.TrimSpace(string(out)))
}

func TestEnsureWorktree_ReusesExisting(t *testing.T) {
	mgr, _ := worktreeManager(t)
	ws1, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws1.CreatedNow)

	ws2, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.False(t, ws2.CreatedNow, "second call must not recreate existing worktree")
	assert.Equal(t, ws1.Path, ws2.Path)
}

func TestEnsureWorktree_BranchAlreadyExists(t *testing.T) {
	mgr, root := worktreeManager(t)

	// Create branch manually in the base repo (simulates partial previous run)
	cmd := gitexec.Command(context.Background(), root, "branch", "itervox/eng-1")
	require.NoError(t, cmd.Run())

	// EnsureWorkspace must succeed even though branch already exists
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
	assert.DirExists(t, ws.Path)
}

func TestRemoveWorktree_RemovesWorktreeAndBranch(t *testing.T) {
	mgr, root := worktreeManager(t)
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.DirExists(t, ws.Path)

	err = mgr.RemoveWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.NoDirExists(t, ws.Path)

	// Branch must also be deleted
	cmd := gitexec.Command(context.Background(), root, "branch", "--list", "itervox/eng-1")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "branch should be deleted after remove")
}

func TestRemoveWorktree_KeepsBranchWhenNameEmpty(t *testing.T) {
	mgr, root := worktreeManager(t)
	_, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)

	// Empty branchName = remove worktree but skip branch deletion
	err = mgr.RemoveWorkspace(context.Background(), "ENG-1", "")
	require.NoError(t, err)

	cmd := gitexec.Command(context.Background(), root, "branch", "--list", "itervox/eng-1")
	out, _ := cmd.Output()
	assert.NotEmpty(t, strings.TrimSpace(string(out)), "branch should be kept when branchName is empty")
}

func TestRemoveWorktree_MissingWorktreeIsNoOp(t *testing.T) {
	mgr, _ := worktreeManager(t)
	err := mgr.RemoveWorkspace(context.Background(), "nonexistent", "itervox/nonexistent")
	assert.NoError(t, err, "removing a non-existent worktree must not error")
}

// --- Bare-clone-backed worktree tests ---

func bareWorktreeManager(t *testing.T) (*workspace.Manager, string) {
	t.Helper()
	upstream := setupUpstreamRepo(t) // defined in bare_test.go, same package
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.CloneURL = upstream
	cfg.Workspace.BaseBranch = "main"
	return workspace.NewManager(cfg), root
}

func TestEnsureWorktree_BareClone_CreatesWorktree(t *testing.T) {
	mgr, root := bareWorktreeManager(t)
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
	assert.Equal(t, filepath.Join(root, "worktrees", "ENG-1"), ws.Path)
	assert.DirExists(t, ws.Path)

	// Verify the worktree is on the expected branch
	cmd := gitexec.Command(context.Background(), ws.Path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "itervox/eng-1", strings.TrimSpace(string(out)))
}

func TestEnsureWorktree_BareClone_ReusesExisting(t *testing.T) {
	mgr, _ := bareWorktreeManager(t)
	ws1, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.True(t, ws1.CreatedNow)

	ws2, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.False(t, ws2.CreatedNow)
	assert.Equal(t, ws1.Path, ws2.Path)
}

func TestRemoveWorktree_BareClone(t *testing.T) {
	mgr, root := bareWorktreeManager(t)
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.DirExists(t, ws.Path)

	err = mgr.RemoveWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.NoDirExists(t, ws.Path)

	// Branch must also be deleted from the bare repo
	cmd := gitexec.Command(context.Background(), filepath.Join(root, ".bare"), "branch", "--list", "itervox/eng-1")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "branch should be deleted from bare repo")
}
