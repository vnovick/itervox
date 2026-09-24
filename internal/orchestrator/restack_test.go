package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
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
	for _, id := range running {
		st.Running[id] = &RunEntry{}
	}
	return st
}

func blocked(identifier string, blockers ...domain.BlockerRef) domain.Issue {
	return domain.Issue{Identifier: identifier, BlockedBy: blockers}
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
