package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// cleanGit runs git in dir through the scrubbed helper and returns trimmed
// stdout+stderr. It is the ONLY way this test inspects repositories, so the
// observer cannot itself be redirected by the GIT_DIR the test injects.
func cleanGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v in %s: %s", args, dir, string(out))
	return strings.TrimSpace(string(out))
}

// repoFingerprint is everything the incident changed in the victim repo:
// HEAD (the branch rename), the branch list (itervox/eng-* appeared),
// core.bare (flipped to true), the total commit count (~50 new commits) and
// the worktree list (worktrees appeared under /var/folders).
type repoFingerprint struct {
	Head      string
	HeadRef   string
	Branches  string
	CoreBare  string
	Commits   string
	Worktrees int
}

func fingerprint(t *testing.T, dir string) repoFingerprint {
	t.Helper()
	return repoFingerprint{
		Head:      cleanGit(t, dir, "rev-parse", "HEAD"),
		HeadRef:   cleanGit(t, dir, "symbolic-ref", "HEAD"),
		Branches:  cleanGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"),
		CoreBare:  cleanGit(t, dir, "config", "--get", "core.bare"),
		Commits:   cleanGit(t, dir, "rev-list", "--all", "--count"),
		Worktrees: len(strings.Split(cleanGit(t, dir, "worktree", "list", "--porcelain"), "\n\n")),
	}
}

// TestGitCommandsIgnoreInheritedGitDir is the regression test for the
// pre-push incident: `git push` ran the lefthook pre-push hook, which ran
// `make test` with GIT_DIR (and friends) exported by git. Every workspace git
// call inherited it and operated on the developer's real repository instead
// of the per-test temporary one.
//
// Here the "real repository" is a decoy with a known state. GIT_DIR and
// GIT_WORK_TREE point at it while the production workspace operations run
// against a separate temp repo; the decoy must come out byte-for-byte the
// same and the temp repo must receive every change.
func TestGitCommandsIgnoreInheritedGitDir(t *testing.T) {
	ctx := context.Background()

	// Victim stand-in with a known HEAD.
	decoy := t.TempDir()
	cleanGit(t, decoy, "init", "-q", "-b", "decoy-main")
	cleanGit(t, decoy, "config", "user.email", "decoy@test.com")
	cleanGit(t, decoy, "config", "user.name", "Decoy")
	cleanGit(t, decoy, "commit", "-q", "--allow-empty", "-m", "decoy root")
	before := fingerprint(t, decoy)
	require.Equal(t, "false", before.CoreBare)
	require.Equal(t, "refs/heads/decoy-main", before.HeadRef)

	// The repository the operations are meant to touch.
	root := t.TempDir()
	cleanGit(t, root, "init", "-q", "-b", "main")
	cleanGit(t, root, "config", "user.email", "test@test.com")
	cleanGit(t, root, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(root, "base.txt"), []byte("v1\n"), 0o644))
	cleanGit(t, root, "add", "base.txt")
	cleanGit(t, root, "commit", "-q", "-m", "base v1")

	// The decoy check runs as a cleanup so it reports even when an earlier
	// step aborts the test — a redirected git call usually breaks the
	// positive path too, and the decoy diff is the evidence that matters.
	// Registered before t.Setenv, so it runs after the env is restored
	// (cleanups are LIFO); cleanGit scrubs regardless.
	t.Cleanup(func() {
		after := fingerprint(t, decoy)
		assert.Equal(t, before, after, "inherited GIT_DIR/GIT_WORK_TREE redirected git into the decoy repository")
	})

	// What a git hook exports. From here on, anything that builds a git
	// command without scrubbing the environment hits the decoy.
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	cfg := newWorktreeConfig(root)
	mgr := workspace.NewManager(cfg)

	// 1. EnsureWorkspace: fetch + worktree add -b.
	ws, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.DirExists(t, ws.Path)

	// 2. Handoff commit (the "chore(itervox): record agent handoff" path).
	handoffDir := filepath.Join(ws.Path, ".itervox", "handoff")
	require.NoError(t, os.MkdirAll(handoffDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(handoffDir, "2026-01-01T00-00-00Z_worker.md"), []byte("# handoff\n"), 0o644))
	staged, err := workspace.CommitPathOnly(ctx, ws.Path, ".itervox/handoff", "chore(itervox): record agent handoff")
	require.NoError(t, err)
	require.True(t, staged, "git add must succeed inside the worktree")

	// 3. Restack onto an advanced main: status, merge-base, rebase.
	require.NoError(t, os.WriteFile(filepath.Join(root, "base.txt"), []byte("v2\n"), 0o644))
	cleanGit(t, root, "commit", "-q", "-am", "base v2")
	outcome, err := mgr.RestackWorktree(ctx, "ENG-1", "itervox/eng-1", "main")
	require.NoError(t, err)

	// 4. Branch read.
	branch := workspace.GetCurrentBranch(ctx, ws.Path)

	// 5. Removal: worktree remove, prune, branch -D on a second workspace.
	ws2, err := mgr.EnsureWorkspace(ctx, "ENG-2", "itervox/eng-2")
	require.NoError(t, err)
	require.NoError(t, mgr.RemoveWorkspace(ctx, "ENG-2", "itervox/eng-2"))

	// The temp repo got every change (the decoy check is the cleanup above).
	assert.Equal(t, workspace.RestackRebased, outcome)
	assert.Equal(t, "itervox/eng-1", branch)
	assert.Contains(t, cleanGit(t, root, "log", "--format=%s", "itervox/eng-1"), "chore(itervox): record agent handoff")
	cleanGit(t, root, "merge-base", "--is-ancestor", "main", "itervox/eng-1")
	assert.Equal(t, "false", cleanGit(t, root, "config", "--get", "core.bare"))
	assert.Equal(t, "refs/heads/main", cleanGit(t, root, "symbolic-ref", "HEAD"))
	assert.NotContains(t, cleanGit(t, root, "branch", "--list"), "itervox/eng-2", "RemoveWorkspace must delete the branch in the temp repo")
	assert.NoDirExists(t, ws2.Path)
}
