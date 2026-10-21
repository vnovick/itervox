package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

const sharedBranch = "user/a-1-some-branch"

// repoWithA1Worktree is the legacy worktree layout (root is the repo) with
// A-1's worktree at root/worktrees/A-1 holding sharedBranch.
func repoWithA1Worktree(t *testing.T) (root, holder string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "-q", "--allow-empty", "-m", "base")
	git("branch", sharedBranch)
	holder = filepath.Join(root, "worktrees", "A-1")
	git("worktree", "add", "-q", holder, sharedBranch)
	return root, holder
}

// runB1OnSharedBranch dispatches B-1, whose worktree branch is sharedBranch,
// against a real workspace.Manager rooted at root.
func runB1OnSharedBranch(t *testing.T, root string) (*orchestrator.Orchestrator, *promptCaptureRunner, context.CancelFunc) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.BaseBranch = "main"
	b1 := makeIssue("idb1", "B-1", "Todo", nil, nil)
	branch := sharedBranch
	b1.BranchName = &branch
	mt := tracker.NewMemoryTracker([]domain.Issue{b1}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, workspace.NewManager(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	return orch, runner, cancel
}

// TestBranchInUseByAnotherIssueHoldsWithoutRetry (#193): B-1's branch is
// checked out in A-1's worktree. B-1 is held with branch_in_use — visible as
// its ineligible reason in the snapshot — no retry is scheduled, no agent
// runs and no worktree is made for it, tick after tick.
func TestBranchInUseByAnotherIssueHoldsWithoutRetry(t *testing.T) {
	root, holder := repoWithA1Worktree(t)
	orch, runner, _ := runB1OnSharedBranch(t, root)

	require.Eventually(t, func() bool {
		_, held := orch.Snapshot().BranchInUseHolds["B-1"]
		return held
	}, 6*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // many more ticks

	snap := orch.Snapshot()
	hold := snap.BranchInUseHolds["B-1"]
	assert.Equal(t, sharedBranch, hold.Branch)
	assert.Equal(t, "A-1", hold.Holder)
	assert.True(t, workspace.BranchCheckedOutAt(hold.HolderPath, sharedBranch))
	assert.Equal(t, filepath.Base(holder), filepath.Base(hold.HolderPath))
	_, retry := snap.RetryAttempts["idb1"]
	assert.False(t, retry, "a branch_in_use failure must not schedule a retry")
	_, claimed := snap.Claimed["idb1"]
	assert.False(t, claimed)
	assert.Equal(t, 0, runnerCalls(runner), "no agent runs while the branch is taken")
	assert.NoDirExists(t, filepath.Join(root, "worktrees", "B-1"))

	b1 := makeIssue("idb1", "B-1", "Todo", nil, nil)
	assert.Equal(t, orchestrator.IneligibleBranchInUse, orchestrator.IneligibleReason(b1, snap, baseConfig()))
}

// TestBranchInUseHoldReleasesWhenBranchFreed (#193): once A-1's worktree is
// removed the hold is released on the next tick and B-1 runs on the branch.
func TestBranchInUseHoldReleasesWhenBranchFreed(t *testing.T) {
	root, holder := repoWithA1Worktree(t)
	orch, runner, _ := runB1OnSharedBranch(t, root)
	require.Eventually(t, func() bool {
		_, held := orch.Snapshot().BranchInUseHolds["B-1"]
		return held
	}, 6*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, runnerCalls(runner))

	out, err := gitexec.Command(context.Background(), root, "worktree", "remove", "--force", holder).CombinedOutput()
	require.NoError(t, err, string(out))

	require.Eventually(t, func() bool { return runnerCalls(runner) > 0 }, 6*time.Second, 20*time.Millisecond,
		"B-1 is dispatched once A-1's worktree frees the branch")
	assert.True(t, workspace.BranchCheckedOutAt(filepath.Join(root, "worktrees", "B-1"), sharedBranch))
	_, held := orch.Snapshot().BranchInUseHolds["B-1"]
	assert.False(t, held)
}

// branchInUseProvider refuses every worktree with git's branch-in-use
// error, wrapped the way ensureWorktree wraps it, and counts the attempts.
// holderPath is a checkout that really has sharedBranch, so the hold is not
// released by the per-tick check.
type branchInUseProvider struct {
	calls      atomic.Int64
	holderPath string
}

func (p *branchInUseProvider) EnsureWorkspace(context.Context, string, string) (workspace.Workspace, error) {
	p.calls.Add(1)
	return workspace.Workspace{}, fmt.Errorf("worktree: add (existing branch): %w",
		&workspace.BranchInUseError{Branch: sharedBranch, Path: p.holderPath, Holder: "A-1"})
}
func (p *branchInUseProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *branchInUseProvider) ResolvePath(string) string                             { return os.TempDir() }

// TestBranchInUseErrorIsNotRetried (#193): the classified error does not
// consume the retry/backoff budget — no RetryAttempts entry, one setup
// attempt — while an ordinary setup failure still schedules a retry.
func TestBranchInUseErrorIsNotRetried(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 5
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("idb1", "B-1", "Todo", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	_, holder := repoWithA1Worktree(t)
	ws := &branchInUseProvider{holderPath: holder}
	orch := orchestrator.New(cfg, mt, &promptCaptureRunner{}, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		_, held := orch.Snapshot().BranchInUseHolds["B-1"]
		return held
	}, 6*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	snap := orch.Snapshot()
	_, retry := snap.RetryAttempts["idb1"]
	assert.False(t, retry, "no retry is scheduled for branch_in_use")
	assert.Equal(t, int64(1), ws.calls.Load(), "the held issue is not set up again every tick")
	assert.Equal(t, orchestrator.IneligibleBranchInUse,
		orchestrator.IneligibleReason(makeIssue("idb1", "B-1", "Todo", nil, nil), snap, cfg))
}
