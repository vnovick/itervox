package workspace_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// gitOut runs git in dir and returns its trimmed stdout, failing the test on
// error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %s: %s", args, out)
	return strings.TrimSpace(string(out))
}

// pushUpstreamCommit adds an empty commit to branch on upstream (creating the
// branch from main when it does not exist) and returns its SHA.
func pushUpstreamCommit(t *testing.T, upstream, branch, msg string) string {
	t.Helper()
	work := t.TempDir()
	gitOut(t, work, "clone", "-q", upstream, work)
	gitOut(t, work, "-C", work, "config", "user.email", "test@test.com")
	gitOut(t, work, "-C", work, "config", "user.name", "Test")
	if gitexec.Command(context.Background(), work, "-C", work, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch).Run() == nil {
		gitOut(t, work, "-C", work, "checkout", "-q", "-B", branch, "origin/"+branch)
	} else {
		gitOut(t, work, "-C", work, "checkout", "-q", "-b", branch)
	}
	gitOut(t, work, "-C", work, "commit", "-q", "--allow-empty", "-m", msg)
	gitOut(t, work, "-C", work, "push", "-q", "origin", branch)
	return gitOut(t, work, "-C", work, "rev-parse", "HEAD")
}

// TestBareFetchSucceedsWithCheckedOutWorktree (#195): with a worktree holding
// a branch that also exists on origin, the old `+refs/heads/*:refs/heads/*`
// refspec made every fetch exit 128 ("refusing to fetch into branch ...
// checked out at ...") and the base never moved. The fetch must succeed and
// origin/<base> must move to upstream's new commit.
func TestBareFetchSucceedsWithCheckedOutWorktree(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	m, root := prBareManager(t, upstream)
	ctx := context.Background()

	ws, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	require.True(t, workspace.BranchCheckedOutAt(ws.Path, prBranch))

	newMain := pushUpstreamCommit(t, upstream, "main", "two")
	pushUpstreamCommit(t, upstream, prBranch, "pushed elsewhere")

	bare := workspace.BarePath(root)
	require.NoError(t, workspace.FetchBare(ctx, bare), "a checked-out worktree must not abort the bare fetch")
	assert.Equal(t, newMain, gitOut(t, bare, "-C", bare, "rev-parse", "refs/remotes/origin/main"),
		"origin/main must move to upstream's new commit")

	// A new worktree starts from the fresh base, and the clone's local base
	// (HEAD's branch) is fast-forwarded with it.
	c1, err := m.EnsureWorkspace(ctx, "C-1", "itervox/c-1")
	require.NoError(t, err)
	assert.Equal(t, newMain, gitOut(t, c1.Path, "rev-parse", "HEAD"))
	assert.Equal(t, newMain, gitOut(t, bare, "-C", bare, "rev-parse", "refs/heads/main"), "local base fast-forwarded")
	assert.Equal(t, "main", gitOut(t, bare, "-C", bare, "symbolic-ref", "--short", "HEAD"))

	// With main checked out in a worktree, the local base is never moved
	// under it, and new worktrees still start from origin/main.
	gitOut(t, bare, "-C", bare, "worktree", "add", "-q", root+"/worktrees/on-main", "main")
	newest := pushUpstreamCommit(t, upstream, "main", "three")
	d1, err := m.EnsureWorkspace(ctx, "D-1", "itervox/d-1")
	require.NoError(t, err)
	assert.Equal(t, newest, gitOut(t, d1.Path, "rev-parse", "HEAD"), "new worktree starts from origin/main")
	assert.Equal(t, newMain, gitOut(t, bare, "-C", bare, "rev-parse", "refs/heads/main"), "a checked-out base is not moved")
}

// upstreamOf returns the configured upstream of branch in gitDir as
// "remote merge", or "" when it has none.
func upstreamOf(t *testing.T, gitDir, branch string) string {
	t.Helper()
	get := func(key string) string {
		out, err := gitexec.Command(context.Background(), gitDir, "-C", gitDir, "config", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	remote, merge := get("branch."+branch+".remote"), get("branch."+branch+".merge")
	if remote == "" && merge == "" {
		return ""
	}
	return remote + " " + merge
}

// TestNewWorktreeDoesNotTrackAnotherLocalBranch (#195): a worktree branch
// made from another issue's branch (stacked) or from the base must not get
// that branch as its upstream — the layout behind #193, where B-1…B-8 all
// tracked A-1's branch and `gh pr view` / `git push` acted on A-1's PR.
func TestNewWorktreeDoesNotTrackAnotherLocalBranch(t *testing.T) {
	m, root := prBareManager(t, upstreamWithPRBranch(t))
	ctx := context.Background()
	bare := workspace.BarePath(root)

	_, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	stacked, err := m.EnsureWorkspaceFrom(ctx, "B-1", "user/b-1-work", prBranch)
	require.NoError(t, err)
	assert.Equal(t, prBranch, stacked.StackedOn, "precondition: B-1 is stacked on A-1's branch")
	_, err = m.EnsureWorkspace(ctx, "C-1", "user/c-1-work")
	require.NoError(t, err)

	assert.Empty(t, upstreamOf(t, bare, "user/b-1-work"), "a branch stacked on A-1's branch must not track it")
	assert.Empty(t, upstreamOf(t, bare, "user/c-1-work"), "a branch made from the base must not track the base")
}

// TestFetchKeepsUnpushedAgentCommits (#195): a fetch never writes local
// branches. An agent's unpushed commit survives on the branch its worktree
// has checked out, AND on a branch no worktree holds (the legacy forced
// refspec would have reset that one to origin's version).
func TestFetchKeepsUnpushedAgentCommits(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	m, root := prBareManager(t, upstream)
	ctx := context.Background()
	bare := workspace.BarePath(root)

	ws, err := m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	gitOut(t, ws.Path, "-c", "user.email=a@b", "-c", "user.name=agent", "commit", "-q", "--allow-empty", "-m", "unpushed agent work")
	checkedOut := gitOut(t, ws.Path, "rev-parse", "HEAD")

	// A second branch with unpushed work whose worktree is gone but whose
	// branch remains, and which origin also has (pushed earlier, then moved
	// on elsewhere).
	other := "user/e-1-work"
	pushUpstreamCommit(t, upstream, other, "e-1 first push")
	require.NoError(t, workspace.FetchBare(ctx, bare))
	ws2, err := m.EnsureWorkspace(ctx, "E-1", other)
	require.NoError(t, err)
	gitOut(t, ws2.Path, "-c", "user.email=a@b", "-c", "user.name=agent", "commit", "-q", "--allow-empty", "-m", "unpushed e-1 work")
	idle := gitOut(t, ws2.Path, "rev-parse", "HEAD")
	gitOut(t, bare, "-C", bare, "worktree", "remove", "--force", ws2.Path)

	pushUpstreamCommit(t, upstream, prBranch, "someone else pushed")
	upstreamOther := pushUpstreamCommit(t, upstream, other, "someone else pushed e-1")

	require.NoError(t, workspace.FetchBare(ctx, bare))
	assert.Equal(t, checkedOut, gitOut(t, ws.Path, "rev-parse", "HEAD"), "the checked-out branch keeps the agent's commit")
	assert.Equal(t, checkedOut, gitOut(t, bare, "-C", bare, "rev-parse", "refs/heads/"+prBranch))
	assert.Equal(t, idle, gitOut(t, bare, "-C", bare, "rev-parse", "refs/heads/"+other), "a branch no worktree holds keeps its unpushed commit")
	assert.Equal(t, upstreamOther, gitOut(t, bare, "-C", bare, "rev-parse", "refs/remotes/origin/"+other), "origin's version lands in the remote-tracking ref")
}

// legacyBareClone makes the pre-#195 layout: a bare clone fetching with
// `+refs/heads/*:refs/heads/*`, no refs/remotes/origin/*, and no fetch
// status file.
func legacyBareClone(t *testing.T, upstream string) (root, bare string) {
	t.Helper()
	root = t.TempDir()
	bare = workspace.BarePath(root)
	gitOut(t, root, "clone", "-q", "--bare", upstream, bare)
	gitOut(t, bare, "-C", bare, "config", "remote.origin.fetch", workspace.LegacyBareFetchRefspec)
	return root, bare
}

// TestBareCloneMigratesOldRefspec (#195): an existing clone with the legacy
// refspec and a worktree holding an origin branch is migrated on first use —
// refspec rewritten (an operator's extra refspec kept), refs/remotes/origin/*
// populated, the fetch succeeds and the base moves — without touching a
// local branch. A second use changes nothing.
func TestBareCloneMigratesOldRefspec(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	root, bare := legacyBareClone(t, upstream)
	ctx := context.Background()
	const extra = "+refs/tags/*:refs/tags/*"
	gitOut(t, bare, "-C", bare, "config", "--add", "remote.origin.fetch", extra)
	gitOut(t, bare, "-C", bare, "worktree", "add", "-q", root+"/worktrees/A-1", prBranch)
	newMain := pushUpstreamCommit(t, upstream, "main", "two")
	pushUpstreamCommit(t, upstream, prBranch, "moved upstream")

	require.Error(t, gitexec.Command(ctx, bare, "-C", bare, "fetch", "--all", "--prune").Run(),
		"precondition: the legacy layout fails to fetch")
	require.True(t, workspace.BareRefspecNeedsMigration(ctx, bare))
	heads := gitOut(t, bare, "-C", bare, "for-each-ref", "refs/heads")

	require.NoError(t, workspace.FetchBare(ctx, bare))
	assert.Equal(t, workspace.BareFetchRefspec+"\n"+extra, gitOut(t, bare, "-C", bare, "config", "--get-all", "remote.origin.fetch"))
	assert.False(t, workspace.BareRefspecNeedsMigration(ctx, bare))
	assert.Equal(t, newMain, gitOut(t, bare, "-C", bare, "rev-parse", "refs/remotes/origin/main"))
	assert.Equal(t, heads, gitOut(t, bare, "-C", bare, "for-each-ref", "refs/heads"), "migration never moves a local branch")

	require.NoError(t, workspace.FetchBare(ctx, bare), "idempotent")
	assert.Equal(t, workspace.BareFetchRefspec+"\n"+extra, gitOut(t, bare, "-C", bare, "config", "--get-all", "remote.origin.fetch"))
	status, err := workspace.ReadBareFetchStatus(bare)
	require.NoError(t, err)
	assert.False(t, status.Failing())
	assert.False(t, status.LastSuccessAt.IsZero())
}

// TestMigrationRepairsCrossBranchUpstream (#195): branches that track
// ANOTHER branch (#193's layout) are repaired on the first successful fetch
// after migration: pointed at their own name on origin when origin has it,
// unset otherwise. Correct upstreams and upstreams on other remotes are left
// alone, and no ref moves (including the one a worktree has checked out).
func TestMigrationRepairsCrossBranchUpstream(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	pushUpstreamCommit(t, upstream, "user/b-2-work", "b-2 pushed")
	root, bare := legacyBareClone(t, upstream)
	ctx := context.Background()
	gitOut(t, bare, "-C", bare, "branch", "user/b-1-work", prBranch)
	gitOut(t, bare, "-C", bare, "worktree", "add", "-q", root+"/worktrees/B-2", "user/b-2-work")
	gitOut(t, bare, "-C", bare, "remote", "add", "fork", upstream)
	gitOut(t, bare, "-C", bare, "branch", "user/f-1", prBranch)
	set := func(branch, remote, merge string) {
		gitOut(t, bare, "-C", bare, "config", "branch."+branch+".remote", remote)
		gitOut(t, bare, "-C", bare, "config", "branch."+branch+".merge", merge)
	}
	set("user/b-1-work", "origin", "refs/heads/"+prBranch) // local only
	set("user/b-2-work", "origin", "refs/heads/"+prBranch) // on origin, checked out
	set("user/f-1", "fork", "refs/heads/"+prBranch)        // another remote
	set("main", "origin", "refs/heads/main")               // already correct
	heads := gitOut(t, bare, "-C", bare, "for-each-ref", "refs/heads")

	require.NoError(t, workspace.FetchBare(ctx, bare))
	assert.Empty(t, upstreamOf(t, bare, "user/b-1-work"), "origin lacks the branch: upstream unset")
	assert.Equal(t, "origin refs/heads/user/b-2-work", upstreamOf(t, bare, "user/b-2-work"), "origin has it: its own branch")
	assert.Equal(t, "fork refs/heads/"+prBranch, upstreamOf(t, bare, "user/f-1"), "another remote's upstream is the operator's")
	assert.Equal(t, "origin refs/heads/main", upstreamOf(t, bare, "main"))
	assert.Equal(t, heads, gitOut(t, bare, "-C", bare, "for-each-ref", "refs/heads"), "the repair changes config only")
	assert.Error(t, gitexec.Command(ctx, bare, "-C", bare, "config", "--get", "itervox.upstreamRepairPending").Run(), "repair marker cleared")

	// One-time: once migrated, a later fetch leaves upstream config alone
	// (here a cross-branch upstream an operator set by hand).
	set("user/b-1-work", "origin", "refs/heads/main")
	require.NoError(t, workspace.FetchBare(ctx, bare))
	assert.Equal(t, "origin refs/heads/main", upstreamOf(t, bare, "user/b-1-work"))
	assert.Equal(t, "origin refs/heads/user/b-2-work", upstreamOf(t, bare, "user/b-2-work"))
}

// TestPRBranchCheckoutFromRemote (#195): the "open PR detected, using PR
// branch" path. A PR branch only origin has is created from origin with
// origin/<branch> as upstream; an existing local copy that is behind is
// fast-forwarded; one with unpushed commits is never rewound; and a branch
// another worktree holds is reported in use without its ref moving.
func TestPRBranchCheckoutFromRemote(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	m, root := prBareManager(t, upstream)
	ctx := context.Background()
	bare := workspace.BarePath(root)
	_, err := m.EnsureWorkspace(ctx, "X-1", "itervox/x-1") // clones the bare
	require.NoError(t, err)

	// Remote-only PR branch.
	remoteOnly := pushUpstreamCommit(t, upstream, "user/d-1-pr", "d-1 pr")
	ws, err := m.EnsureWorkspace(ctx, "D-1", "user/d-1-pr")
	require.NoError(t, err)
	assert.Equal(t, remoteOnly, gitOut(t, ws.Path, "rev-parse", "HEAD"))
	assert.Equal(t, "origin refs/heads/user/d-1-pr", upstreamOf(t, bare, "user/d-1-pr"))

	// Local copy (from the clone) behind origin: fast-forwarded.
	newer := pushUpstreamCommit(t, upstream, prBranch, "a-1 pushed again")
	ws, err = m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	assert.Equal(t, newer, gitOut(t, ws.Path, "rev-parse", "HEAD"))

	// Stacking on A-1's checked-out branch while origin is ahead must not
	// fast-forward it under A-1's worktree.
	pushUpstreamCommit(t, upstream, prBranch, "pushed while a-1 runs")
	_, err = m.EnsureWorkspaceFrom(ctx, "S-1", "user/s-1-work", prBranch)
	require.NoError(t, err)
	assert.Equal(t, newer, gitOut(t, bare, "-C", bare, "rev-parse", "refs/heads/"+prBranch), "a checked-out branch is never moved")

	// Held by A-1's worktree: another issue gets BranchInUseError and the
	// branch does not move, even though origin is ahead.
	gitOut(t, ws.Path, "-c", "user.email=a@b", "-c", "user.name=agent", "commit", "-q", "--allow-empty", "-m", "unpushed")
	held := gitOut(t, ws.Path, "rev-parse", "HEAD")
	pushUpstreamCommit(t, upstream, prBranch, "pushed elsewhere")
	_, err = m.EnsureWorkspace(ctx, "Z-1", prBranch)
	var biu *workspace.BranchInUseError
	require.ErrorAs(t, err, &biu)
	assert.Equal(t, held, gitOut(t, ws.Path, "rev-parse", "HEAD"))

	// Local ahead of (diverged from) origin, not checked out: kept as is.
	gitOut(t, bare, "-C", bare, "worktree", "remove", "--force", ws.Path)
	ws, err = m.EnsureWorkspace(ctx, "A-1", prBranch)
	require.NoError(t, err)
	assert.Equal(t, held, gitOut(t, ws.Path, "rev-parse", "HEAD"), "unpushed commits are never rewound")
}

// TestFailedBareFetchIsRecordedOnce (#195): a failing bare fetch is handed to
// the caller for recording once per distinct failure (not on every
// dispatch), again after a success, and lands in the status file doctor
// reads. Worktrees are still created from the last fetched base.
func TestFailedBareFetchIsRecordedOnce(t *testing.T) {
	upstream := upstreamWithPRBranch(t)
	m, root := prBareManager(t, upstream)
	ctx := context.Background()
	bare := workspace.BarePath(root)

	ws, err := m.EnsureWorkspace(ctx, "A-1", "itervox/a-1")
	require.NoError(t, err)
	assert.Empty(t, ws.BareFetchFailure, "a successful fetch reports nothing")

	moved := upstream + ".moved"
	require.NoError(t, os.Rename(upstream, moved))
	ws, err = m.EnsureWorkspace(ctx, "B-1", "itervox/b-1")
	require.NoError(t, err, "a failed fetch is best-effort: the worktree is still created")
	assert.Contains(t, ws.BareFetchFailure, "shared bare clone fetch failed")
	status, err := workspace.ReadBareFetchStatus(bare)
	require.NoError(t, err)
	assert.True(t, status.Failing(), "doctor sees the failing fetch")
	assert.NotEmpty(t, status.LastError)

	ws, err = m.EnsureWorkspace(ctx, "C-1", "itervox/c-1")
	require.NoError(t, err)
	assert.Empty(t, ws.BareFetchFailure, "the same failure is reported once")

	require.NoError(t, os.Rename(moved, upstream))
	ws, err = m.EnsureWorkspace(ctx, "D-1", "itervox/d-1")
	require.NoError(t, err)
	assert.Empty(t, ws.BareFetchFailure)
	status, err = workspace.ReadBareFetchStatus(bare)
	require.NoError(t, err)
	assert.False(t, status.Failing(), "a success clears the failing state")

	require.NoError(t, os.Rename(upstream, moved))
	ws, err = m.EnsureWorkspace(ctx, "E-1", "itervox/e-1")
	require.NoError(t, err)
	assert.NotEmpty(t, ws.BareFetchFailure, "after a success the failure is reported again")
}
