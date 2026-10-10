package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// freshStackProvider creates a fresh workspace each time, reporting it as
// stacked on stackedOn ("" = the blocker's branch was not available).
type freshStackProvider struct {
	path, stackedOn string
	removes         atomic.Int64
}

func (p *freshStackProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: p.path, Identifier: identifier, CreatedNow: true, StackedOn: p.stackedOn}, nil
}
func (p *freshStackProvider) RemoveWorkspace(context.Context, string, string) error {
	p.removes.Add(1)
	return nil
}
func (p *freshStackProvider) ResolvePath(string) string { return p.path }

// reviewBlockedIssues is ENG-2, blocked only by ENG-1, which is in review.
func reviewBlockedIssues() []domain.Issue {
	blocker := makeIssue("id1", "ENG-1", "In Review", nil, nil)
	dependent := makeIssue("id2", "ENG-2", "Todo", nil, nil)
	ident, id, st := "ENG-1", "id1", "In Review"
	dependent.BlockedBy = []domain.BlockerRef{{ID: &id, Identifier: &ident, State: &st}}
	return []domain.Issue{blocker, dependent}
}

func runReviewStack(t *testing.T, stackedPRs bool, stackedOn string, wait func(*orchestrator.Orchestrator, *tracker.MemoryTracker, *promptCaptureRunner) bool) (*orchestrator.Orchestrator, *promptCaptureRunner, *freshStackProvider) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "In Review"
	cfg.Workspace.BaseBranch = "main"
	cfg.Dependencies.StackedPRs = stackedPRs
	mt := tracker.NewMemoryTracker(reviewBlockedIssues(), cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	ws := &freshStackProvider{path: t.TempDir(), stackedOn: stackedOn}
	orch := orchestrator.New(cfg, mt, runner, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool { return wait(orch, mt, runner) }, 6*time.Second, 20*time.Millisecond)
	return orch, runner, ws
}

func runnerCalls(r *promptCaptureRunner) int {
	n, _ := r.snapshot()
	return n
}

// TestDependentStartsStackedWhileBlockerInReview (#73 follow-up): with
// stacked_prs on, an issue whose only blocker is in completion_state is
// dispatched, stacked on the blocker's branch, instead of waiting for the
// blocker to merge.
func TestDependentStartsStackedWhileBlockerInReview(t *testing.T) {
	_, runner, _ := runReviewStack(t, true, "itervox/eng-1", func(_ *orchestrator.Orchestrator, mt *tracker.MemoryTracker, r *promptCaptureRunner) bool {
		issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id2"})
		return runnerCalls(r) > 0 && err == nil && len(issues) == 1 && issues[0].State == "In Review"
	})
	_, prompts := runner.snapshot()
	require.NotEmpty(t, prompts)
	assert.Contains(t, prompts[0], "## Stacked Branch", "the run is stacked on the in-review blocker")
}

// TestDependentWaitsWhenBlockerBranchUnavailable: if the worktree cannot be
// stacked on the in-review blocker's branch, no agent runs (it would build on
// base_branch without the blocker's code); the miss is recorded and the
// issue waits for the blocker as before, without retrying every tick.
func TestDependentWaitsWhenBlockerBranchUnavailable(t *testing.T) {
	orch, runner, ws := runReviewStack(t, true, "", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker, _ *promptCaptureRunner) bool {
		return o.Snapshot().StackUnavailable["ENG-2"] != ""
	})
	assert.Equal(t, "ENG-1@in review", orch.Snapshot().StackUnavailable["ENG-2"])
	time.Sleep(200 * time.Millisecond) // several more ticks
	assert.Equal(t, 0, runnerCalls(runner), "no agent may run on an unstacked worktree")
	assert.Equal(t, int64(1), ws.removes.Load(), "the fresh worktree is removed once, not re-created every tick")
	_, running := orch.Snapshot().Running["id2"]
	assert.False(t, running)
}

// TestDependentBlockedWithoutStackedPRs: without stacked_prs the in-review
// blocker still holds the dependent, as before.
func TestDependentBlockedWithoutStackedPRs(t *testing.T) {
	_, runner, _ := runReviewStack(t, false, "itervox/eng-1", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker, _ *promptCaptureRunner) bool {
		return o.Snapshot().DependencyAudit != nil && len(o.Snapshot().DependencyAudit) > 0
	})
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, runnerCalls(runner))
}

// TestReviewStackGate covers the dispatch gate directly.
func TestReviewStackGate(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.StackOnReviewState = "in review"
	issues := reviewBlockedIssues()
	dependent := issues[1]

	assert.Equal(t, "", orchestrator.IneligibleReason(dependent, state, cfg), "only blocker in review: admitted")

	state.StackUnavailable["ENG-2"] = "ENG-1@in review"
	assert.Contains(t, orchestrator.IneligibleReason(dependent, state, cfg), "blocked_by", "a recorded miss holds it")
	delete(state.StackUnavailable, "ENG-2")

	inProgress := "In Progress"
	d2 := dependent
	d2.BlockedBy = []domain.BlockerRef{dependent.BlockedBy[0]}
	d2.BlockedBy[0].State = &inProgress
	assert.Contains(t, orchestrator.IneligibleReason(d2, state, cfg), "blocked_by", "blocker not in review yet")

	otherID, otherIdent, otherState := "id3", "ENG-3", "In Review"
	d3 := dependent
	d3.BlockedBy = append([]domain.BlockerRef{}, dependent.BlockedBy...)
	d3.BlockedBy = append(d3.BlockedBy, domain.BlockerRef{ID: &otherID, Identifier: &otherIdent, State: &otherState})
	assert.Contains(t, orchestrator.IneligibleReason(d3, state, cfg), "blocked_by", "two blockers: no single base to stack on")

	d4 := dependent
	d4.BlockedBy = []domain.BlockerRef{{ID: &otherID, State: &otherState}}
	assert.Contains(t, orchestrator.IneligibleReason(d4, state, cfg), "blocked_by", "a blocker without an identifier names no branch")

	state.StackOnReviewState = ""
	assert.Contains(t, orchestrator.IneligibleReason(dependent, state, cfg), "blocked_by", "stacked_prs off")
}

// gitRepoForStacking makes a repo at root (the legacy worktree layout: root
// itself is the repo) with main and the given blocker branch carrying its
// own commit.
func gitRepoForStacking(t *testing.T, blockerBranch string) string {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "-q", "--allow-empty", "-m", "base")
	if blockerBranch != "" {
		git("checkout", "-q", "-b", blockerBranch)
		require.NoError(t, os.WriteFile(filepath.Join(root, "blocker.go"), []byte("package x\n"), 0o644))
		git("add", "blocker.go")
		git("commit", "-q", "-m", "blocker work")
		git("checkout", "-q", "main")
	}
	return root
}

// runReviewStackReal dispatches ENG-2 (blocked by ENG-1, in review, whose
// tracker branch is blockerBranchName) with a real workspace.Manager.
func runReviewStackReal(t *testing.T, root string, blockerBranchName *string, wait func(*orchestrator.Orchestrator, *promptCaptureRunner) bool) *promptCaptureRunner {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "In Review"
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.BaseBranch = "main"
	cfg.Dependencies.StackedPRs = true
	issues := reviewBlockedIssues()
	issues[1].BlockedBy[0].BranchName = blockerBranchName
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, workspace.NewManager(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool { return wait(orch, runner) }, 6*time.Second, 20*time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond)
	return runner
}

// TestDependentStacksOnTrackerBranchOfBlockerInReview (#103): with a real
// workspace.Manager, the dependent's worktree is created from the blocker's
// branch as the tracker names it (Linear's branchName), so the blocker's
// work is in the dependent's worktree.
func TestDependentStacksOnTrackerBranchOfBlockerInReview(t *testing.T) {
	branch := "alex/eng-1-add-parser"
	root := gitRepoForStacking(t, branch)
	runner := runReviewStackReal(t, root, &branch, func(_ *orchestrator.Orchestrator, r *promptCaptureRunner) bool {
		return runnerCalls(r) > 0
	})
	_, prompts := runner.snapshot()
	require.NotEmpty(t, prompts)
	assert.Contains(t, prompts[0], "## Stacked Branch")
	assert.Contains(t, prompts[0], branch)
	_, err := os.Stat(filepath.Join(root, "worktrees", "ENG-2", "blocker.go"))
	assert.NoError(t, err, "the blocker's work is in the dependent's worktree")
}

// TestBackOutKeepsExistingDependentBranch (#103): when the in-review
// blocker's branch is missing, the back-out removes the fresh worktree but
// never deletes a dependent branch that already carries someone's work.
func TestBackOutKeepsExistingDependentBranch(t *testing.T) {
	root := gitRepoForStacking(t, "") // no blocker branch: stacking fails
	git := func(args ...string) string {
		out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git("checkout", "-q", "-b", "itervox/eng-2")
	require.NoError(t, os.WriteFile(filepath.Join(root, "work.go"), []byte("package x\n"), 0o644))
	git("add", "work.go")
	git("commit", "-q", "-m", "precious work")
	work := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	runner := runReviewStackReal(t, root, nil, func(o *orchestrator.Orchestrator, _ *promptCaptureRunner) bool {
		return o.Snapshot().StackUnavailable["ENG-2"] != ""
	})
	assert.Equal(t, 0, runnerCalls(runner), "no agent runs on an unstacked worktree")
	assert.Equal(t, work, git("rev-parse", "itervox/eng-2"), "the existing branch and its work survive the back-out")
	_, err := os.Stat(filepath.Join(root, "worktrees", "ENG-2"))
	assert.True(t, os.IsNotExist(err), "the fresh worktree is removed")
}

// TestBackOutDeletesFreshDependentBranch: a branch the back-out itself just
// created (nothing of its own) is deleted with the worktree, so a later
// dispatch starts from a fresh base instead of a stale empty branch.
func TestBackOutDeletesFreshDependentBranch(t *testing.T) {
	root := gitRepoForStacking(t, "")
	runner := runReviewStackReal(t, root, nil, func(o *orchestrator.Orchestrator, _ *promptCaptureRunner) bool {
		return o.Snapshot().StackUnavailable["ENG-2"] != ""
	})
	assert.Equal(t, 0, runnerCalls(runner))
	err := gitexec.Command(context.Background(), root, "rev-parse", "--verify", "-q", "refs/heads/itervox/eng-2").Run()
	assert.Error(t, err, "the fresh, empty branch is deleted")
}

// TestBackOutHoldsReusedUnstackedWorktree (#103 review): a dependent whose
// worktree already exists from base_branch (made before the dependency was
// added) is admitted for its in-review blocker but not stacked on it. No
// agent runs, the issue waits for the blocker, and the existing worktree and
// its work are kept.
func TestBackOutHoldsReusedUnstackedWorktree(t *testing.T) {
	branch := "alex/eng-1-add-parser"
	root := gitRepoForStacking(t, branch)
	wt := filepath.Join(root, "worktrees", "ENG-2")
	git := func(dir string, args ...string) {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git(root, "worktree", "add", "-q", "-b", "itervox/eng-2", wt, "main")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.go"), []byte("package x\n"), 0o644))
	git(wt, "add", "work.go")
	git(wt, "commit", "-q", "-m", "earlier work")

	runner := runReviewStackReal(t, root, &branch, func(o *orchestrator.Orchestrator, _ *promptCaptureRunner) bool {
		return o.Snapshot().StackUnavailable["ENG-2"] != ""
	})
	assert.Equal(t, 0, runnerCalls(runner), "no agent runs without the blocker's code")
	assert.FileExists(t, filepath.Join(wt, "work.go"), "the reused worktree and its work are kept")
	assert.NoFileExists(t, filepath.Join(wt, "blocker.go"))
}

// blockerStateTracker reports the dependent's blocker in a state the test
// controls, the way a real tracker refreshes blocker states every poll.
type blockerStateTracker struct {
	*tracker.MemoryTracker
	state atomic.Value // string
}

func (b *blockerStateTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	issues, err := b.MemoryTracker.FetchCandidateIssues(ctx)
	st := b.state.Load().(string)
	for i := range issues {
		refs := append([]domain.BlockerRef(nil), issues[i].BlockedBy...) // never write the tracker's own slice
		for j := range refs {
			refs[j].State = &st
		}
		issues[i].BlockedBy = refs
	}
	return issues, err
}

// TestStackMissExpiresLive (#103): on the real tick path, a recorded miss is
// dropped when the blocker leaves review, so when it comes back with its
// branch now available the dependent is admitted and stacked.
func TestStackMissExpiresLive(t *testing.T) {
	root := gitRepoForStacking(t, "")
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "In Review"
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.BaseBranch = "main"
	cfg.Dependencies.StackedPRs = true
	bt := &blockerStateTracker{MemoryTracker: tracker.NewMemoryTracker(reviewBlockedIssues(), cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	bt.state.Store("In Review")
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, bt, runner, workspace.NewManager(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool { return orch.Snapshot().StackUnavailable["ENG-2"] != "" }, 5*time.Second, 20*time.Millisecond)
	bt.state.Store("In Progress") // the blocker goes back to work
	require.Eventually(t, func() bool { return orch.Snapshot().StackUnavailable["ENG-2"] == "" }, 5*time.Second, 20*time.Millisecond,
		"the miss expires on a live tick")
	assert.Equal(t, 0, runnerCalls(runner))

	git := func(args ...string) {
		out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("checkout", "-q", "-b", "itervox/eng-1")
	require.NoError(t, os.WriteFile(filepath.Join(root, "blocker.go"), []byte("package x\n"), 0o644))
	git("add", "blocker.go")
	git("commit", "-q", "-m", "blocker work")
	git("checkout", "-q", "main")
	bt.state.Store("In Review") // back in review, branch now present
	require.Eventually(t, func() bool { return runnerCalls(runner) > 0 }, 5*time.Second, 20*time.Millisecond)
	_, prompts := runner.snapshot()
	assert.Contains(t, prompts[0], "## Stacked Branch")
}
