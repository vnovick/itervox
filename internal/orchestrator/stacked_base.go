package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// reviewStackKey identifies the in-review blocker an issue may stack on
// (#73 follow-up), or "" when it may not: dependencies.stacked_prs is on,
// the issue has exactly one unresolved blocker, that blocker is in
// tracker.completion_state (its work is done and in review, not merged) and
// it has an identifier, which names the branch to stack on. The key carries
// the blocker's state so a recorded miss (State.StackUnavailable) expires
// when the blocker moves.
func reviewStackKey(issue domain.Issue, state State) string {
	if state.StackOnReviewState == "" {
		return ""
	}
	unresolved := unresolvedBlockers(issue, state)
	if len(unresolved) != 1 {
		return ""
	}
	b := unresolved[0]
	if b.State == nil || !strings.EqualFold(strings.TrimSpace(*b.State), state.StackOnReviewState) {
		return ""
	}
	if b.Identifier == nil || *b.Identifier == "" {
		return ""
	}
	return *b.Identifier + "@" + strings.ToLower(strings.TrimSpace(*b.State))
}

// reviewStackAdmits reports whether the dispatch gate lets issue through
// despite its blocker: the blocker is in review, so the work can start
// stacked on the blocker's branch, unless stacking on this same blocker
// already failed.
func reviewStackAdmits(issue domain.Issue, state State) bool {
	key := reviewStackKey(issue, state)
	return key != "" && state.StackUnavailable[issue.Identifier] != key
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

// prBaseBranch is the branch a pull request opened by this run should target
// (#73), exposed to prompts as `run.pr_base_branch`: the blocker's branch
// when the worktree is stacked on it, otherwise workspace.base_branch.
// Workspace config has no runtime setter, so no lock is taken.
func (o *Orchestrator) prBaseBranch(stackedOn string) string {
	if stackedOn != "" {
		return stackedOn
	}
	if o == nil || o.cfg == nil {
		return ""
	}
	return o.cfg.Workspace.BaseBranch
}

// buildStackedPRBlock tells the agent that its worktree is stacked and which
// base its pull request must use (#73). Empty when the run is not stacked, so
// unstacked prompts are unchanged.
func buildStackedPRBlock(stackedOn string) string {
	if stackedOn == "" {
		return ""
	}
	return strings.Join([]string{
		"## Stacked Branch",
		"",
		fmt.Sprintf("- run.pr_base_branch: `%s`", stackedOn),
		"",
		fmt.Sprintf("This branch is stacked on its blocker's branch `%s`. Open the pull request against it", stackedOn),
		fmt.Sprintf("(`gh pr create --base %s`), not the default branch, so it shows only this issue's changes.", stackedOn),
		"Itervox also sets this base on the pull request after the run.",
	}, "\n")
}

func (o *Orchestrator) prURLFinder() func(ctx context.Context, wsPath string) string {
	if o.findOpenPRURL != nil {
		return o.findOpenPRURL
	}
	return workspace.FindOpenPRURL
}

func (o *Orchestrator) prBaseSetter() func(ctx context.Context, prURL, base string) (bool, error) {
	if o.setPRBase != nil {
		return o.setPRBase
	}
	return workspace.SetPRBase
}

// retargetPRBase points prURL at base and logs the outcome. Best-effort:
// GitHub refuses a base branch that is not on the remote, and a failure here
// only leaves the PR's diff wider than it needs to be, so it is logged, not
// returned.
func (o *Orchestrator) retargetPRBase(ctx context.Context, identifier, prURL, base, why string) {
	changed, err := o.prBaseSetter()(ctx, prURL, base)
	switch {
	case err != nil:
		slog.Warn("orchestrator: could not set pull request base (non-fatal)",
			"identifier", identifier, "pr_url", prURL, "base", base, "reason", why, "error", err)
	case changed:
		slog.Info("orchestrator: pull request base set",
			"identifier", identifier, "pr_url", prURL, "base", base, "reason", why)
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLine("INFO",
				fmt.Sprintf("worker: pr_base_set url=%s base=%s", prURL, base)))
		}
	}
}

// addPRFooter appends the "Shipped with Itervox" footer to prURL once
// (agent.pr_footer, #81). Best-effort: a gh failure is logged.
func (o *Orchestrator) addPRFooter(ctx context.Context, identifier, prURL string) {
	ensure := o.ensurePRFooter
	if ensure == nil {
		ensure = workspace.EnsurePRFooter
	}
	added, err := ensure(ctx, prURL)
	switch {
	case err != nil:
		slog.Warn("orchestrator: could not add the pull request footer (non-fatal)",
			"identifier", identifier, "pr_url", prURL, "error", err)
	case added:
		slog.Info("orchestrator: pull request footer added", "identifier", identifier, "pr_url", prURL)
	}
}
