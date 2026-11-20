package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/workspace"
)

// Patches only: agent.push / agent.profiles.<name>.push: daemon. The
// agent of such a profile emits commits and nothing else: its guardrails
// runtime (prepareRunGuardrails with patchOut) strips every forge and
// tracker credential from its environment and refuses its pushes and
// pull-request commands. After a successful run, once after_run_required,
// the signed checks (verify) and the file part of require_evidence passed,
// the worker pushes the branch itself (daemonPush), with the daemon's
// credentials, through gitexec:
//
//   - the commit pushed is resolved once and pushed by its SHA, so what
//     was checked is what lands;
//   - with verify, its tree (handoffs and evidence left out, as the seal
//     leaves them out) must be the seal's tree: "the tree changed after
//     the checks" otherwise;
//   - the push is classified into guardrails.lanes like an agent's push in
//     the pre-push hook: human waits for a human approval through the same
//     approval flow (an input-required entry with GuardrailLanePaths), and
//     a lane above auto is recorded in State.PushLanes through
//     GuardrailLane before the push, so merge policy only gets stricter.
//
// Everything here runs on the worker goroutine and touches no State: the
// approval and the lane reach the event loop as events (EventGuardrailApproval,
// EventGuardrailLane), and a refused push ends the run input-required
// through the worker's exit event.

// patchOutApprovalPoll is how often a daemon push waiting for a human-lane
// approval reads the decision; patchOutBusyRetries bounds the retries of a
// request or lane report the saturated event loop refused.
const (
	patchOutApprovalPoll = time.Second
	patchOutBusyRetries  = 30
)

// patchOutPromptBlock tells a patch-out agent that publishing is not its
// job, so it does not spend turns on pushes its shims refuse.
const patchOutPromptBlock = "## Publishing (patches only)\n\n" +
	"Patches only: " + guardrails.PatchOutMessage + ". Do not run `git push`, " +
	"`gh pr create` or any other command that publishes or changes a pull request: they are refused, " +
	"and no forge credentials are available. Commit your work on the issue branch and end the run; " +
	"Itervox runs its checks, pushes the branch and opens the pull request."

// runPatchOut reports whether a run of profile is patches only. profile
// comes from the run's cfgMu snapshot; agent.push is read-only after
// startup.
func (o *Orchestrator) runPatchOut(profile config.AgentProfile) bool {
	return config.ResolvePush(profile.Push, o.cfg.Agent.Push) == config.PushDaemon
}

// patchOutStripEnv are the variables, beyond guardrails.ForgeCredentialEnv,
// a patch-out agent's environment loses: every variable holding the
// tracker key, whatever it is called. tracker.api_key is read-only after
// startup.
func (o *Orchestrator) patchOutStripEnv() []string {
	return guardrails.EnvNamesWithValue(os.Environ(), o.cfg.Tracker.APIKey)
}

// SetPRCreator replaces how the daemon opens the pull request for its own
// push (push: daemon); workspace.CreatePR (gh) by default. Must be called
// before Run.
func (o *Orchestrator) SetPRCreator(fn func(ctx context.Context, dir, head, base, title, body string) (string, error)) {
	o.createPR = fn
}

func (o *Orchestrator) prCreator() func(ctx context.Context, dir, head, base, title, body string) (string, error) {
	if o.createPR != nil {
		return o.createPR
	}
	return workspace.CreatePR
}

// daemonPushRun is one run's daemon push.
type daemonPushRun struct {
	issue    domain.Issue
	runLogID string
	wsPath   string
	// baseRefs classify the push and find the commits beyond the base.
	baseRefs []string
	// verify is whether the run had signed checks; sealTree the tree they
	// were sealed on ("" when the seal could not hash the tree).
	verify   bool
	sealTree string
	// branch and sha, when set, are what to push instead of the worktree's
	// current branch and HEAD: a stack split's two commits.
	branch, sha string
}

// daemonPushResult is the outcome of a daemon push.
type daemonPushResult struct {
	// branch and sha are what was (or would have been) pushed.
	branch, sha string
	// pushed: the remote branch now points at sha (pushed by this call or
	// already there). False with hold == "" means there was nothing to
	// push (no commits beyond the base).
	pushed bool
	// hold is the input-required context when the push was refused.
	hold string
}

// patchOutHoldContext is the input-required text for a refused daemon push.
func patchOutHoldContext(reason string) string {
	return "Patches only (push: daemon): Itervox did not push this run's branch.\n- " + reason +
		"\n\nFix the cause (or ask the agent to), then reply to resume; Itervox runs its checks again and pushes when the run ends."
}

// daemonPush pushes the run's branch for a patch-out run. Worker goroutine.
func (o *Orchestrator) daemonPush(ctx context.Context, run daemonPushRun) daemonPushResult {
	ws, identifier := run.wsPath, run.issue.Identifier
	branch := run.branch
	if branch == "" {
		branch = workspace.GetCurrentBranch(ctx, ws)
	}
	res := daemonPushResult{branch: branch}
	hold := func(reason string) daemonPushResult {
		res.hold = patchOutHoldContext(reason)
		slog.Warn("worker: daemon push refused (push: daemon)", "issue_identifier", identifier, "branch", branch, "reason", reason)
		o.logPatchOut(identifier, run.runLogID, "WARN", "worker: daemon push refused: "+reason)
		return res
	}
	if branch == "" || slices.Contains(protectedBranches(o.cfg), branch) {
		return hold(fmt.Sprintf("the worktree is not on an issue branch (it is on %s): Itervox pushes only the issue's own branch, never a protected one", cmp.Or(branch, "a detached HEAD")))
	}
	sha := run.sha
	if sha == "" {
		sha = attest.Head(ctx, ws)
	}
	if sha == "" {
		return hold("the worktree has no commit to push")
	}
	res.sha = sha
	if base := firstResolvableRef(ctx, ws, run.baseRefs); base != "" {
		out, err := gitexec.Command(ctx, ws, "rev-list", "--count", base+".."+sha).Output()
		if err == nil && strings.TrimSpace(string(out)) == "0" {
			o.logPatchOut(identifier, run.runLogID, "INFO", "worker: no commits beyond "+base+"; nothing for the daemon to push")
			return res
		}
	}
	if run.verify {
		if run.sealTree == "" {
			return hold("the signed checks recorded no tree to compare the push with")
		}
		tree, err := attest.CommitTreeHash(ctx, ws, sha, verifyTreeExcludes)
		if err != nil {
			return hold(fmt.Sprintf("the tree of commit %s could not be read (%v)", shortHash(sha), err))
		}
		if tree != run.sealTree {
			return hold(fmt.Sprintf("the tree changed after the checks: the signed checks passed on tree %s, and commit %s (the one to push) has tree %s. "+
				"Commit exactly what the checks ran on (no uncommitted changes, nothing changed after the run)", shortHash(run.sealTree), shortHash(sha), shortHash(tree)))
		}
	}
	remoteSHA := workspace.RemoteBranchSHA(ctx, ws, branch)
	if remoteSHA == sha {
		res.pushed = true
		return res
	}
	g := o.cfg.Guardrails // read-only after startup
	lr := guardrails.ClassifyPush(ws, branch, sha, remoteSHA,
		guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion}, run.baseRefs)
	if lr.Lane == guardrails.LaneHuman {
		if reason, ok := o.awaitDaemonPushApproval(ctx, run, branch, sha, remoteSHA, lr); !ok {
			return hold(reason)
		}
	}
	if guardrails.IsLane(lr.Lane) {
		paths := lr.Paths()
		rep := guardrails.LaneReport{Lane: lr.Lane, Paths: paths[:min(len(paths), guardrails.MaxLanePathsSent)], PathsTotal: len(paths)}
		if err := o.retryBusy(ctx, func() error { return o.GuardrailLane(identifier, run.runLogID, rep) }); err != nil {
			return hold(fmt.Sprintf("this push changes %s files (%s), and Itervox could not record that for its merge policy (%v)",
				lr.Lane, guardrails.LanePathSummary(paths, len(paths), guardrails.MaxLanePathsShown), err))
		}
	}
	if err := workspace.PushRef(ctx, ws, sha, branch); err != nil {
		return hold("the daemon's push failed: " + logging.RedactString(err.Error()))
	}
	res.pushed = true
	slog.Info("worker: daemon pushed the branch after its checks (push: daemon)",
		"issue_identifier", identifier, "branch", branch, "commit", shortHash(sha), "lane", lr.Lane)
	o.logPatchOut(identifier, run.runLogID, "INFO", fmt.Sprintf("worker: daemon pushed %s (%s) after its checks (push: daemon, lane %s)", branch, shortHash(sha), lr.Lane))
	return res
}

// awaitDaemonPushApproval holds a human-lane daemon push for a human
// approval, through the approval flow an agent's held push uses. ok means
// approved (the lane is then recorded by resolveGuardrailApproval);
// otherwise reason says why the push is refused.
func (o *Orchestrator) awaitDaemonPushApproval(ctx context.Context, run daemonPushRun, branch, sha, remoteSHA string, lr guardrails.LaneResult) (reason string, ok bool) {
	identifier := run.issue.Identifier
	sent := lr.Human[:min(len(lr.Human), guardrails.MaxLanePathsSent)]
	g := o.cfg.Guardrails // read-only after startup
	reasons, reasonsTotal := guardrails.PushReasons(run.wsPath, branch, sha, remoteSHA,
		guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion}, run.baseRefs, lr)
	req := guardrails.ApprovalRequest{
		Key:          guardrails.DaemonPushKey(run.wsPath, branch, sha),
		Command:      "git push origin " + branch + " (by Itervox for a patches-only run)",
		Lane:         guardrails.LaneHuman,
		Paths:        sent,
		PathsTotal:   len(lr.Human),
		Reasons:      reasons,
		ReasonsTotal: reasonsTotal,
	}
	var st guardrails.ApprovalStatus
	err := o.retryBusy(ctx, func() error {
		var rerr error
		st, rerr = o.RequestGuardrailApproval(identifier, run.runLogID, req)
		return rerr
	})
	if err != nil {
		return fmt.Sprintf("it changes %s (guardrails.lanes.human) and its approval could not be requested (%v)",
			guardrails.LanePathSummary(sent, len(lr.Human), guardrails.MaxLanePathsShown), err), false
	}
	o.logPatchOut(identifier, run.runLogID, "INFO", "worker: daemon push of "+branch+" waits for a human approval (guardrails.lanes.human)")
	id := st.ID
	for st.Status == guardrails.StatusPending {
		select {
		case <-ctx.Done():
			return "the run ended while its push waited for a human approval", false
		case <-time.After(patchOutApprovalPoll):
		}
		st = o.GuardrailApprovalStatus(identifier, run.runLogID, id)
	}
	switch st.Status {
	case guardrails.StatusApproved:
		return "", true
	case guardrails.StatusRejected:
		msg := strings.TrimSpace(st.Message)
		if msg == "" {
			msg = "no reason given"
		}
		return "the operator rejected the push of " + guardrails.LanePathSummary(sent, len(lr.Human), guardrails.MaxLanePathsShown) + ": " + msg, false
	default:
		return "the approval request for the push ended without an answer", false
	}
}

// retryBusy runs fn until it does not return ErrBusy (the event loop's
// channel was full), at most patchOutBusyRetries times.
func (o *Orchestrator) retryBusy(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < patchOutBusyRetries; attempt++ {
		if err = fn(); !errors.Is(err, ErrBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return err
}

// openDaemonPR opens the pull request for a daemon push when none is open
// for the branch, and returns its URL ("" on failure, which is logged: the
// run still succeeds, as when an agent's PR could not be found).
func (o *Orchestrator) openDaemonPR(ctx context.Context, wsPath, branch, base string, issue domain.Issue) string {
	prCtx, cancel := context.WithTimeout(ctx, postRunTimeout)
	defer cancel()
	issueURL := ""
	if issue.URL != nil {
		issueURL = *issue.URL
	}
	body := workspace.IssuePRBody(issue.Identifier, issueURL, o.cfg.Tracker.Kind,
		"Pushed and opened by Itervox after its checks (patches only: `push: daemon`).")
	url, err := o.prCreator()(prCtx, wsPath, branch, base, issue.Identifier+": "+issue.Title, body)
	if err != nil {
		slog.Warn("worker: opening the pull request for the daemon push failed",
			"issue_identifier", issue.Identifier, "branch", branch, "error", logging.RedactString(err.Error()))
		o.logPatchOut(issue.Identifier, "", "WARN", "worker: opening the pull request failed: "+logging.RedactString(err.Error()))
		return ""
	}
	return url
}

// withoutCI drops the "ci" check from required: it needs the pull request
// the daemon push opens, so the pre-push evidence gate cannot judge it.
func withoutCI(required []string) []string {
	return slices.DeleteFunc(slices.Clone(required), func(c string) bool { return c == config.EvidenceCheckCI })
}

// firstResolvableRef is the first of refs that names a commit in dir.
func firstResolvableRef(ctx context.Context, dir string, refs []string) string {
	for _, ref := range refs {
		if gitexec.Command(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil {
			return ref
		}
	}
	return ""
}

func (o *Orchestrator) logPatchOut(identifier, runLogID, level, msg string) {
	if o.logBuf != nil {
		o.logBuf.Add(identifier, makeBufLineWithSession(level, msg, runLogID))
	}
}
