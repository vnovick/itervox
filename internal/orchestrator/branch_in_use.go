package orchestrator

import (
	"errors"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

// IneligibleBranchInUse is the ineligible reason for an issue held because
// the branch its worktree needs is checked out in another worktree (#193).
const IneligibleBranchInUse = "branch_in_use"

// BranchInUseHold is one State.BranchInUseHolds entry: the branch the
// issue's worktree needs and the worktree that has it checked out.
type BranchInUseHold struct {
	Branch string
	// HolderPath is the worktree holding Branch, as git reported it.
	HolderPath string
	// Holder is the identifier of the itervox issue whose worktree that is,
	// or "" when it is not an itervox worktree.
	Holder string
	Since  time.Time
}

// branchInUseHoldFrom extracts the hold from a worker's workspace error, or
// reports false when err is not a branch-in-use refusal.
func branchInUseHoldFrom(err error, now time.Time) (BranchInUseHold, bool) {
	var biu *workspace.BranchInUseError
	if !errors.As(err, &biu) {
		return BranchInUseHold{}, false
	}
	return BranchInUseHold{Branch: biu.Branch, HolderPath: biu.Path, Holder: biu.Holder, Since: now}, true
}

// setBranchInUseHold records the hold. Event loop only.
func setBranchInUseHold(state *State, identifier string, hold BranchInUseHold) {
	if state.BranchInUseHolds == nil {
		state.BranchInUseHolds = make(map[string]BranchInUseHold)
	}
	state.BranchInUseHolds[identifier] = hold
}

// releaseBranchInUseHolds drops the holds that no longer apply, once per
// tick before dispatch (event loop only): the holding worktree no longer has
// the branch checked out (it was removed, cleared, or switched branches), or
// the issue is no longer a candidate. A released issue is dispatched again
// and, should the branch be taken once more, held again on the worker's
// first git check, without spending a retry.
//
// workspace.BranchCheckedOutAt reads two small files and runs no git, so the
// check is cheap enough for the loop.
func releaseBranchInUseHolds(state *State, candidates []domain.Issue) {
	if len(state.BranchInUseHolds) == 0 {
		return
	}
	present := make(map[string]struct{}, len(candidates))
	for _, issue := range candidates {
		present[issue.Identifier] = struct{}{}
	}
	for ident, hold := range state.BranchInUseHolds {
		if _, ok := present[ident]; !ok {
			delete(state.BranchInUseHolds, ident)
			continue
		}
		if !workspace.BranchCheckedOutAt(hold.HolderPath, hold.Branch) {
			delete(state.BranchInUseHolds, ident)
			slog.Info("orchestrator: branch freed by the other worktree; releasing branch_in_use hold",
				"identifier", ident, "branch", hold.Branch, "holder", hold.Holder)
		}
	}
}
