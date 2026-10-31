package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/workspace"
)

// runFinalizeTimeout bounds the git and tracker work after the agents
// stopped (push, the question's handoff commit).
const runFinalizeTimeout = 2 * time.Minute

// run drives the daemon's orchestrator, fed only this issue, until the run
// settles, then stops it and turns the final state into the summary.
//
// The orchestrator keeps its single-goroutine event loop: this goroutine
// only reads Snapshot() and RunHistory(), and its only inputs are the
// candidate list (singleIssueTracker) and the OneShot hooks set before Run.
func (s *runSession) run(ctx context.Context, runner agent.Runner) runSummary {
	single := &singleIssueTracker{Tracker: s.tr, issueID: s.issue.ID, active: s.cfg.Tracker.ActiveStates}
	orch := orchestrator.New(s.cfg, single, runner, s.ws)
	single.running = func() bool {
		_, ok := orch.Snapshot().Running[s.issue.ID]
		return ok
	}
	logBuf := logbuffer.New()
	staging := stageIssueLogs(logBuf, s.opts.logDir)
	orch.SetLogBuffer(logBuf)
	orch.SetAppSessionID(newAppSessionID())
	var spendBefore orchestrator.Spend
	if s.opts.logDir != "" {
		spendPath := filepath.Join(s.opts.logDir, "issue_spend.json")
		if ledger, err := orchestrator.ReadSpendFile(spendPath); err == nil {
			spendBefore = ledger.Issues[s.issue.Identifier]
		}
		orch.SetSpendFile(spendPath) // agent.budget (#136) counts across runs that share --log-dir
	}
	if s.deps.guardrailExe != "" {
		orch.SetGuardrailExecutable(s.deps.guardrailExe)
	}
	var approved []string
	if s.opts.approve != "" {
		approved = []string{s.opts.approve}
	}
	orch.SetOneShot(orchestrator.OneShot{
		OpenPR:              s.openPR,
		Resume:              s.resumeContext(ctx),
		ReplyHint:           runReplyHint,
		PreApprovedCommands: approved,
		OnApprovalRefused:   s.noteRefused,
	})
	orch.OnDispatch = func(string) { single.dispatched.Store(true) }
	wake := make(chan struct{}, 1)
	orch.OnStateChange = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	runCtx, cancelTimeout := ctx, context.CancelFunc(func() {})
	if s.opts.timeout > 0 {
		runCtx, cancelTimeout = context.WithTimeout(ctx, s.opts.timeout)
	}
	defer cancelTimeout()
	loopCtx, stopLoop := context.WithCancel(runCtx)
	defer stopLoop()
	done := make(chan error, 1)
	go func() {
		defer failFastOnPanic("run-orchestrator")
		done <- orch.Run(loopCtx)
	}()
	slog.Info("run: started", "issue", s.issue.Identifier, "branch_base", s.base, "worktree", s.opts.worktree)

	tick := time.NewTicker(runSettlePoll)
	defer tick.Stop()
	var stopErr error
	for stopErr == nil && !runSettled(orch.Snapshot(), s.issue, single.fetches.Load()) {
		select {
		case <-runCtx.Done():
			stopErr = runCtx.Err()
		case <-wake:
		case <-tick.C:
		}
	}
	stopLoop()
	<-done // joins the workers and every tracker write they started
	flushIssueLogs(logBuf, issueLogFlushTimeout, issueLogCloseTimeout)
	if staging != "" {
		copyRedactedIssueLogs(staging, filepath.Join(s.opts.logDir, "issues"))
		_ = os.RemoveAll(staging)
	}

	fctx, cancel := context.WithTimeout(context.Background(), runFinalizeTimeout)
	defer cancel()
	sum := s.classify(orch.Snapshot(), orch.RunHistory(), spendBefore, stopErr, ctx.Err() != nil)
	return s.finalize(fctx, sum)
}

// classify turns the final state into the summary and exit code.
func (s *runSession) classify(snap orchestrator.State, history []orchestrator.CompletedRun, spendBefore orchestrator.Spend, stopErr error, interrupted bool) runSummary {
	sum := s.newSummary()
	ident := s.issue.Identifier
	var impl *orchestrator.CompletedRun
	for i := range history {
		h := history[i]
		if h.Identifier != ident {
			continue
		}
		sum.InputTokens += h.InputTokens
		sum.OutputTokens += h.OutputTokens
		if h.Kind == "reviewer" {
			continue
		}
		sum.Turns += h.TurnCount
		impl = &history[i]
		if h.BranchName != "" {
			sum.Branch = h.BranchName
		}
		if h.PRURL != "" {
			sum.PRURL = h.PRURL
		}
	}
	if spend, ok := snap.Spend.Issues[ident]; ok && spend.CostKnown {
		c := spend.CostUSD - spendBefore.CostUSD
		sum.CostUSD = &c
	}
	for _, v := range snap.ReviewVerdicts[ident] {
		sum.Reviewers = append(sum.Reviewers, runReviewer{Profile: v.Profile, Verdict: v.Verdict, Reasons: v.Reasons})
	}
	if outcome, ok := snap.ReviewOutcomes[ident]; ok && len(sum.Reviewers) > 0 {
		sum.ReviewOutcome = map[bool]string{true: "blocked", false: "passed"}[outcome.Blocked]
	}
	if sum.Branch == "" {
		sum.Branch = workspace.GetCurrentBranch(context.Background(), s.ws.ResolvePath(ident))
	}

	switch {
	case interrupted:
		return s.finish(sum, "failed", runExitFailed, "interrupted by a signal")
	case errors.Is(stopErr, context.DeadlineExceeded):
		return s.finish(sum, "failed", runExitFailed, fmt.Sprintf("timed out after %s", s.opts.timeout))
	case snap.InputRequiredIssues[ident] != nil:
		sum.Question = snap.InputRequiredIssues[ident].Context
		sum.ApprovalCommands = s.refusedCommands()
		sum.asked = impl != nil && impl.Status == "input_required"
		msg := "the agent asked a question"
		if len(sum.ApprovalCommands) > 0 {
			msg = "approval needed for `" + strings.Join(sum.ApprovalCommands, "`, `") + "`"
		}
		return s.finish(sum, "needs_input", runExitNeedsInput, msg)
	case snap.PauseReasons[ident] == orchestrator.PauseReasonBudgetExceeded:
		return s.finish(sum, "budget_exceeded", runExitBudget, "agent.budget reached; the issue is paused")
	case impl == nil:
		reason := orchestrator.IneligibleReason(s.issue, snap, s.cfg)
		if reason == "" {
			reason = "not dispatched"
		}
		return s.finish(sum, "nothing_to_do", runExitOK, "the issue was not dispatched: "+reason)
	case impl.Status == "succeeded" && sum.PRURL != "":
		return s.finish(sum, "pr_opened", runExitOK, "pull request opened")
	case impl.Status == "succeeded":
		return s.finish(sum, "succeeded", runExitOK, "the run succeeded with no pull request (no changes to propose)")
	}
	return s.finish(sum, "failed", runExitFailed, failureMessage(snap, s.issue, impl.Status))
}

// failureMessage is why the run failed: the pending retry's error, the
// issue's latest recorded failure, or its last run's status.
func failureMessage(snap orchestrator.State, issue domain.Issue, status string) string {
	if r := snap.RetryAttempts[issue.ID]; r != nil && r.Error != nil && *r.Error != "" {
		return "the run failed: " + *r.Error
	}
	for i := len(snap.RecentFailures) - 1; i >= 0; i-- {
		if f := snap.RecentFailures[i]; f.Identifier == issue.Identifier && f.Message != "" {
			return "the run failed: " + f.Message
		}
	}
	if reason := snap.PauseReasons[issue.Identifier]; reason != "" {
		return "the run stopped: " + reason
	}
	return "the run ended " + status
}
