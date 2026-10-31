package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
)

// Review and continue: a run started after workspace.auto_clear_workspace
// removed the issue's worktree (and its local branch) must continue the
// issue's branch, not start over from the base branch. The branch survives
// on origin, where the earlier run pushed it for its PR.

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitexec.Command(context.Background(), dir, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// pushWorkThenAutoClear commits a file on the issue's worktree, pushes the
// branch to origin, and removes the workspace with its branch name, as the
// auto-clear after a successful run does. It returns the pushed commit.
func pushWorkThenAutoClear(t *testing.T, m *Manager, id, branch string) string {
	t.Helper()
	ctx := context.Background()
	ws, err := m.EnsureWorkspace(ctx, id, branch)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "work.txt"), []byte("agent work\n"), 0o644))
	gitIn(t, ws.Path, "add", "work.txt")
	gitIn(t, ws.Path, "commit", "-qm", "agent work")
	head := gitIn(t, ws.Path, "rev-parse", "HEAD")
	gitIn(t, ws.Path, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	require.NoError(t, m.RemoveWorkspace(ctx, id, branch))
	require.NoDirExists(t, ws.Path)
	return head
}

func assertContinuesBranch(t *testing.T, m *Manager, id, branch, pushed string) {
	t.Helper()
	ws, err := m.EnsureWorkspace(context.Background(), id, branch)
	require.NoError(t, err)
	assert.True(t, ws.CreatedNow)
	assert.Equal(t, branch, gitIn(t, ws.Path, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, pushed, gitIn(t, ws.Path, "rev-parse", "HEAD"),
		"the recreated worktree must continue the issue's pushed branch, not the base")
	assert.FileExists(t, filepath.Join(ws.Path, "work.txt"))
}

// TestWorktreeRecreatedFromOriginBranchAfterAutoClear_Local covers the
// operator's own clone (no workspace.clone_url).
func TestWorktreeRecreatedFromOriginBranchAfterAutoClear_Local(t *testing.T) {
	upstream := setupUpstreamRepoInternal(t)
	root := filepath.Join(t.TempDir(), "repo")
	gitIn(t, filepath.Dir(root), "clone", "-q", upstream, root)
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	m := NewManager(cfg)

	pushed := pushWorkThenAutoClear(t, m, "ENG-7", "itervox/eng-7")
	assertContinuesBranch(t, m, "ENG-7", "itervox/eng-7", pushed)
}

// TestWorktreeRecreatedFromOriginBranchAfterAutoClear_Bare covers a
// workspace.clone_url bare clone.
func TestWorktreeRecreatedFromOriginBranchAfterAutoClear_Bare(t *testing.T) {
	upstream := setupUpstreamRepoInternal(t)
	cfg := &config.Config{}
	cfg.Workspace.Root = t.TempDir()
	cfg.Workspace.Worktree = true
	cfg.Workspace.CloneURL = upstream
	cfg.Workspace.BaseBranch = "main"
	m := NewManager(cfg)

	pushed := pushWorkThenAutoClear(t, m, "ENG-8", "itervox/eng-8")
	assertContinuesBranch(t, m, "ENG-8", "itervox/eng-8", pushed)
}

// setupUpstreamRepoInternal is a bare upstream with one commit on main.
func setupUpstreamRepoInternal(t *testing.T) string {
	t.Helper()
	work := initUpstream(t, filepath.Join(t.TempDir(), "work"), "base")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	gitIn(t, work, "clone", "-q", "--bare", work, upstream)
	return upstream
}
