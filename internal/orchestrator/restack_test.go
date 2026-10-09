package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

func newRestackCfg(base string) *config.Config {
	cfg := &config.Config{}
	cfg.Workspace.BaseBranch = base
	cfg.Dependencies.StackedPRs = true
	return cfg
}

func strp(s string) *string { return &s }

func restackState(running ...string) State {
	st := State{
		Running:        map[string]*RunEntry{},
		TerminalStates: []string{"Done"},
	}
	// Keyed by issue ID, as the event loop keys it (dispatch stores
	// state.Running[issue.ID]).
	for _, identifier := range running {
		st.Running["id-"+identifier] = &RunEntry{Issue: domain.Issue{ID: "id-" + identifier, Identifier: identifier}}
	}
	return st
}

func blocked(identifier string, blockers ...domain.BlockerRef) domain.Issue {
	return domain.Issue{ID: "id-" + identifier, Identifier: identifier, BlockedBy: blockers}
}

func restackBlocker(id, state string) domain.BlockerRef {
	return domain.BlockerRef{Identifier: strp(id), State: strp(state)}
}

// TestRestackEligibleWhenBlockersLandedAndIdle is issue #60's selection core.
func TestRestackEligibleWhenBlockersLandedAndIdle(t *testing.T) {
	assert.True(t, restackEligible(restackState(),
		blocked("ENG-2", restackBlocker("ENG-1", "Done"))))
}

// TestRestackNotEligibleWhileRunning is THE safety property. Rebasing a
// worktree an agent has checked out moves HEAD under a live process mid-turn,
// corrupting the run and potentially destroying uncommitted work.
func TestRestackNotEligibleWhileRunning(t *testing.T) {
	assert.False(t, restackEligible(restackState("ENG-2"),
		blocked("ENG-2", restackBlocker("ENG-1", "Done"))),
		"a running dependent must never be rebased — it is picked up on a later cycle instead")
}

// TestRestackNotEligibleWithALiveBlocker pins consistency with
// stackedBaseBranch: stacking only happens with exactly one live blocker, so an
// issue that still has one is not ready to move.
func TestRestackNotEligibleWithALiveBlocker(t *testing.T) {
	assert.False(t, restackEligible(restackState(),
		blocked("ENG-2",
			restackBlocker("ENG-1", "Done"),
			restackBlocker("ENG-3", "Todo"),
		)))
}

// TestRestackEligibleWithSeveralLandedBlockers is the complement: a second
// blocker that has ALSO landed does not disqualify the dependent.
func TestRestackEligibleWithSeveralLandedBlockers(t *testing.T) {
	assert.True(t, restackEligible(restackState(),
		blocked("ENG-2",
			restackBlocker("ENG-1", "Done"),
			restackBlocker("ENG-3", "Done"),
		)))
}

// TestRestackNotEligibleWithUnknownBlockerState pins fail-safe handling: a
// blocker whose state the tracker did not report is NOT assumed landed.
func TestRestackNotEligibleWithUnknownBlockerState(t *testing.T) {
	assert.False(t, restackEligible(restackState(),
		blocked("ENG-2", domain.BlockerRef{Identifier: strp("ENG-1")})),
		"an unknown blocker state must not be read as terminal")
}

// TestRestackNotEligibleWithoutIdentifier pins that a worktree keyed by
// identifier cannot be resolved without one.
func TestRestackNotEligibleWithoutIdentifier(t *testing.T) {
	assert.False(t, restackEligible(restackState(), domain.Issue{}))
}

// TestMarkRestackConflictParksTheIssueForAHuman pins issue #60's ruling: on
// conflict, ask. It reuses the input-required queue so the conflict appears on
// surfaces an operator already watches.
func TestMarkRestackConflictParksTheIssueForAHuman(t *testing.T) {
	o := &Orchestrator{cfg: newRestackCfg("main")}
	st := restackState()
	iss := domain.Issue{ID: "id-2", Identifier: "ENG-2"}

	o.markRestackConflict(&st, iss, time.Now())

	entry := st.InputRequiredIssues["ENG-2"]
	require.NotNil(t, entry, "a conflicted restack must be visible to an operator")
	assert.Equal(t, "id-2", entry.IssueID)
	assert.Contains(t, entry.Context, "conflict")
	assert.Contains(t, entry.Context, "branch is unchanged",
		"the operator must be told the rebase was aborted, not left half-applied")
}

// TestMarkRestackConflictDoesNotClobberExistingContext pins that an issue
// already waiting on a human keeps the context it was parked with.
func TestMarkRestackConflictDoesNotClobberExistingContext(t *testing.T) {
	o := &Orchestrator{cfg: newRestackCfg("main")}
	st := restackState()
	st.InputRequiredIssues = map[string]*InputRequiredEntry{
		"ENG-2": {Identifier: "ENG-2", Context: "original question"},
	}

	o.markRestackConflict(&st, domain.Issue{Identifier: "ENG-2"}, time.Now())

	assert.Equal(t, "original question", st.InputRequiredIssues["ENG-2"].Context)
}

// restackFakeProvider is a workspace provider whose RestackWorktree returns a
// fixed outcome.
type restackFakeProvider struct {
	outcome workspace.RestackOutcome
}

func (p *restackFakeProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	return workspace.Workspace{Path: "/tmp/ws", Identifier: identifier}, nil
}
func (p *restackFakeProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *restackFakeProvider) ResolvePath(string) string                             { return "/tmp/ws-ENG-2" }
func (p *restackFakeProvider) RestackWorktree(context.Context, string, string, string) (workspace.RestackOutcome, error) {
	return p.outcome, nil
}

// TestRestackRetargetsPullRequestToBaseBranch (#73): once a dependent has been
// restacked onto workspace.base_branch (or already sits there), its open pull
// request is pointed at base_branch too. A conflicted or skipped restack
// leaves the PR alone.
func TestRestackRetargetsPullRequestToBaseBranch(t *testing.T) {
	issue := blocked("ENG-2", restackBlocker("ENG-1", "Done"))
	for _, tc := range []struct {
		outcome    workspace.RestackOutcome
		wantEdited bool
	}{
		{workspace.RestackRebased, true},
		{workspace.RestackUpToDate, true},
		{workspace.RestackSkippedDirty, false},
		{workspace.RestackConflict, false},
	} {
		var mu sync.Mutex
		var lookups []string
		var edits [][2]string
		o := &Orchestrator{cfg: newRestackCfg("main"), workspace: &restackFakeProvider{outcome: tc.outcome}}
		o.findOpenPRURL = func(_ context.Context, wsPath string) string {
			mu.Lock()
			defer mu.Unlock()
			lookups = append(lookups, wsPath)
			return "https://github.com/o/r/pull/2"
		}
		o.setPRBase = func(_ context.Context, prURL, base string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			edits = append(edits, [2]string{prURL, base})
			return true, nil
		}
		state := restackState()
		conflicted := o.restackUnblockedIssue(context.Background(), &state, issue)
		o.prRetargetWg.Wait()
		assert.Equal(t, tc.outcome == workspace.RestackConflict, conflicted, "outcome %v", tc.outcome)
		if tc.wantEdited {
			assert.Equal(t, []string{"/tmp/ws-ENG-2"}, lookups, "outcome %v", tc.outcome)
			assert.Equal(t, [][2]string{{"https://github.com/o/r/pull/2", "main"}}, edits, "outcome %v", tc.outcome)
		} else {
			assert.Empty(t, edits, "outcome %v must not touch the PR", tc.outcome)
		}
	}
}

// TestRestackRetargetSkipsWhenNoPullRequest: no open PR for the worktree, no
// edit.
func TestRestackRetargetSkipsWhenNoPullRequest(t *testing.T) {
	o := &Orchestrator{cfg: newRestackCfg("main"), workspace: &restackFakeProvider{outcome: workspace.RestackRebased}}
	o.findOpenPRURL = func(context.Context, string) string { return "" }
	edited := false
	o.setPRBase = func(context.Context, string, string) (bool, error) { edited = true; return true, nil }
	state := restackState()
	o.restackUnblockedIssue(context.Background(), &state, blocked("ENG-2", restackBlocker("ENG-1", "Done")))
	o.prRetargetWg.Wait()
	assert.False(t, edited)
}

// TestBlockerLandingRetargetsDependentPullRequest drives #73's second case
// through the dependency-audit transition the event loop uses: the dependent
// is first audited while its blocker is live, then again once the blocker is
// Done. The unblock transition restacks the worktree and points its pull
// request at workspace.base_branch.
func TestBlockerLandingRetargetsDependentPullRequest(t *testing.T) {
	var mu sync.Mutex
	var edits [][2]string
	o := &Orchestrator{cfg: newRestackCfg("main"), workspace: &restackFakeProvider{outcome: workspace.RestackRebased}}
	o.findOpenPRURL = func(context.Context, string) string { return "https://github.com/o/r/pull/2" }
	o.setPRBase = func(_ context.Context, prURL, base string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		edits = append(edits, [2]string{prURL, base})
		return true, nil
	}
	state := restackState()
	now := time.Now()

	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), &state,
		blocked("ENG-2", restackBlocker("ENG-1", "In Progress")), now)
	o.prRetargetWg.Wait()
	assert.Empty(t, edits, "nothing happens while the blocker is live")

	o.auditFetchedIssueDependenciesAndDispatch(context.Background(), &state,
		blocked("ENG-2", restackBlocker("ENG-1", "Done")), now.Add(time.Minute))
	o.prRetargetWg.Wait()
	assert.Equal(t, [][2]string{{"https://github.com/o/r/pull/2", "main"}}, edits,
		"the blocker landing must retarget the dependent's PR to base_branch")
}
