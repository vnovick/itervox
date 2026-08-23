package orchestrator

import (
	"context"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

// stackedBaseBranch returns the branch a new worktree for issue should be
// created from when stacked PRs are enabled, or "" to use the configured
// workspace.base_branch as before.
//
// The policy, and why it is this narrow:
//
//   - Stacking applies only to an issue with EXACTLY ONE non-terminal blocker,
//     counted regardless of whether each carries an identifier.
//     With several, any choice of base is arbitrary — the work would sit on
//     one blocker's branch while still depending on others, so the PR would
//     not be reviewable in isolation and a merge would drag in unrelated
//     commits. Declining to stack is the honest outcome.
//   - A TERMINAL blocker is skipped: its work has landed, so base_branch
//     already contains it and stacking on it would only add distance.
//   - The blocker must have an identifier. The branch is derived from it via
//     the same ResolveWorktreeBranch the blocker's own worktree used, so the
//     two agree without needing to fetch the blocker.
//
// This function is pure and decides intent only. Whether the branch actually
// EXISTS is a git question, answered by the workspace manager, which falls
// back to base_branch when the ref is absent — that check belongs where git
// lives, not in dispatch policy.
func stackedBaseBranch(state State, issue domain.Issue) string {
	var candidate string
	var live int
	for _, blocker := range issue.BlockedBy {
		if blocker.State != nil && isTerminalState(*blocker.State, state) {
			continue // already landed; base_branch has it
		}
		// Counted BEFORE the identifier check. A live blocker the tracker
		// reported without an identifier is still a dependency — it just
		// cannot name a branch. Skipping it here would let a genuinely
		// ambiguous issue look like it had exactly one blocker and stack on
		// the wrong base. Linear leaves Identifier nil whenever the relation
		// node omits it, so this is reachable, not theoretical.
		live++
		if live > 1 {
			return "" // more than one live blocker — no unambiguous base
		}
		if blocker.Identifier == nil || *blocker.Identifier == "" {
			return "" // the sole live blocker cannot name a branch
		}
		candidate = *blocker.Identifier
	}
	if candidate == "" {
		return ""
	}
	return workspace.ResolveWorktreeBranch(nil, candidate)
}

// ensureWorkspaceMaybeStacked creates the issue's workspace, basing it on a
// blocker's branch when stacked PRs are enabled and exactly one live blocker
// makes that unambiguous.
//
// Every failure mode degrades to the existing behaviour rather than failing
// the dispatch: stacking disabled, provider without StackedProvider, no
// single live blocker, or a blocker branch that does not exist locally all
// end up calling plain EnsureWorkspace semantics.
func (o *Orchestrator) ensureWorkspaceMaybeStacked(
	ctx context.Context,
	issue domain.Issue,
	branchName string,
) (workspace.Workspace, error) {
	if !o.stackedPRsEnabled() {
		return o.workspace.EnsureWorkspace(ctx, issue.Identifier, branchName)
	}
	stacked, ok := o.workspace.(workspace.StackedProvider)
	if !ok {
		return o.workspace.EnsureWorkspace(ctx, issue.Identifier, branchName)
	}
	base := stackedBaseBranch(o.Snapshot(), issue)
	if base == "" {
		return o.workspace.EnsureWorkspace(ctx, issue.Identifier, branchName)
	}
	return stacked.EnsureWorkspaceFrom(ctx, issue.Identifier, branchName, base)
}

// stackedPRsEnabled reads the opt-in flag. Dependencies config has no runtime
// setter (it is absent from CLAUDE.md's cfgMu allowlist), so no lock is taken.
func (o *Orchestrator) stackedPRsEnabled() bool {
	return o != nil && o.cfg != nil && o.cfg.Dependencies.StackedPRs
}
