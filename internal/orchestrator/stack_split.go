package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
//     base + code alone (a temporary worktree). Pass: the code PR goes
//     first, against the base, and the pipeline PR is stacked on it. Fail:
//     the checks run on base + pipeline alone; pass: the pipeline PR goes
//     first and the code is stacked on it. Fail again: one combined PR, as
//     without the split, and the reason is recorded;
//   - the stack's top commit has exactly the run's tree, which its signed
//     checks already passed on, and the lower commit passed its own checks,
//     so no branch in the stack is a broken middle. Each push goes through
//     daemonPush (the seal's tree, the lanes, a human approval for the
//     human-lane push);
//   - the top lives on the issue branch (the worktree's branch is moved to
//     it, with no change to the files), the lower one on <branch>-code or
//     <branch>-pipeline.
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

// stackSplitAuthorEnv makes a split commit's author and committer those of
// the run's head commit, so the same split builds the same commits again
// (a resumed run pushes the lower branch it already pushed, unchanged).
func stackSplitAuthorEnv(ctx context.Context, ws, sha string) ([]string, error) {
	out, err := gitexec.Command(ctx, ws, "log", "-1", "--date=raw",
		"--format=%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read the head commit's author: %w", err)
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(f) != 6 {
		return nil, fmt.Errorf("orchestrator: unexpected author line for %s", shortHash(sha))
	}
	return []string{
		"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2],
		"GIT_COMMITTER_NAME=" + f[3], "GIT_COMMITTER_EMAIL=" + f[4], "GIT_COMMITTER_DATE=" + f[5],
	}, nil
}

// splitTree writes the tree of base with paths taken from commit from (a
// path from does not have is removed) and returns its id. A temporary
// index; the worktree's own index and files are untouched.
func splitTree(ctx context.Context, ws, base, from string, paths []string) (string, error) {
	tmp, err := os.MkdirTemp("", "itervox-split-index-")
	if err != nil {
		return "", fmt.Errorf("orchestrator: temp index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")
	git := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := gitexec.Command(ctx, ws, args...)
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: git %s: %w", args[0], err)
		}
		return out, nil
	}
	if _, err := git(nil, "read-tree", base); err != nil {
		return "", err
	}
	listing, err := git(nil, "ls-tree", "-r", "-z", "--full-tree", from)
	if err != nil {
		return "", err
	}
	entries := map[string]string{} // path → "mode type id"
	for _, rec := range bytes.Split(listing, []byte{0}) {
		if meta, p, ok := strings.Cut(string(rec), "\t"); ok {
			entries[p] = meta
		}
	}
	var info bytes.Buffer
	for _, p := range paths {
		if meta, ok := entries[p]; ok {
			info.WriteString(meta + "\t" + p + "\x00")
		} else {
			info.WriteString("0 0000000000000000000000000000000000000000\t" + p + "\x00")
		}
	}
	if _, err := git(info.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	out, err := git(nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// commitTree commits tree on parent with msg and the head commit's author.
func commitTree(ctx context.Context, ws, tree, parent, msg string, env []string) (string, error) {
	cmd := gitexec.Command(ctx, ws, "commit-tree", tree, "-p", parent, "-m", msg)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("orchestrator: git commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// checkSplitCommit runs the profile's signed checks on commit in a
// temporary worktree, records them under role, and returns the verdict.
func (o *Orchestrator) checkSplitCommit(ctx context.Context, run stackSplitRun, commit, role string) (verifyVerdict, error) {
	ws := run.push.wsPath
	dir, err := os.MkdirTemp("", "itervox-split-")
	if err != nil {
		return verifyVerdict{}, fmt.Errorf("orchestrator: temp worktree: %w", err)
	}
	wt := filepath.Join(dir, "wt")
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = gitexec.Command(rmCtx, ws, "worktree", "remove", "--force", wt).Run()
		_ = gitexec.Command(rmCtx, ws, "worktree", "prune").Run()
		_ = os.RemoveAll(dir)
	}()
	// No repository hooks run for the daemon's own checkout.
	if out, err := gitexec.Command(ctx, ws, "-c", "core.hooksPath="+os.DevNull,
		"worktree", "add", "--detach", "--quiet", wt, commit).CombinedOutput(); err != nil {
		return verifyVerdict{}, fmt.Errorf("orchestrator: git worktree add: %w: %s", err, logging.RedactString(strings.TrimSpace(string(out))))
	}
	vr := verifyRun{issue: run.push.issue, runID: attestRunID(run.runTimestamp, run.profile) + "_split-" + role,
		profile: run.profile, runLogID: run.push.runLogID}
	o.logPatchOut(vr.issue.Identifier, vr.runLogID, "INFO", "worker: stack split: running the signed checks on the "+role+" change alone")
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
	rec := StackSplitRecord{RunID: run.push.runLogID, Order: StackSplitCombined, CodeLane: lanes.Classify(code).Lane}
	rec.CodeFiles, rec.CodeFilesTotal = sanitizeLanePaths(code, len(code))
	rec.PipelineFiles, rec.PipelineFilesTotal = sanitizeLanePaths(pipeline, len(pipeline))
	combined := func(reason string) stackSplitOutcome {
		rec.Order, rec.Reason = StackSplitCombined, reason
		rec.LowerBranch, rec.UpperBranch = "", ""
		o.logPatchOut(identifier, run.push.runLogID, "INFO", "worker: stack split: one combined pull request: "+reason)
		o.sendStackSplit(ctx, identifier, rec)
		return stackSplitOutcome{}
	}
	switch {
	case run.stackedOn != "":
		return combined("the issue is stacked on its blocker's branch " + run.stackedOn + "; a split is not stacked a second time")
	case len(run.checks) == 0:
		return combined("the profile has no verify checks, so the order cannot be decided by testing")
	case run.push.sealTree == "":
		return combined("the run's signed checks recorded no tree")
	}
	env, err := stackSplitAuthorEnv(ctx, ws, sha)
	if err != nil {
		return combined("the split commits could not be built (" + err.Error() + ")")
	}
	subject := strings.TrimSpace(gitOutput(ctx, ws, "log", "-1", "--format=%s", sha))
	build := func(side string, files []string) (string, error) {
		tree, err := splitTree(ctx, ws, base, sha, files)
		if err != nil {
			return "", err
		}
		return commitTree(ctx, ws, tree, base, subject+" ("+side+" only)\n\nStack split by Itervox (guardrails.lanes.split).", env)
	}
	codeCommit, err := build("code", code)
	if err != nil {
		return combined("the code commit could not be built (" + err.Error() + ")")
	}
	lower, lowerBranch := codeCommit, branch+stackSplitCodeSuffix
	verdict, err := o.checkSplitCommit(ctx, run, codeCommit, "code")
	if err != nil {
		return combined("the code change could not be checked alone (" + err.Error() + ")")
	}
	if ctx.Err() != nil {
		return stackSplitOutcome{}
	}
	if verdict.ok() {
		rec.Order, rec.Reason = StackSplitCodeFirst, "the signed checks pass on the code change alone"
	} else {
		codeFailure := firstLine(verdict.Failed)
		pipeCommit, err := build("pipeline", pipeline)
		if err != nil {
			return combined("the pipeline commit could not be built (" + err.Error() + ")")
		}
		verdict, err = o.checkSplitCommit(ctx, run, pipeCommit, "pipeline")
		if err != nil {
			return combined("the pipeline change could not be checked alone (" + err.Error() + ")")
		}
		if ctx.Err() != nil {
			return stackSplitOutcome{}
		}
		if !verdict.ok() {
			return combined("the signed checks fail on the code change alone (" + codeFailure +
				") and on the pipeline change alone (" + firstLine(verdict.Failed) + ")")
		}
		lower, lowerBranch = pipeCommit, branch+stackSplitPipelineSuffix
		rec.Order = StackSplitPipelineFirst
		rec.Reason = "the signed checks fail on the code change alone (" + codeFailure + ") and pass on the pipeline change alone"
	}
	lowerSeal := verdict.sealTree()
	top, err := commitTree(ctx, ws, sha+"^{tree}", lower, subject+"\n\nStack split by Itervox (guardrails.lanes.split): stacked on "+lowerBranch+".", env)
	if err != nil {
		return combined("the stacked commit could not be built (" + err.Error() + ")")
	}
	// The top has the run's own tree, so moving the issue branch to it
	// changes no file in the worktree.
	if out, err := gitexec.Command(ctx, ws, "reset", "--soft", "--quiet", top).CombinedOutput(); err != nil {
		return combined("the issue branch could not be moved to the stacked commit (" + logging.RedactString(strings.TrimSpace(string(out))) + ")")
	}
	rec.LowerBranch, rec.UpperBranch = lowerBranch, branch
	o.logPatchOut(identifier, run.push.runLogID, "INFO", fmt.Sprintf("worker: stack split (%s): %s, then %s stacked on it", rec.Order, lowerBranch, branch))

	res := stackSplitOutcome{handled: true, topBranch: branch, order: rec.Order}
	push := run.push
	push.branch, push.sha, push.sealTree = lowerBranch, lower, lowerSeal
	if r := o.daemonPush(ctx, push); r.hold != "" {
		res.hold = r.hold
		o.sendStackSplit(ctx, identifier, rec)
		return res
	}
	res.lowerPRURL = o.openStackPR(ctx, run, lowerBranch, run.prBase, rec, true)
	rec.LowerPRURL, rec.LowerPR = res.lowerPRURL, prdetector.PRNumber(res.lowerPRURL)
	o.sendStackSplit(ctx, identifier, rec) // before the top's push, which may wait for a human

	push = run.push
	push.branch, push.sha, push.baseRefs = branch, top, []string{lower}
	if r := o.daemonPush(ctx, push); r.hold != "" {
		res.hold = r.hold
		return res
	}
	res.topPRURL = o.openStackPR(ctx, run, branch, lowerBranch, rec, false)
	rec.UpperPRURL = res.topPRURL
	o.sendStackSplit(ctx, identifier, rec)
	return res
}

// openStackPR opens one pull request of a stack split. The lower one names
// the issue without closing it; the top one, merged last, closes it.
func (o *Orchestrator) openStackPR(ctx context.Context, run stackSplitRun, head, base string, rec StackSplitRecord, lower bool) string {
	issue := run.push.issue
	prCtx, cancel := context.WithTimeout(ctx, postRunTimeout)
	defer cancel()
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

func gitOutput(ctx context.Context, ws string, args ...string) string {
	out, err := gitexec.Command(ctx, ws, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
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
