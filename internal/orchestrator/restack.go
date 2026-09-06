package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

// Restacker is the optional workspace capability needed to replay a stacked
// worktree onto an updated base. Asserted at the call site so a provider that
// cannot rebase simply never restacks instead of failing dispatch.
type Restacker interface {
	RestackWorktree(ctx context.Context, identifier, branchName, onto string) (workspace.RestackOutcome, error)
}

// restackEligible reports whether issue's worktree may be replayed onto the
// base branch right now.
//
// Pure, so the rules — which are all safety rules — are testable without git or
// a live orchestrator.
//
//   - **Not running.** This is the constraint that makes the feature safe at
//     all: rebasing a worktree an agent has checked out moves HEAD under a live
//     process mid-turn, corrupting the run and potentially destroying the
//     agent's uncommitted work. A running dependent is skipped and picked up on
//     a later cycle, which costs one cycle of drift and risks nothing.
//
//   - **No remaining live blocker.** Stacking only ever happens with exactly
//     one live blocker (see stackedBaseBranch), so an issue that still has one
//     is either not stacked on the branch that just merged, or is not ready to
//     move yet. Either way, leave it.
//
//   - **Has an identifier.** The worktree is keyed by it; without one there is
//     nothing to resolve.
func restackEligible(state State, issue domain.Issue) bool {
	if issue.Identifier == "" {
		return false
	}
	if _, running := state.Running[issue.Identifier]; running {
		slog.Debug("orchestrator: skipping restack for a running issue",
			"identifier", issue.Identifier)
		return false
	}
	for _, b := range issue.BlockedBy {
		if b.State == nil || !isTerminalState(*b.State, state) {
			return false
		}
	}
	return true
}

// restackUnblockedIssue replays this issue's worktree onto the base branch now
// that its blockers have landed.
//
// Called from the dependency-audit transition rather than from an automation,
// so restacking happens whether or not the operator configured a
// `blockers_resolved` rule — the drift is a property of the merge, not of the
// automation.
//
// Returns true when the restack CONFLICTED, so the caller can escalate to a
// human. A rebase conflict is a semantic disagreement between two changes;
// resolving it automatically yields a plausible-shaped guess, which is the
// worst available outcome. Issue #60 rules this explicitly: on conflict, ask.
//
// Errors are logged and swallowed. Restacking is an optimisation over the
// documented manual rebase, so failing the surrounding tick because one
// worktree could not be replayed would trade a cosmetic problem for an
// operational one.
func (o *Orchestrator) restackUnblockedIssue(
	ctx context.Context,
	state *State,
	issue domain.Issue,
) bool {
	if !o.stackedPRsEnabled() {
		return false
	}
	restacker, ok := o.workspace.(Restacker)
	if !ok {
		return false
	}
	base := o.baseBranchForRestack()
	if base == "" || !restackEligible(*state, issue) {
		return false
	}

	branch := workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
	outcome, err := restacker.RestackWorktree(ctx, issue.Identifier, branch, base)
	if err != nil {
		slog.Warn("orchestrator: restack failed",
			"identifier", issue.Identifier, "onto", base, "error", err)
		return false
	}
	switch outcome {
	case workspace.RestackConflict:
		slog.Info("orchestrator: restack conflicted — escalating for a human",
			"identifier", issue.Identifier, "onto", base)
		return true
	case workspace.RestackRebased:
		slog.Info("orchestrator: restacked dependent onto merged base",
			"identifier", issue.Identifier, "onto", base)
	case workspace.RestackSkippedDirty:
		slog.Info("orchestrator: skipped restack, worktree has uncommitted changes",
			"identifier", issue.Identifier)
	case workspace.RestackUpToDate:
		// Nothing to say: the common case once a stack has settled.
	}
	return false
}

// baseBranchForRestack resolves the branch dependents are replayed onto.
// Workspace config has no runtime setter, so no lock is taken — the same
// reasoning as stackedPRsEnabled.
func (o *Orchestrator) baseBranchForRestack() string {
	if o == nil || o.cfg == nil {
		return ""
	}
	return o.cfg.Workspace.BaseBranch
}

// markRestackConflict parks an issue for a human after a failed restack.
//
// The branch is untouched (RestackWorktree aborts the rebase), so this is not
// damage control — it is the escalation issue #60 asks for: "on conflict, move
// the issue to input_required rather than guessing. A rebase conflict needs an
// owner, and silently resolving it is worse than asking."
//
// Reuses the existing input-required queue so the conflict shows up on exactly
// the surfaces an operator already watches — the dashboard's input-required
// list, the heartbeat count, and the tracker-reply path — instead of inventing
// a parallel notification nobody is looking at.
func (o *Orchestrator) markRestackConflict(state *State, issue domain.Issue, now time.Time) {
	if state.InputRequiredIssues == nil {
		state.InputRequiredIssues = map[string]*InputRequiredEntry{}
	}
	if _, exists := state.InputRequiredIssues[issue.Identifier]; exists {
		return // already waiting on a human; do not overwrite their context
	}
	state.InputRequiredIssues[issue.Identifier] = &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context: "Restacking this branch onto " + o.baseBranchForRestack() +
			" hit a merge conflict after its blocker landed. The rebase was aborted, so the branch is unchanged. " +
			"Resolve the conflict manually, then resume.",
		BranchName: workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier),
		QueuedAt:   now,
	}
	slog.Warn("orchestrator: issue parked for a human after a restack conflict",
		"identifier", issue.Identifier)
}
