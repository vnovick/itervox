package orchestrator

import (
	"cmp"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

// Run attribution for Repo Analysis hotspots (#201): which branch a run
// worked on, the PR it created or continued, and a reviewer's verdict end up
// on its CompletedRun, so the hotspots job can attribute the run to the
// files that branch changed. All of it is written by the event loop
// (EventWorkerExited → recordHistory); the hotspots job only reads history.

// noteRunExit copies the branch and PR a run's exit reports onto its live
// entry, which recordHistory reads. Event loop only.
func noteRunExit(live, exit *RunEntry) {
	if live == nil || exit == nil {
		return
	}
	live.BranchName = cmp.Or(exit.BranchName, live.BranchName)
	live.PRURL = cmp.Or(exit.PRURL, live.PRURL)
}

// noteReviewVerdict records on a finished reviewer's live entry the verdict
// advanceReviewChainForIssue just ingested for its profile. Event loop only.
func noteReviewVerdict(state State, live *RunEntry, identifier, profile string) {
	if live == nil {
		return
	}
	for _, v := range state.ReviewVerdicts[identifier] {
		if v.Profile == profile {
			live.reviewVerdict = v.Verdict
		}
	}
}

// historyBranch is the branch a completed run is attributed to: the one its
// exit reported, else the worktree branch the issue resolves to (the branch
// a failed or stalled run, whose exit carries none, was working on).
func historyBranch(live *RunEntry, issue domain.Issue) string {
	if live != nil && live.BranchName != "" {
		return live.BranchName
	}
	if issue.Identifier == "" {
		return ""
	}
	return workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
}
