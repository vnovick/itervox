package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/prdetector"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// Stack split (guardrails.lanes.split, push: daemon only). When one run's
// changes touch both human-lane files (guardrails.lanes.human: CI, deploy,
// pipeline config) and other files, the daemon push offers them as two
// stacked pull requests, so the code does not wait for a person unless it
// depends on the pipeline change:
//
//   - the run's changed files (against the merge base with the PR's target:
//     "the base", which is what the single PR and the run's signed checks
//     are based on) are classified with the pre-push hook's classifier;
//     human-lane files go to the pipeline side, lockfiles and Itervox's own
//     bookkeeping stay with the code, and no file is ever divided;
//   - the order is decided by testing: the profile's verify checks run on
//     base + code alone (a temporary worktree, after the workspace's
//     hooks.after_create, so it has the installed dependencies a worktree
//     of the run has). Pass: the code PR goes first, against the base, and
//     the pipeline PR is stacked on it. Fail: the checks run on base +
//     pipeline alone; pass: the pipeline PR goes first and the code is
//     stacked on it. Fail again: one combined PR, as without the split, and
//     the reason is recorded;
//   - the stack's top commit has exactly the run's tree, which its signed
//     checks already passed on, and the lower commit passed its own checks,
//     so no branch in the stack is a broken middle. Each push goes through
//     daemonPush (the seal's tree, the lanes, a human approval for the
//     human-lane push). Both commits keep the head commit's message, with a
//     trailer saying which half they are;
//   - the top lives on the issue branch (the worktree's branch is moved to
//     it, with no change to the files), the lower one on <branch>-code or
//     <branch>-pipeline. A lower branch that already exists on the remote
//     and is not exactly the commit to push is never pushed over: one
//     combined PR instead;
//   - a later run of the issue whose split's lower branch is already pushed
//     (its top was held or refused, or the daemon restarted between the two
//     pushes) keeps the split: it updates the lower branch only by a
//     fast-forward from the commit it pushed (StackSplitRecord.LowerSHA),
//     when that side changed and still passes its checks, and stacks the
//     top on it; it never splits from scratch.
//
// All of it runs on the worker goroutine and touches no State: the decision
// reaches the event loop as EventStackSplit (State.StackSplits, a feed
// event), the pushes' lanes as EventGuardrailLane as before.

// Stack split orders (StackSplitRecord.Order).
const (
	StackSplitCodeFirst     = "code_first"
	StackSplitPipelineFirst = "pipeline_first"
	StackSplitCombined      = "combined"
)

// Branch suffixes of a stack split's lower branch.
const (
	stackSplitCodeSuffix     = "-code"
	stackSplitPipelineSuffix = "-pipeline"
)

// isStackSplitLowerBranch reports whether branch is the lower branch a
// stack split of issueBranch pushes: never the issue's working branch.
func isStackSplitLowerBranch(branch, issueBranch string) bool {
	return issueBranch != "" && (branch == issueBranch+stackSplitCodeSuffix || branch == issueBranch+stackSplitPipelineSuffix)
}

// stackSplitRun is what a stack split needs beyond the daemon push.
type stackSplitRun struct {
	push         daemonPushRun
	checks       []config.VerifyCheck
	runTimestamp string
	profile      string
	// prBase is the branch the pull request targets ("" for the
	// repository's default).
	prBase string
	// stackedOn is the blocker branch a stacked worktree (#73) is based on.
	stackedOn string
}

// stackSplitOutcome is a stack split's result. handled false: no split,
// the caller pushes and opens one PR as before.
type stackSplitOutcome struct {
	handled bool
	// hold is the input-required context when one of the pushes was refused.
	hold string
	// topBranch and topPRURL are the issue branch's pull request (the top
	// of the stack); lowerPRURL the one below it.
	topBranch            string
	topPRURL, lowerPRURL string
	order                string
}

// stackSplitBuild is one stack split in progress. Worker goroutine.
type stackSplitBuild struct {
	o              *Orchestrator
	run            stackSplitRun
	ws, identifier string
	branch, sha    string
	base           string
	code, pipeline []string
	env            []string
	headMsg        string
	rec            StackSplitRecord
	hooked         bool
	// prev is the earlier split a resumed run keeps (nil: a new split);
	// keepLower: its lower branch stays as it is.
	prev      *StackSplitRecord
	keepLower bool
}

// combined records that the run is pushed as one pull request, and why.
func (b *stackSplitBuild) combined(ctx context.Context, reason string) stackSplitOutcome {
	b.rec.Order, b.rec.Reason = StackSplitCombined, reason
	b.rec.LowerBranch, b.rec.UpperBranch, b.rec.LowerSHA = "", "", ""
	b.rec.LowerPR, b.rec.LowerPRURL, b.rec.UpperPRURL = 0, "", ""
	b.o.logPatchOut(b.identifier, b.run.push.runLogID, "INFO", "worker: stack split: one combined pull request: "+reason)
	b.o.sendStackSplit(ctx, b.identifier, b.rec)
	return stackSplitOutcome{}
}

// commit builds the commit of base plus files from the head commit, on
// parent, with the head commit's message and note.
func (b *stackSplitBuild) commit(ctx context.Context, files []string, parent, note string) (string, error) {
	tree, err := splitTree(ctx, b.ws, b.base, b.sha, files)
	if err != nil {
		return "", err
	}
	return commitTree(ctx, b.ws, tree, parent, stackSplitMessage(b.headMsg, note), b.env)
}

// checkSplitCommit runs the profile's signed checks on commit in a
// temporary worktree, after the workspace's after_create hook, records
// them under role, and returns the verdict.
func (o *Orchestrator) checkSplitCommit(ctx context.Context, run stackSplitRun, commit, role string) (verifyVerdict, error) {
	ws := run.push.wsPath
	dir, err := os.MkdirTemp("", "itervox-split-")
	if err != nil {
		return verifyVerdict{}, fmt.Errorf("orchestrator: temp worktree: %w", err)
	}
	wt := filepath.Join(dir, "wt")
	defer removeTempWorktree(ws, wt, dir)
	// No repository hooks run for the daemon's own checkout.
	if out, err := gitexec.Command(ctx, ws, "-c", "core.hooksPath="+os.DevNull,
		"worktree", "add", "--detach", "--quiet", wt, commit).CombinedOutput(); err != nil {
		return verifyVerdict{}, fmt.Errorf("orchestrator: git worktree add: %w: %s", err, logging.RedactString(strings.TrimSpace(string(out))))
	}
	identifier, runLogID := run.push.issue.Identifier, run.push.runLogID
	// The worktree has the commit's files only: the workspace's ignored
	// ones (node_modules, vendor, build output) are installed by the same
	// hook a new workspace runs, bounded by hooks.timeout_ms.
	if hook := o.cfg.Hooks.AfterCreate; strings.TrimSpace(hook) != "" { // read-only after startup
		o.logPatchOut(identifier, runLogID, "INFO", "worker: stack split: running hooks.after_create in the temporary worktree of the "+role+" change")
		if err := workspace.RunHook(ctx, hook, wt, o.cfg.Hooks.TimeoutMs, o.hookLogFn(identifier, runLogID)); err != nil {
			return verifyVerdict{}, fmt.Errorf("hooks.after_create failed in the temporary worktree: %w", err)
		}
	}
	vr := verifyRun{issue: run.push.issue, runID: attestRunID(run.runTimestamp, run.profile) + "_split-" + role,
		profile: run.profile, runLogID: runLogID}
	o.logPatchOut(identifier, runLogID, "INFO", "worker: stack split: running the signed checks on the "+role+" change alone")
	return o.runVerifyChecks(ctx, wt, vr, run.checks), nil
}

// isSplitBookkeeping is a path that always stays with the code side:
// Itervox's handoffs and evidence.
func isSplitBookkeeping(p string) bool {
	for _, d := range verifyTreeExcludes {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// stackSplit splits a patches-only run's push into two stacked pull
// requests when guardrails.lanes.split applies; see the comment at the top
// of this file. Worker goroutine.
func (o *Orchestrator) stackSplit(ctx context.Context, run stackSplitRun) stackSplitOutcome {
	g := o.cfg.Guardrails // read-only after startup
	if !g.Enabled || !g.Lanes.Split || len(g.Lanes.Human) == 0 {
		return stackSplitOutcome{}
	}
	ws, identifier := run.push.wsPath, run.push.issue.Identifier
	branch := workspace.GetCurrentBranch(ctx, ws)
	sha := attest.Head(ctx, ws)
	if branch == "" || sha == "" || slices.Contains(protectedBranches(o.cfg), branch) {
		return stackSplitOutcome{} // daemonPush refuses it with its own reason
	}
	if workspace.RemoteBranchSHA(ctx, ws, branch) != "" {
		return stackSplitOutcome{} // a split applies to a branch's first push only
	}
	lanes := guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion}
	base, paths, err := guardrails.ChangedSinceBase(ws, sha, run.push.baseRefs)
	if err != nil {
		return stackSplitOutcome{} // daemonPush classifies it fail closed
	}
	code, pipeline := lanes.SplitPaths(paths, isSplitBookkeeping)
	if len(code) == 0 || len(pipeline) == 0 {
		return stackSplitOutcome{} // nothing to split: one PR as before
	}
	b := &stackSplitBuild{o: o, run: run, ws: ws, identifier: identifier, branch: branch, sha: sha, base: base,
		code: code, pipeline: pipeline, hooked: strings.TrimSpace(o.cfg.Hooks.AfterCreate) != ""}
	b.rec = StackSplitRecord{RunID: run.push.runLogID, Order: StackSplitCombined, CodeLane: lanes.Classify(code).Lane}
	b.rec.CodeFiles, b.rec.CodeFilesTotal = sanitizeLanePaths(code, len(code))
	b.rec.PipelineFiles, b.rec.PipelineFilesTotal = sanitizeLanePaths(pipeline, len(pipeline))
	switch {
	case run.stackedOn != "":
		return b.combined(ctx, "the issue is stacked on its blocker's branch "+run.stackedOn+"; a split is not stacked a second time")
	case len(run.checks) == 0:
		return b.combined(ctx, "the profile has no verify checks, so the order cannot be decided by testing")
	case run.push.sealTree == "":
		return b.combined(ctx, "the run's signed checks recorded no tree")
	}
	if b.env, err = stackSplitAuthorEnv(ctx, ws, sha); err != nil {
		return b.combined(ctx, "the split commits could not be built ("+err.Error()+")")
	}
	b.headMsg = gitOutput(ctx, ws, "log", "-1", "--format=%B", sha)
	// The latest decision, from the loop's published copy (off the loop).
	if prev, ok := o.Snapshot().StackSplits[identifier]; ok && prev.LowerSHA != "" && prev.UpperBranch == branch &&
		isStackSplitLowerBranch(prev.LowerBranch, branch) && (prev.Order == StackSplitCodeFirst || prev.Order == StackSplitPipelineFirst) {
		return b.resume(ctx, prev)
	}
	return b.fresh(ctx)
}

// fresh decides a new split's order by testing and stacks it.
func (b *stackSplitBuild) fresh(ctx context.Context) stackSplitOutcome {
	o, run := b.o, b.run
	codeCommit, err := b.commit(ctx, b.code, b.base, "code change only (guardrails.lanes.split)")
	if err != nil {
		return b.combined(ctx, "the code commit could not be built ("+err.Error()+")")
	}
	lower, lowerBranch := codeCommit, b.branch+stackSplitCodeSuffix
	verdict, err := o.checkSplitCommit(ctx, run, codeCommit, "code")
	if err != nil {
		return b.combined(ctx, "the code change could not be checked alone ("+err.Error()+")")
	}
	if ctx.Err() != nil {
		return stackSplitOutcome{}
	}
	if verdict.ok() {
		b.rec.Order, b.rec.Reason = StackSplitCodeFirst, "the signed checks pass on the code change alone"
	} else {
		codeFailure := firstLine(verdict.Failed)
		pipeCommit, err := b.commit(ctx, b.pipeline, b.base, "pipeline change only (guardrails.lanes.split)")
		if err != nil {
			return b.combined(ctx, "the pipeline commit could not be built ("+err.Error()+")")
		}
		verdict, err = o.checkSplitCommit(ctx, run, pipeCommit, "pipeline")
		if err != nil {
			return b.combined(ctx, "the pipeline change could not be checked alone ("+err.Error()+")")
		}
		if ctx.Err() != nil {
			return stackSplitOutcome{}
		}
		if !verdict.ok() {
			reason := "the signed checks fail on the code change alone (" + codeFailure +
				") and on the pipeline change alone (" + firstLine(verdict.Failed) + ")"
			if !b.hooked {
				reason += "; they ran in temporary worktrees with the committed files only, so they may lack installed dependencies " +
					"(node_modules, vendor): set hooks.after_create to install them"
			}
			return b.combined(ctx, reason)
		}
		lower, lowerBranch = pipeCommit, b.branch+stackSplitPipelineSuffix
		b.rec.Order = StackSplitPipelineFirst
		b.rec.Reason = "the signed checks fail on the code change alone (" + codeFailure + ") and pass on the pipeline change alone"
	}
	// Never push over a branch Itervox did not push: exactly this commit
	// (a split rebuilt after a restart) is the only remote state accepted.
	if remote := workspace.RemoteBranchSHA(ctx, b.ws, lowerBranch); remote != "" && remote != lower {
		return b.combined(ctx, fmt.Sprintf("the branch %s already exists on the remote (at %s) and is not the commit the split would push; "+
			"Itervox does not push over a branch it did not push", lowerBranch, shortHash(remote)))
	}
	return b.stack(ctx, lower, lowerBranch, verdict.sealTree(), true)
}

// resume keeps an earlier split whose lower branch is pushed: the lower
// branch moves only by a fast-forward from the commit Itervox pushed, and
// only when its side changed and still passes the checks; the top is
// stacked on it.
func (b *stackSplitBuild) resume(ctx context.Context, prev StackSplitRecord) stackSplitOutcome {
	b.rec.LowerPR, b.rec.LowerPRURL = prev.LowerPR, prev.LowerPRURL
	lowerBranch := prev.LowerBranch
	remote := workspace.RemoteBranchSHA(ctx, b.ws, lowerBranch)
	switch {
	case remote == "":
		return b.combined(ctx, "the lower branch "+lowerBranch+" of the earlier stack split is no longer on the remote")
	case remote != prev.LowerSHA:
		return b.combined(ctx, fmt.Sprintf("the lower branch %s of the earlier stack split moved since Itervox pushed it (%s, now %s); "+
			"Itervox does not push over it", lowerBranch, shortHash(prev.LowerSHA), shortHash(remote)))
	case !haveCommit(ctx, b.ws, remote, lowerBranch):
		return b.combined(ctx, "the lower branch "+lowerBranch+" of the earlier stack split could not be fetched")
	}
	// Until a new commit is pushed, the remote lower branch is still the
	// commit the split pushed (a held fast-forward keeps it so).
	b.rec.Order, b.rec.LowerSHA = prev.Order, prev.LowerSHA
	b.prev, b.keepLower = &prev, true
	side, files := "code", b.code
	if prev.Order == StackSplitPipelineFirst {
		side, files = "pipeline", b.pipeline
	}
	tree, err := splitTree(ctx, b.ws, b.base, b.sha, files)
	if err != nil {
		return b.combined(ctx, "the "+side+" commit could not be built ("+err.Error()+")")
	}
	if tree == treeOf(ctx, b.ws, remote) {
		b.rec.Reason = "the earlier stack split is kept; its lower branch " + lowerBranch + " is unchanged"
		return b.stack(ctx, remote, lowerBranch, "", false)
	}
	cand, err := commitTree(ctx, b.ws, tree, remote, stackSplitMessage(b.headMsg, side+" change only, updated (guardrails.lanes.split)"), b.env)
	if err != nil {
		return b.combined(ctx, "the "+side+" commit could not be built ("+err.Error()+")")
	}
	verdict, err := b.o.checkSplitCommit(ctx, b.run, cand, side)
	if ctx.Err() != nil {
		return stackSplitOutcome{}
	}
	if err != nil || !verdict.ok() {
		why := "fail on it"
		if err != nil {
			why = "could not run (" + err.Error() + ")"
		} else {
			why += " (" + firstLine(verdict.Failed) + ")"
		}
		b.rec.Reason = "the earlier stack split is kept; the " + side + " change made since then stays above " + lowerBranch +
			" because the signed checks " + why
		return b.stack(ctx, remote, lowerBranch, "", false)
	}
	b.keepLower = false // the lower branch is to carry this run's side
	b.rec.Reason = "the earlier stack split is kept; its lower branch " + lowerBranch + " is fast-forwarded with the " + side + " change made since then"
	return b.stack(ctx, cand, lowerBranch, verdict.sealTree(), true)
}

// stack moves the issue branch to the top commit (the run's tree on
// lower), pushes lower when pushLower (with lowerSeal, the tree its checks
// ran on), opens or reuses the lower pull request, then pushes the top and
// opens or reuses its pull request.
func (b *stackSplitBuild) stack(ctx context.Context, lower, lowerBranch, lowerSeal string, pushLower bool) stackSplitOutcome {
	o, run := b.o, b.run
	// The record's code side describes what is on the lower branch: the
	// earlier run's until this run's is pushed there.
	usePrevCode := func() {
		if b.prev != nil {
			b.rec.CodeLane, b.rec.CodeFiles, b.rec.CodeFilesTotal = b.prev.CodeLane, b.prev.CodeFiles, b.prev.CodeFilesTotal
		}
	}
	codeSide := b.rec
	if b.keepLower {
		usePrevCode()
	}
	topSide := "pipeline"
	if b.rec.Order == StackSplitPipelineFirst {
		topSide = "code"
	}
	top, err := commitTree(ctx, b.ws, b.sha+"^{tree}", lower, stackSplitMessage(b.headMsg, topSide+" change, stacked on "+lowerBranch+" (guardrails.lanes.split)"), b.env)
	if err != nil {
		return b.combined(ctx, "the stacked commit could not be built ("+err.Error()+")")
	}
	// The top has the run's own tree, so moving the issue branch to it
	// changes no file in the worktree.
	if out, err := gitexec.Command(ctx, b.ws, "reset", "--soft", "--quiet", top).CombinedOutput(); err != nil {
		return b.combined(ctx, "the issue branch could not be moved to the stacked commit ("+logging.RedactString(strings.TrimSpace(string(out)))+")")
	}
	b.rec.LowerBranch, b.rec.UpperBranch = lowerBranch, b.branch
	o.logPatchOut(b.identifier, run.push.runLogID, "INFO", fmt.Sprintf("worker: stack split (%s): %s, then %s stacked on it", b.rec.Order, lowerBranch, b.branch))

	res := stackSplitOutcome{handled: true, topBranch: b.branch, order: b.rec.Order}
	if pushLower {
		push := run.push
		push.branch, push.sha, push.sealTree = lowerBranch, lower, lowerSeal
		if r := o.daemonPush(ctx, push); r.hold != "" {
			res.hold = r.hold
			usePrevCode()
			o.sendStackSplit(ctx, b.identifier, b.rec)
			return res
		}
		b.rec.CodeLane, b.rec.CodeFiles, b.rec.CodeFilesTotal = codeSide.CodeLane, codeSide.CodeFiles, codeSide.CodeFilesTotal
	}
	b.rec.LowerSHA = lower
	if b.rec.LowerPRURL == "" {
		if url := o.openStackPR(ctx, run, lowerBranch, run.prBase, b.rec, true); url != "" {
			b.rec.LowerPRURL, b.rec.LowerPR = url, prdetector.PRNumber(url)
		}
	}
	res.lowerPRURL = b.rec.LowerPRURL
	o.sendStackSplit(ctx, b.identifier, b.rec) // before the top's push, which may wait for a human

	push := run.push
	push.branch, push.sha, push.baseRefs = b.branch, top, []string{lower}
	if r := o.daemonPush(ctx, push); r.hold != "" {
		res.hold = r.hold
		return res
	}
	res.topPRURL = o.openStackPR(ctx, run, b.branch, lowerBranch, b.rec, false)
	if res.topPRURL != "" {
		b.rec.UpperPRURL = res.topPRURL
		o.sendStackSplit(ctx, b.identifier, b.rec)
	}
	return res
}

// openStackPR opens one pull request of a stack split, or returns the one
// already open for head (a resumed run). The lower one names the issue
// without closing it; the top one, merged last, closes it.
func (o *Orchestrator) openStackPR(ctx context.Context, run stackSplitRun, head, base string, rec StackSplitRecord, lower bool) string {
	issue := run.push.issue
	prCtx, cancel := context.WithTimeout(ctx, postRunTimeout)
	defer cancel()
	if url := o.prFinder()(prCtx, run.push.wsPath, head); url != "" {
		o.logPatchOut(issue.Identifier, run.push.runLogID, "INFO", "worker: stack split: the pull request for "+head+" is already open: "+url)
		return url
	}
	codeOnLower := rec.Order == StackSplitCodeFirst
	side := "pipeline"
	if lower == codeOnLower {
		side = "code"
	}
	note := fmt.Sprintf("Stack split by Itervox (`guardrails.lanes.split`, %s): this pull request carries the %s change (%s).",
		strings.ReplaceAll(rec.Order, "_", " "), side, rec.Reason)
	var body string
	if lower {
		body = "Part of " + issue.Identifier + ".\n\n" + note + " `" + rec.UpperBranch + "` is stacked on it.\n"
	} else {
		issueURL := ""
		if issue.URL != nil {
			issueURL = *issue.URL
		}
		body = workspace.IssuePRBody(issue.Identifier, issueURL, o.cfg.Tracker.Kind,
			note+" It is stacked on `"+base+"`.\n\nPushed and opened by Itervox after its checks (patches only: `push: daemon`).")
	}
	url, err := o.prCreator()(prCtx, run.push.wsPath, head, base, issue.Identifier+": "+issue.Title+" ("+side+")", body)
	if err != nil {
		slog.Warn("worker: opening a stack split pull request failed",
			"issue_identifier", issue.Identifier, "branch", head, "error", logging.RedactString(err.Error()))
		o.logPatchOut(issue.Identifier, run.push.runLogID, "WARN", "worker: opening the pull request for "+head+" failed: "+logging.RedactString(err.Error()))
		return ""
	}
	return url
}

// stackSplitApplies reports whether a run's push may be split: only a
// patches-only run's (push: daemon), where Itervox controls the push.
func (o *Orchestrator) stackSplitApplies(patchOut bool) bool {
	return patchOut && o.cfg.Guardrails.Enabled && o.cfg.Guardrails.Lanes.Split
}

// firstLine is the first line of the first reason, for a one-line record.
func firstLine(reasons []string) string {
	if len(reasons) == 0 {
		return "no reason recorded"
	}
	line, _, _ := strings.Cut(reasons[0], "\n")
	return strings.TrimSpace(line)
}

// commentStackLowerPR links the lower pull request of a stack split on the
// tracker issue (the top one is linked like any run's PR). Best-effort.
func (o *Orchestrator) commentStackLowerPR(ctx context.Context, issue domain.Issue, split stackSplitOutcome, runLogID string) {
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, split.lowerPRURL) {
			return
		}
	}
	body := fmt.Sprintf("🔗 Pull request created: %s (stack split, %s: the lower pull request; %s is stacked on it)",
		split.lowerPRURL, strings.ReplaceAll(split.order, "_", " "), split.topBranch)
	if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(body)); err != nil {
		slog.Warn("worker: create stack split PR comment failed (ignored)",
			"issue_identifier", issue.Identifier, "error", err)
		return
	}
	o.logPatchOut(issue.Identifier, runLogID, "INFO", "worker: pr_opened url="+split.lowerPRURL)
}
