package orchestrator

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/prdetector"
	"github.com/vnovick/itervox/internal/procgroup"
	"github.com/vnovick/itervox/internal/prompt"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// Worker-level timeout constants. maxTransitionAttempts (the completion-state
// transition retry count) lives in write_sink.go alongside directWriteSink,
// the only place that retry loop runs.
const (
	// hookFallbackTimeout is used when no explicit hook timeout is configured.
	hookFallbackTimeout = 30 * time.Second
	// postRunTimeout bounds post-run actions: branch push, PR comment, state transition.
	postRunTimeout = 60 * time.Second
)

// operatorReplyEnvelope is the orchestrator-controlled prompt block that tells
// every dispatched agent how the human operator will reply when the agent
// exits `input_required`.
const operatorReplyEnvelope = "## Operator Reply Channel\n\n" +
	"If you exit with status `input_required`, a human operator will see your question in the Itervox dashboard and reply via the \"Reply & Resume Agent\" textarea on this issue. Their reply will resume you with the answer prepended to your next prompt. Do not attempt to reach the operator through the tracker, email, or external APIs."

// runWorker implements the full per-issue lifecycle: workspace, hooks, multi-turn loop.
// Runs in its own goroutine; communicates back only via o.events.
// workerHost is the SSH host to run the agent on; empty string means run locally.
// agentCommand is the resolved agent command to run (may differ from cfg.Agent.Command
// when a per-issue profile override is active).
// backend is the resolved runner backend for this worker after applying any
// explicit backend overrides.
// profileName is the active named profile for this issue (may be ""); used to
// exclude the current agent from its own sub-agent context in teams mode.
// skipPRCheck bypasses the open-PR guard (used when a forced re-analysis is requested).
// resume, when non-nil, continues an existing agent session via --resume:
//   - SessionID set, UserMessage empty: pause/resume — normal prompt rendering.
//   - SessionID set, UserMessage set: input-required resume — the user's reply
//     replaces the rendered prompt, the run is capped at one turn, and PR
//     detection is skipped (the worktree already exists from the original
//     dispatch and we're continuing in-place).
//
// See ResumeContext in state.go for the full contract.
func (o *Orchestrator) runWorker(ctx context.Context, issue domain.Issue, attempt int, workerHost string, agentCommand string, backend string, profileName string, skipPRCheck bool, resume *ResumeContext, automation *AutomationDispatch, switchNotice *BackendSwitchNotice) {
	// CORE-026: outermost defer, so Run's join sees this worker done only
	// after everything below — including the panic path's sendExit — ran.
	defer o.workersWg.Done(issue.Identifier)
	// CORE-042: every agent and hook group this worker starts is labelled
	// with the issue in the orphan-group ledger.
	ctx = procgroup.WithLabel(ctx, issue.Identifier)
	defer func() {
		if r := recover(); r != nil {
			metrics.GoroutinePanic()
			err := fmt.Errorf("%w: %v", errWorkerPanic, r)
			slog.Error("worker panicked",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"panic", r,
				"stack", string(debug.Stack()))
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
		}
	}()

	// Assign a run-level log session ID immediately so every log entry — including
	// early hook/worker messages written before the agent subprocess starts — shares
	// the same session ID. This ID is used purely for log correlation in the Timeline;
	// the real Claude Code session ID (claudeSessionID below) is kept separately for
	// --resume continuity within the same run.
	runLogID := generateRunID()
	// Notify the event loop right away so the Timeline run record has a session ID
	// from the start, before the first agent turn completes.
	select {
	case o.events <- OrchestratorEvent{
		Type:     EventWorkerUpdate,
		IssueID:  issue.ID,
		RunEntry: &RunEntry{SessionID: runLogID},
	}:
	default:
		metrics.EventDropped() // CORE-045
		slog.Debug("orchestrator: worker update event dropped (channel full)", "issue_id", issue.ID)
	}

	// --- Workspace ---
	automationRun := automation != nil && !automation.UseIssueLifecycle
	hasResumeSession := resume != nil && resume.SessionID != ""
	hasResumeMessage := resume != nil && resume.UserMessage != ""
	inputRequiredResume := hasResumeSession && hasResumeMessage
	// Input-required resumes usually continue inside the workspace created by
	// the original dispatch. Keep that workspace when it still exists, but
	// fall back to fresh-dispatch setup if EnsureWorkspace has to recreate it
	// after a restart or manual cleanup.
	if inputRequiredResume {
		skipPRCheck = true
	}
	skipFreshDispatchSetup := inputRequiredResume

	wsPath := ""
	stackedOn := "" // the blocker branch this worktree is stacked on (#73)
	branchName := workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
	activeBranchName := branchName

	// Detect open PR (best-effort). On success, use the PR branch so the worktree
	// checks out the existing branch instead of creating a new one.
	var prCtx *prdetector.PRContext
	var detectedPRURL string // PR URL discovered during this run (pre-existing or newly created)
	var openedPRURL string   // PR URL first discovered during this run.
	var openedPRBranch string
	if !skipPRCheck {
		prCtx, _ = prdetector.Detect(ctx, issue)
		if prCtx != nil && prCtx.Branch == "" {
			// Treat a PR with no head branch as "not found" — an empty branch name
			// passed to EnsureWorkspace produces an unnamed worktree.
			slog.Warn("worker: open PR has empty branch, ignoring",
				"issue_identifier", issue.Identifier, "pr_url", prCtx.URL)
			prCtx = nil
		}
		if prCtx != nil {
			branchName = prCtx.Branch
			activeBranchName = prCtx.Branch
			detectedPRURL = prCtx.URL
			slog.Info("worker: open PR detected, using PR branch",
				"issue_identifier", issue.Identifier, "branch", prCtx.Branch, "pr_url", prCtx.URL)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLine("INFO",
					fmt.Sprintf("worker: pr_context url=%s branch=%s", prCtx.URL, prCtx.Branch)))
			}
		}
	} else {
		reason := "forced re-analysis requested"
		logMsg := "worker: forced re-analysis of existing PR"
		if inputRequiredResume {
			reason = "input-required resume"
			logMsg = "worker: resuming existing workspace after input request"
		}
		slog.Info("worker: skipping PR check",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier, "reason", reason)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", logMsg, runLogID))
		}
	}

	if o.workspace != nil {
		// Stacked PRs: when enabled and the issue has exactly one live
		// blocker, base this worktree on that blocker's branch so the PR
		// shows only the increment. Opt-in, best-effort, and type-asserted —
		// a provider that cannot stack, or a blocker branch that does not
		// exist here, falls back to workspace.base_branch unchanged.
		ws, err := o.ensureWorkspaceMaybeStacked(ctx, issue, branchName)
		if err != nil {
			slog.Warn("worker: workspace setup failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: workspace setup failed: %v", err), runLogID))
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
			return
		}
		wsPath = ws.Path
		stackedOn = ws.StackedOn
		// #73 follow-up: an issue whose only blocker is in review is admitted
		// so it can start stacked on that blocker's branch. If the fresh
		// worktree could not be stacked (the branch is not here), starting on
		// base_branch would build on code the blocker has not landed: back
		// out before any agent runs and let the issue wait for the blocker.
		if shouldBackOutUnstacked(ws.CreatedNow, inputRequiredResume, stackedOn, o.reviewStackKeyNow(issue)) {
			slog.Info("worker: in-review blocker's branch not available to stack on; waiting for the blocker",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO",
					"worker: blocker is in review but its branch is not available here to stack on; waiting for it to land", runLogID))
			}
			// Delete the branch only if it carries nothing of its own: a
			// branch that existed before (someone's work) is only checked
			// out into the new worktree, and must survive the back-out.
			removeBranch := ""
			if !branchCarriesOwnCommits(ctx, wsPath, branchName) {
				removeBranch = branchName
			}
			if err := o.workspace.RemoveWorkspace(ctx, issue.Identifier, removeBranch); err != nil {
				slog.Warn("worker: remove unstacked workspace failed",
					"issue_identifier", issue.Identifier, "error", err)
			}
			o.sendExit(ctx, issue, attempt, TerminalStackUnavailable, nil)
			return
		}
		if inputRequiredResume && ws.CreatedNow {
			skipFreshDispatchSetup = false
			slog.Info("worker: input-required resume recreated workspace, rerunning setup",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", "worker: recreated workspace for input-required resume", runLogID))
			}
		}

		if ws.CreatedNow && !skipFreshDispatchSetup {
			hookLog := o.hookLogFn(issue.Identifier, runLogID)
			if err := workspace.RunHook(ctx, o.cfg.Hooks.AfterCreate, wsPath, o.cfg.Hooks.TimeoutMs, hookLog); err != nil {
				slog.Warn("worker: after_create hook failed, removing workspace so next retry re-runs it",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
				if o.logBuf != nil {
					o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: after_create hook failed: %v", err), runLogID))
				}
				_ = o.workspace.RemoveWorkspace(ctx, issue.Identifier, "")
				o.sendExit(ctx, issue, attempt, TerminalFailed, err)
				return
			}
		}
	}

	// Enrich PR context with diffs now that wsPath is known (best-effort).
	// BaseBranch is read-only after startup — no lock required.
	if prCtx != nil && wsPath != "" {
		prdetector.FetchPRContext(ctx, prCtx, wsPath, o.cfg.Agent.BaseBranch)
	}

	// Transition issue to working state (e.g. Todo → In Progress).
	if !skipFreshDispatchSetup && !automationRun {
		o.transitionToWorking(ctx, issue)
	}

	// Log the backend being used for this worker.
	displayBackend := backend
	if displayBackend == "" {
		displayBackend = "claude"
	}
	slog.Info("worker: starting",
		"issue_id", issue.ID, "issue_identifier", issue.Identifier,
		"attempt", attempt, "backend", displayBackend, "profile", profileName)
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: starting (backend=%s)", displayBackend), runLogID))
	}

	// Snapshot mutable config fields once under cfgMu so the entire worker
	// lifecycle uses a consistent view without holding the lock.
	o.cfgMu.RLock()
	maxTurns := o.cfg.Agent.MaxTurns
	readTimeoutMs := o.cfg.Agent.ReadTimeoutMs
	turnTimeoutMs := o.cfg.Agent.TurnTimeoutMs
	beforeRunHook := o.cfg.Hooks.BeforeRun
	afterRunHook := o.cfg.Hooks.AfterRun
	hookTimeoutMs := o.cfg.Hooks.TimeoutMs
	profilesSnap := make(map[string]config.AgentProfile, len(o.cfg.Agent.Profiles))
	maps.Copy(profilesSnap, o.cfg.Agent.Profiles)
	o.cfgMu.RUnlock()

	profileAllowedActions := filterAllowedActionsForAutomation(profilesSnap[profileName].AllowedActions, automation)
	// Resolved once from the same cfgMu snapshot as the rest of the profile,
	// so every turn of this run launches with a consistent permission mode
	// even if the profile is edited mid-run (issue #66).
	permissionMode := agent.ParsePermissionMode(profilesSnap[profileName].PermissionMode)
	profileCreateIssueState := strings.TrimSpace(profilesSnap[profileName].CreateIssueState)
	profileMoveIssueState := ""
	if automation != nil && automation.Trigger.Type == config.AutomationTriggerBlockersResolved {
		profileMoveIssueState = strings.TrimSpace(automation.MoveToState)
	}
	actionContext := ""
	if len(profileAllowedActions) > 0 {
		if workerHost != "" {
			actionContext = buildAgentActionContext(profileAllowedActions, profileCreateIssueState, profileMoveIssueState, true)
		} else if o.agentActionTokens == nil || o.agentActionBaseURL == "" {
			slog.Warn("worker: profile allowed_actions configured but daemon action bridge is unavailable",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "profile", profileName)
		} else if shimDir, token, err := prepareAgentActionRuntime(
			o.agentActionTokens,
			issue.Identifier,
			runLogID,
			profileAllowedActions,
			profileCreateIssueState,
			profileMoveIssueState,
			turnTimeoutMs,
		); err != nil {
			slog.Warn("worker: failed to prepare daemon action bridge",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "profile", profileName, "error", err)
		} else {
			pathValue := shimDir
			if currentPath := os.Getenv("PATH"); currentPath != "" {
				pathValue = shimDir + string(os.PathListSeparator) + currentPath
			}
			agentCommand = prependEnvToCommand(agentCommand, map[string]string{
				"ITERVOX_ACTION_TOKEN":       token,
				"ITERVOX_DAEMON_URL":         o.agentActionBaseURL,
				"ITERVOX_CREATE_ISSUE_STATE": profileCreateIssueState,
				"ITERVOX_ISSUE_IDENTIFIER":   issue.Identifier,
				"ITERVOX_MOVE_ISSUE_STATE":   profileMoveIssueState,
				"ITERVOX_RUN_ID":             runLogID,
				"PATH":                       pathValue,
			})
			actionContext = buildAgentActionContext(profileAllowedActions, profileCreateIssueState, profileMoveIssueState, false)
			defer func() {
				o.agentActionTokens.Revoke(token)
				_ = os.RemoveAll(shimDir)
			}()
		}
	}

	// --- Multi-turn loop ---
	// before_run hook runs once per worker invocation (not per turn), so that
	// hooks like "git reset --hard origin/main" set up a clean workspace for the
	// attempt without wiping Claude's work between turns.
	if wsPath != "" && !skipFreshDispatchSetup {
		hookLog := o.hookLogFn(issue.Identifier, runLogID)
		if err := workspace.RunHook(ctx, beforeRunHook, wsPath, hookTimeoutMs, hookLog); err != nil {
			slog.Warn("worker: before_run hook failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: before_run hook failed: %v", err), runLogID))
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
			return
		}
	}

	// Checkout the issue's tracked branch so retried workers resume the agent's
	// previous work instead of starting over from the default branch.
	// Runs AFTER before_run (which resets to main) so the branch layering is:
	//   1. before_run: git checkout main && git reset --hard origin/main
	//   2. (here):    git checkout <feature-branch>   ← agent continues from here
	// On a fresh dispatch the branch typically doesn't exist yet — the checkout
	// fails silently and the agent creates it during its first turn.
	// In worktree mode the branch is already checked out by EnsureWorkspace.
	// CheckoutBranch is only needed in legacy directory mode.
	o.cfgMu.RLock()
	worktreeMode := o.cfg.Workspace.Worktree
	o.cfgMu.RUnlock()
	if wsPath != "" && !skipFreshDispatchSetup && !worktreeMode && issue.BranchName != nil && *issue.BranchName != "" {
		if b := *issue.BranchName; !workspace.IsDefaultBranch(b) {
			if err := workspace.CheckoutBranch(ctx, wsPath, b); err != nil {
				slog.Warn("worker: branch checkout failed, agent will start from current branch",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier,
					"branch", b, "error", err)
			} else {
				slog.Info("worker: resuming on tracked branch",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "branch", b)
			}
		}
	}

	var claudeSessionID *string
	// Pre-populate sessionID for --resume when this worker is continuing an
	// existing agent session (pause/resume or input-required resume).
	if resume != nil && resume.SessionID != "" {
		sid := resume.SessionID
		claudeSessionID = &sid
		slog.Info("worker: resuming existing session",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier,
			"session_id", resume.SessionID,
			"input_required", resume.UserMessage != "")
	}
	var allTextBlocks []string                                  // accumulate all Claude text blocks for the final tracker comment
	var cumulativeInput, cumulativeCached, cumulativeOutput int // accumulate tokens across turns for dashboard display
	var prevResultText string                                   // detect repeated identical responses (Codex resume loop)
	// Input-required resume: single turn with the user's reply as the prompt.
	// Normal dispatch and pause/resume use the configured max.
	effectiveMaxTurns := maxTurns
	if inputRequiredResume {
		effectiveMaxTurns = 1
	}
	startedAt := time.Now()
	// Agent handoff plumbing: generate ONE timestamp per worker invocation
	// (not per turn) so the `run.handoff_path` the agent sees is stable
	// across all turns. If we regenerated per turn, an agent following
	// INSTRUCTIONS would write multiple files per worker run, and turn N's
	// prompt would include turn 1..N-1's outputs as "prior agent handoffs."
	runTimestamp := handoffRunTimestamp(startedAt)
	runHandoffRelPath := handoffPathFor(runTimestamp, profileName)
	// CORE-101: the Liquid `run` object for the WORKFLOW.md body, profile
	// SOUL/INSTRUCTIONS and automation instructions.
	runEvidenceRelPath := ""
	evidenceBlock := ""
	if required := profilesSnap[profileName].RequireEvidence; len(required) > 0 {
		runEvidenceRelPath = evidenceRelPathFor(profileName)
		evidenceBlock = buildEvidenceBlock(required, runEvidenceRelPath)
	}
	runVars := runBindings(runTimestamp, runHandoffRelPath, o.prBaseBranch(stackedOn), runEvidenceRelPath, switchNotice)
	// Result of the most recent after_run hook invocation. When
	// hooks.after_run_required is set, the final turn's hook result gates
	// TerminalSucceeded (spec F3: a unit is not done on the agent's
	// self-assessment alone).
	var lastAfterRunErr error
	turn := 1
	for ; turn <= effectiveMaxTurns; turn++ {
		// Enrich issue with comments before rendering the first-turn prompt.
		if turn == 1 {
			if detailed, err := o.tracker.FetchIssueDetail(ctx, issue.ID); err != nil {
				slog.Warn("worker: fetch issue detail failed (using cached issue)",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			} else {
				issue = *detailed
			}
		}

		// Render prompt — reviewer workers use the reviewer_prompt template
		// instead of the main WORKFLOW.md body.
		var attemptPtr *int
		if attempt > 0 {
			a := attempt
			attemptPtr = &a
		}
		o.cfgMu.RLock()
		isReviewer := profileName != "" && profileName == o.cfg.Agent.ReviewerProfile
		reviewerTmpl := o.cfg.Agent.ReviewerPrompt
		o.cfgMu.RUnlock()

		promptTemplate := o.cfg.PromptTemplate
		if isReviewer && reviewerTmpl != "" {
			promptTemplate = reviewerTmpl
		}
		renderedPrompt, err := prompt.RenderWith(promptTemplate, issue, attemptPtr, runVars)
		if err != nil {
			slog.Warn("worker: prompt render failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: prompt render failed: %v", err), runLogID))
			}
			// Use a fresh background context for the after_run hook on the failure
			// path: the worker context may already be cancelled.
			// Use an explicit cancel call (not defer) because this block is inside
			// a loop — defer would not fire until runWorker returns (GO-R10-2).
			if wsPath != "" {
				hookCtx, hookCancel := context.WithTimeout(context.Background(), hookFallbackTimeout)
				if hookErr := workspace.RunHook(hookCtx, afterRunHook, wsPath, hookTimeoutMs, o.hookLogFn(issue.Identifier, runLogID)); hookErr != nil {
					slog.Warn("worker: after_run hook failed (ignored)", "issue_id", issue.ID, "error", hookErr)
				}
				hookCancel()
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
			return
		}

		// Operator-reply envelope notice. Lives in the
		// orchestrator-controlled wrapper (not in any profile's INSTRUCTIONS.md)
		// so individual `agent.profiles.<name>.instructions_file` overrides
		// cannot drop it. Without this, agents that decide they need human
		// input often escalate via tracker / external API calls instead of
		// exiting input_required, because nothing in their prompt tells them
		// a reply lane exists.
		renderedPrompt += "\n\n" + operatorReplyEnvelope

		// Agent handoff plumbing: inline any prior agents' handoffs from the
		// workspace (chronological, budget-truncated) and tell the agent
		// where to write its own deliverable. runTimestamp / runHandoffRelPath
		// are generated once per worker invocation above the turn loop so the
		// agent sees a stable path across all turns of this run.
		if priorHandoffs := buildHandoffContextBlock(wsPath, DefaultHandoffBudgetBytes); priorHandoffs != "" {
			renderedPrompt += "\n\n" + priorHandoffs
		}
		renderedPrompt += "\n\n" + buildRunContextBlock(runTimestamp, runHandoffRelPath)
		if block := buildStackedPRBlock(stackedOn); block != "" {
			renderedPrompt += "\n\n" + block
		}
		if evidenceBlock != "" {
			renderedPrompt += "\n\n" + evidenceBlock
		}
		// CORE-101: daemon-owned, after the envelope and handoffs and before
		// every profile block, so an instructions_file cannot drop it.
		if block := buildBackendSwitchNoticeBlock(switchNotice); block != "" {
			renderedPrompt += "\n\n" + block
		}
		// #58 — when this run is one profile of a multi-reviewer chain, tell
		// the agent where to record its verdict. Empty (and therefore a
		// no-op) for normal workers and single-reviewer setups.
		if verdictPath := o.reviewVerdictRelPathCfg(issue.Identifier, profileName); verdictPath != "" {
			renderedPrompt += "\n\n" + buildReviewVerdictBlock(verdictPath)
		}

		// Append the active profile's prompt (role context) whenever a named
		// profile is selected. The pre-removal `agent_mode == "teams"` gate
		// has been deleted (agent_mode is gone — see CHANGELOG); the
		// subagent roster context below also injects unconditionally so
		// multi-profile setups always tell the agent who its peers are.
		if profileName != "" {
			if profile, ok := profilesSnap[profileName]; ok {
				for _, block := range renderProfilePromptBlocks(profile, issue, attemptPtr, runVars) {
					if block != "" {
						renderedPrompt += "\n\n" + block
					}
				}
			}
		}
		if automation != nil && automation.Instructions != "" {
			bindings := automationTriggerBindings(automation)
			maps.Copy(bindings, runVars)
			renderedPrompt += "\n\n" + prompt.RenderPromptOverlay(
				automation.Instructions,
				issue,
				attemptPtr,
				bindings,
			)
		}
		if actionContext != "" {
			renderedPrompt += "\n\n" + actionContext
		}
		// Append sub-agent roster context whenever the inventory has more than
		// one profile — gives the active backend the names + descriptions of
		// peer agents it can spawn via its delegation tool. `buildSubAgentContext`
		// returns "" when there's nothing to say, so single-profile setups
		// see no change.
		if subCtx := buildSubAgentContext(profilesSnap, profileName, backend); subCtx != "" {
			renderedPrompt += "\n\n" + subCtx
		}

		// On the first turn, inject open PR context if detected.
		if turn == 1 {
			if prBlock := prdetector.FormatPRContext(prCtx); prBlock != "" {
				renderedPrompt += "\n\n" + prBlock
			}
		}

		// Input-required resume: replace the rendered prompt with the user's
		// reply so the agent receives the answer to its question.
		// For pause/resume (UserMessage empty), keep the rendered prompt
		// as-is — Claude Code's `--resume <sid> -p <prompt>` continues the
		// conversation with the prompt as a new user message. Without -p,
		// --resume requires a deferred-tool marker in the session, which
		// doesn't exist when the session was paused between tool calls.
		if hasResumeMessage {
			if inputRequiredResume {
				renderedPrompt = resume.UserMessage
			} else {
				var b strings.Builder
				b.WriteString(renderedPrompt)
				b.WriteString("\n\nA previous Itervox run asked the human for input before continuing.")
				if resume.InputContext != "" {
					b.WriteString("\n\nAgent request:\n")
					b.WriteString(resume.InputContext)
				}
				b.WriteString("\n\nUser reply:\n")
				b.WriteString(resume.UserMessage)
				renderedPrompt = b.String()
			}
		}

		// Run agent turn — pass a logger pre-seeded with the issue identifier so
		// Claude's live output appears in the log stream and can be filtered by identifier.
		// onProgress sends a live EventWorkerUpdate each time Claude produces output so
		// the dashboard reflects token counts and session ID mid-turn.
		workerLog := &bufLogger{
			base:       slog.With("issue_id", issue.ID, "issue_identifier", issue.Identifier),
			buf:        o.logBuf,
			identifier: issue.Identifier,
			sessionID:  runLogID,
		}
		// forwarder tracks the last api_retry throttle delivered this turn.
		// Every new vendor signal is a fresh *LimitSignal (agent.mergeLimit
		// copies), so pointer identity forwards each api_retry once for the
		// event loop's backend breaker (CORE-053). BH-M3-5: it is marked
		// delivered only after the non-blocking send below succeeded.
		var forwarder limitForwarder
		onProgress := func(partial agent.TurnResult) {
			var limit *agent.LimitSignal
			fresh := forwarder.candidate(partial.LastLimit)
			if fresh != nil {
				c := *fresh
				limit = &c
			}
			select {
			case o.events <- OrchestratorEvent{
				Type:    EventWorkerUpdate,
				IssueID: issue.ID,
				RunEntry: &RunEntry{
					TurnCount:    turn,
					TotalTokens:  cumulativeInput + cumulativeCached + cumulativeOutput + partial.TotalTokens,
					InputTokens:  cumulativeInput + partial.InputTokens,
					OutputTokens: cumulativeOutput + partial.OutputTokens,
					SessionID:    runLogID,
					LastMessage:  partial.LastText,
				},
				Limit: limit,
			}:
				if fresh != nil {
					forwarder.sent(fresh)
				}
			default:
				metrics.EventDropped() // CORE-045
				slog.Debug("orchestrator: worker update event dropped (channel full)", "issue_id", issue.ID)
			}
		}
		// M2-close: refuse a prompt the backend's CLI would reject, loudly
		// and before it starts (never a silent truncation, never an error
		// buried in the CLI's stderr).
		if err := agent.ValidatePromptSize(backend, renderedPrompt); err != nil {
			o.logger().Error("worker: prompt exceeds the backend's input cap; run not started",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"turn", turn, "backend", backend, "error", err)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, formatBufLine("ERROR", "worker: prompt exceeds the backend's input cap", []any{"detail", err.Error(), "session_id", runLogID}))
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
			return
		}
		turnStart := time.Now()
		logDir := ""
		if o.agentLogDir != "" {
			logDir = filepath.Join(o.agentLogDir, workspace.SanitizeKey(issue.Identifier))
		}
		result, runErr := o.runner.RunTurn(ctx, workerLog, onProgress, claudeSessionID, renderedPrompt, wsPath,
			agentCommand, workerHost, logDir, readTimeoutMs, turnTimeoutMs, permissionMode)

		if result.SessionID != "" {
			s := result.SessionID
			claudeSessionID = &s
			// Propagate the agent's real session ID to state.Running so that
			// manual pause/resume can capture it. Non-blocking — drop on busy
			// channel; the next progress update will retry.
			select {
			case o.events <- OrchestratorEvent{
				Type:     EventWorkerUpdate,
				IssueID:  issue.ID,
				RunEntry: &RunEntry{AgentSessionID: s},
			}:
			default:
				metrics.EventDropped() // CORE-045
			}
		}

		// Accumulate all Claude text blocks for the final session comment.
		allTextBlocks = append(allTextBlocks, result.AllTextBlocks...)

		// after_run hook. Best-effort by default; the final turn's result
		// becomes the per-unit gate when hooks.after_run_required is set.
		lastAfterRunErr = o.runAfterHook(ctx, afterRunHook, hookTimeoutMs, wsPath, issue.ID, issue.Identifier, runLogID)

		// Track the current git branch after each turn so retried workers can
		// resume from the same branch. Only fires when the agent has switched to
		// a non-default branch (i.e. created its feature branch).
		if wsPath != "" {
			if currentBranch := workspace.GetCurrentBranch(ctx, wsPath); !workspace.IsDefaultBranch(currentBranch) {
				if issue.BranchName == nil || *issue.BranchName != currentBranch {
					if err := o.tracker.SetIssueBranch(ctx, issue.ID, currentBranch); err != nil {
						workerLog.Warn("worker: set branch failed (ignored)",
							"branch", currentBranch, "error", err)
					} else {
						b := currentBranch
						issue.BranchName = &b
						activeBranchName = currentBranch
						workerLog.Info("worker: branch tracked on issue", "branch", currentBranch)
					}
				}
			}
		}

		// CORE-051: a failed turn carrying a quota limit signal (CORE-050)
		// exits TerminalRateLimited so the event loop can evaluate the
		// rate_limited fallback on this first failure. It runs BEFORE the
		// input-required branch: a limit is never a question a human reply
		// can answer (CORE-166). An orchestrator cancellation stays a
		// cancellation.
		if (result.Failed || runErr != nil) && result.LastLimit.Terminal() && !errors.Is(runErr, context.Canceled) {
			failureText := logging.RedactString(result.FailureText)
			cause := turnFailureCause(turn, failureText, runErr)
			o.logger().Warn("worker: turn hit a vendor usage limit",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn,
				"limit_type", result.LastLimit.LimitType, "limit_source", result.LastLimit.Source,
				"resets_at", formatResetsAt(result.LastLimit), "error", boundedTurnFailureLog(cause))
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, formatBufLine("WARN", "worker: turn hit a vendor usage limit",
					[]any{"limit_type", result.LastLimit.LimitType, "resets_at", formatResetsAt(result.LastLimit), "session_id", runLogID}))
			}
			o.sendExitWithLimit(ctx, issue, attempt, TerminalRateLimited, cause, limitForHost(result.LastLimit, workerHost))
			return
		}

		// InputRequired means the agent needs human input to continue (e.g.
		// permission prompt, API key). Send TerminalInputRequired so the event
		// loop queues the issue for user input instead of retrying or completing.
		//
		// CORE-164: this runs BEFORE the Failed branch. The vendor parsers flag
		// InputRequired only on an error result event (Claude result is_error,
		// Codex turn.failed), which also sets Failed, so checking Failed first
		// turned every vendor-signalled input request into TerminalFailed
		// (retry / failed_state) and left only the text sentinel reaching this
		// branch. A runner error (read timeout, start failure, cancellation) is
		// still a failure: the stream did not end on the input request.
		if result.InputRequired && runErr == nil {
			o.queueInputRequiredEntry(
				ctx,
				issue,
				attempt,
				runLogID,
				claudeSessionID,
				backend,
				agentCommand,
				workerHost,
				profileName,
				activeBranchName,
				explicitInputRequiredContext(result),
				"",
				buildInputRequiredExitRunEntry(
					issue,
					attempt,
					startedAt,
					turn,
					runLogID,
					workerHost,
					backend,
					cumulativeInput,
					cumulativeCached,
					cumulativeOutput,
					result,
				),
			)
			return
		}

		// A runner error is always a failed turn, whatever result.Failed says
		// (CORE-029): a runner that returns an error must never fall through
		// to the 0-token "session concluded" break below.
		if result.Failed || runErr != nil {
			// A result error with no failure text and no tokens produced means the
			// claude CLI was asked to --resume a session that had already concluded.
			// Treat this as a clean session end rather than a real failure so the
			// issue does not land in the retry queue. Only when the runner itself
			// returned no error (CORE-029): a read timeout, start failure or
			// cancellation before the first token is not a concluded session.
			if runErr == nil && result.FailureText == "" && result.InputTokens == 0 && result.OutputTokens == 0 {
				slog.Info("worker: empty result error on 0-token turn — treating as clean session end",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
				break
			}
			// CORE-167: FailureText embeds agent stderr, which can carry
			// exported secrets. The runners already redact it where they
			// assemble it; redact again here so a runner that does not (a
			// future backend, a test double) cannot leak through the exit
			// cause, the retry row on the dashboard, the tracker comment on
			// exhaustion, or the log lines below. Idempotent on redacted text.
			failureText := logging.RedactString(result.FailureText)
			cause := turnFailureCause(turn, failureText, runErr)
			// The WARN line carries the agent's own message plus only the
			// last turnFailedLogStderrTail bytes of stderr (where the root
			// cause is); the full redacted FailureText (up to 64 KiB) is on
			// the DEBUG line and in the per-issue dashboard log.
			o.logger().Warn("worker: turn failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"turn", turn, "error", boundedTurnFailureLog(cause))
			o.logger().Debug("worker: turn failed (full failure text)",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"turn", turn, "error", logging.RedactString(cause.Error()))
			// Emit full error detail to the per-issue log buffer (#7).
			if o.logBuf != nil {
				detail := logging.RedactString(cause.Error())
				if failureText != "" && !strings.Contains(detail, failureText) {
					detail = detail + " | " + failureText
				}
				o.logBuf.Add(issue.Identifier, formatBufLine("WARN", "worker: turn failed", []any{"detail", detail, "session_id", runLogID}))
			}
			// An advisory throttle (CORE-050) rides along so the retry
			// honours the vendor's delay; the reason stays TerminalFailed.
			o.sendExitWithLimit(ctx, issue, attempt, TerminalFailed, cause, limitForHost(result.LastLimit, workerHost))
			return
		}

		// A turn that produces no tokens means the agent has nothing more to do
		// (the session was already concluded). Break early for a clean exit.
		if result.InputTokens == 0 && result.OutputTokens == 0 {
			slog.Info("worker: 0-token turn — session concluded, exiting loop",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
			break
		}

		if o.queueSuccessfulTurnInputRequired(
			ctx,
			issue,
			attempt,
			result,
			backend,
			agentCommand,
			workerHost,
			profileName,
			activeBranchName,
			runLogID,
			claudeSessionID,
			startedAt,
			turn,
			cumulativeInput,
			cumulativeCached,
			cumulativeOutput,
		) {
			return
		}

		// Codex runs to completion in a single turn — "codex exec" produces
		// the full result and exits. Resuming a completed Codex session just
		// replays the same answer (burning tokens). Exit after the first
		// successful turn for Codex backends.
		//
		// InputRequired is handled above: a Codex turn that asks the user
		// something must enter the input_required state instead of being
		// treated as an ordinary successful single-turn completion.
		if backend == "codex" && !result.Failed {
			slog.Info("worker: codex turn completed — exiting loop (single-turn backend)",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
			break
		}

		// Detect repeated identical responses — a sign the session has
		// concluded but the agent is replaying its last answer on each
		// resume (common with Codex). Two consecutive identical result
		// texts trigger an early exit to avoid burning tokens.
		if result.ResultText != "" && result.ResultText == prevResultText {
			slog.Info("worker: duplicate result text detected — session concluded, exiting loop",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
			break
		}
		prevResultText = result.ResultText

		// Emit turn summary line (#3): turn N complete — Δin/Δout tokens, elapsed Xs.
		if o.logBuf != nil {
			elapsed := time.Since(turnStart)
			summary := fmt.Sprintf("turn %d complete — +%d in / +%d out tokens, %.1fs",
				turn, result.InputTokens, result.OutputTokens, elapsed.Seconds())
			o.logBuf.Add(issue.Identifier, formatBufLine("INFO", "worker: turn_summary", []any{"summary", summary, "session_id", runLogID}))
		}

		// Accumulate tokens from this turn before the end-of-turn update so the
		// dashboard always shows the true running total, not just the per-turn count.
		cumulativeInput += result.InputTokens
		cumulativeCached += result.CachedInputTokens
		cumulativeOutput += result.OutputTokens

		// Send non-blocking progress update so the dashboard shows live turn/token data.
		select {
		case o.events <- OrchestratorEvent{
			Type:    EventWorkerUpdate,
			IssueID: issue.ID,
			RunEntry: &RunEntry{
				TurnCount:    turn,
				LastMessage:  result.ResultText,
				TotalTokens:  cumulativeInput + cumulativeCached + cumulativeOutput,
				InputTokens:  cumulativeInput,
				OutputTokens: cumulativeOutput,
				SessionID:    runLogID,
				// CORE-091: the turn's session-cumulative cost, keyed by the
				// agent session so a resumed session is not counted twice.
				AgentSessionID: result.SessionID,
				CostUSD:        result.CostUSD,
			},
		}:
		default:
			metrics.EventDropped() // CORE-045
			// Event loop busy — skip this tick, next turn will send another update.
		}

		// Refresh tracker state to decide whether to continue
		refreshed, err := o.tracker.FetchIssueStatesByIDs(ctx, []string{issue.ID})
		if err != nil {
			slog.Warn("worker: state refresh failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn, "error", err)
			o.sendExit(ctx, issue, attempt, TerminalFailed, err)
			return
		}
		if len(refreshed) > 0 {
			savedBranch := issue.BranchName
			issue = refreshed[0]
			// FetchIssueStatesByIDs may not return the branch name (e.g. GitHub's
			// fetchSingleIssue doesn't scan comments). Preserve what we tracked.
			if issue.BranchName == nil {
				issue.BranchName = savedBranch
			}
		}

		o.cfgMu.RLock()
		activeStates := append([]string{}, o.cfg.Tracker.ActiveStates...)
		o.cfgMu.RUnlock()
		if !isActiveState(issue.State, State{ActiveStates: activeStates}) {
			break // issue left active states — clean exit
		}
	}

	// F3 — per-unit gate. When hooks.after_run_required is set, a failing
	// after_run hook on the final turn blocks TerminalSucceeded: the unit is
	// not done on the agent backend's clean exit alone. Checked before any
	// success side effects (PR push, comments, completion-state transition).
	if o.cfg.Hooks.AfterRunRequired && lastAfterRunErr != nil {
		slog.Warn("worker: after_run gate failed — unit does not complete",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", lastAfterRunErr)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR",
				fmt.Sprintf("worker: after_run gate failed (after_run_required): %v", lastAfterRunErr), runLogID))
		}
		o.sendExit(ctx, issue, attempt, TerminalFailed,
			fmt.Errorf("worker: after_run gate failed: %w", lastAfterRunErr))
		return
	}

	// If the agent created a PR during this run, comment its URL on the tracker
	// issue.  This runs before the session summary so the PR link is visible even
	// on trackers that truncate long comments.  Uses the same gh CLI check as the
	// pre-run guard (now the workspace is on the newly-created branch).
	if wsPath != "" && prCtx == nil && !automationRun {
		if prURL := workspace.FindOpenPRURL(ctx, wsPath); prURL != "" {
			detectedPRURL = prURL
			// Dedup: check if we already posted a PR comment for this URL.
			prComment := fmt.Sprintf("🔗 Pull request created: %s", prURL)
			alreadyPosted := false
			for _, c := range issue.Comments {
				if strings.Contains(c.Body, prURL) {
					alreadyPosted = true
					break
				}
			}
			if alreadyPosted {
				slog.Info("worker: PR comment already posted, skipping",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "pr_url", prURL)
			} else if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(prComment)); err != nil {
				slog.Warn("worker: create PR comment failed (ignored)",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
			} else {
				slog.Info("worker: PR link commented on issue",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "pr_url", prURL)
				if o.logBuf != nil {
					// parseLogLine detects "pr" events by substring-matching "pr_opened" in the
					// message text, not by the log level or a separate event-type field.
					o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: pr_opened url=%s", prURL), runLogID))
				}
				// Queue pr_opened after the worker exit event below. The
				// event loop must clear state.Running first or it will
				// reject the automation as already_running.
				openedPRURL = prURL
				openedPRBranch = activeBranchName
			}
		}
	}

	// Stacked PRs (#73): a pull request from a stacked worktree must target
	// the blocker's branch, or it shows the blocker's commits too. The agent
	// is told the base in its prompt; this makes it so whatever the agent did.
	if stackedOn != "" && detectedPRURL != "" && !automationRun {
		baseCtx, baseCancel := context.WithTimeout(context.Background(), postRunTimeout)
		o.retargetPRBase(baseCtx, issue.Identifier, detectedPRURL, stackedOn, "stacked on blocker branch")
		baseCancel()
	}

	// agent.pr_footer (#81): credit Itervox once on the PR this run produced.
	// Only a PR found on the worktree's own branch counts (prCtx == nil): a
	// PR that is merely linked from the issue may be a person's, and its
	// body is not Itervox's to edit. PRFooter is read-only after startup, so
	// no lock is taken.
	if o.cfg.Agent.PRFooter && prCtx == nil && detectedPRURL != "" && !automationRun {
		footerCtx, footerCancel := context.WithTimeout(context.Background(), postRunTimeout)
		o.addPRFooter(footerCtx, issue.Identifier, detectedPRURL)
		footerCancel()
	}

	// Build session summary once — reused for handoff synthesis, the PR
	// comment, and the tracker comment. V2-3: sessionCommentForRun returns ""
	// when the final output begins with [SILENT], suppressing every comment
	// surface while the log buffer keeps the audit record. Built here (before
	// the PR push and handoff synthesis) because the synthesis below uses it as
	// the handoff body; it depends only on the completed turn output, not on
	// anything computed in the PR blocks.
	sessionComment := sessionCommentForRun(allTextBlocks, issue.Identifier)

	// F2 — "update the shared state" is part of the definition of done. A
	// worker that exits clean without writing its handoff deliverable must
	// not reach TerminalSucceeded unmodified: synthesize the handoff from
	// the session summary (marked as synthesized) so the durable state
	// update is committed on the issue branch (best-effort; falls back to a
	// working-tree file). If synthesis itself fails, the unit fails — success
	// without a handoff is not success. Runs BEFORE the PR push below so the
	// synthesized (or agent-authored) handoff reaches the pushed branch.
	if wsPath != "" && ctx.Err() == nil {
		synthesized, synthErr := ensureHandoffOnSuccess(wsPath, runHandoffRelPath, sessionComment)
		if synthErr != nil {
			slog.Error("worker: handoff missing at success and synthesis failed — failing unit",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"handoff_path", runHandoffRelPath, "error", synthErr)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR",
					fmt.Sprintf("worker: handoff synthesis failed: %v", synthErr), runLogID))
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed,
				fmt.Errorf("worker: success without handoff and synthesis failed: %w", synthErr))
			return
		}
		if synthesized {
			slog.Info("worker: agent wrote no handoff — synthesized from session summary",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"handoff_path", runHandoffRelPath)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO",
					fmt.Sprintf("worker: handoff synthesized at %s (agent did not write one)", runHandoffRelPath), runLogID))
			}
		}

		// Durable shared state (F2/D3): committed on the issue branch so it
		// reaches the remote via the push below on the PR-continuation path
		// (prCtx != nil). On other paths the commit is local-only (best-effort;
		// lost if auto_clear removes the worktree before any later push).
		// Scoped add + pathspec-scoped commit — never sweeps unrelated agent
		// changes (even pre-staged ones). In a non-git
		// workspace `git add` fails and we skip the commit silently (previous
		// behavior — the file remains in the working tree, no Warn spam on
		// every success); other commit failures log.
		if staged, err := workspace.CommitPathOnly(ctx, wsPath, HandoffDirRelPath, "chore(itervox): record agent handoff"); staged && err != nil {
			slog.Warn("worker: handoff commit failed (file remains uncommitted)",
				"issue_identifier", issue.Identifier, "error", err)
		}
	}

	// Push the PR branch and post a summary comment on the existing PR (best-effort).
	// Use a fresh background-derived context with a timeout so that a cancellation
	// of the worker context (e.g. user pause) between the ctx.Err() guard and
	// command execution does not silently skip the post-run cleanup.
	if prCtx != nil && ctx.Err() == nil && !automationRun {
		postRunCtx, postRunCancel := context.WithTimeout(context.Background(), postRunTimeout)
		defer postRunCancel()
		// Push so the remote branch reflects the agent's changes.
		if wsPath != "" {
			pushCmd := gitexec.Command(postRunCtx, wsPath, "push", "origin", prCtx.Branch)
			if err := pushCmd.Run(); err != nil {
				slog.Warn("worker: git push failed (non-fatal)",
					"issue_identifier", issue.Identifier, "branch", prCtx.Branch, "error", err)
			}
		}

		if sessionComment != "" {
			// gh pr comment <url> uses the GitHub API directly and does not need a
			// working directory; omitting Dir avoids misleading readers.
			ghCommentCmd := exec.CommandContext(postRunCtx, "gh", "pr", "comment", prCtx.URL,
				"--body", sessionComment)
			ghCommentCmd.Env = gitexec.Environ()
			if err := ghCommentCmd.Run(); err != nil {
				slog.Warn("worker: gh pr comment failed (non-fatal)",
					"issue_identifier", issue.Identifier, "pr_url", prCtx.URL, "error", err)
			} else {
				slog.Info("worker: posted session summary to PR",
					"issue_identifier", issue.Identifier, "pr_url", prCtx.URL)
			}
		}
	}

	// Post one comprehensive comment covering the full session narration (best-effort).
	// Skip when there is an open PR: the summary was already posted as a PR comment
	// above, so posting it again on the tracker issue would create a duplicate (GO-R10-3).
	if sessionComment != "" && prCtx == nil && !automationRun {
		if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(sessionComment)); err != nil {
			slog.Warn("worker: create session comment failed (ignored)",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		}
	}

	// Move issue to completion_state (e.g. "In Review") so Itervox stops
	// re-dispatching it. Without this, issues stay in active_states after a
	// successful run and get picked up again on the next retry tick.
	// Up to 4 attempts total (1 immediate + 3 retries with 2s/4s/8s backoff) to guard against
	// transient API errors that would otherwise cause an infinite dispatch loop.
	// Skip if the worker context was cancelled (user paused/killed the issue) —
	// transitioning state on a cancelled run would wrongly move a paused issue.
	o.cfgMu.RLock()
	completionState := o.cfg.Tracker.CompletionState
	isReviewerRun := profileName != "" && (profileName == o.cfg.Agent.ReviewerProfile || containsString(o.cfg.Agent.ReviewerProfiles, profileName))
	o.cfgMu.RUnlock()

	// "Done needs evidence" (#80): a profile with require_evidence moves its
	// issue on only with proof. Without it the run ends input-required with
	// the reason and the issue stays where it is. Reviewer and automation
	// runs do not move the issue for themselves, so they are not gated.
	if required := profilesSnap[profileName].RequireEvidence; len(required) > 0 &&
		completionState != "" && ctx.Err() == nil && !automationRun && !isReviewerRun {
		evCtx, evCancel := context.WithTimeout(context.Background(), postRunTimeout)
		verdict := checkEvidence(evCtx, wsPath, evidenceRelPathFor(profileName), required, detectedPRURL, o.prChecksReader())
		evCancel()
		if !verdict.ok() {
			slog.Info("worker: holding issue for evidence (require_evidence)",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "missing", len(verdict.Missing))
			o.queueInputRequiredEntry(ctx, issue, attempt, runLogID, claudeSessionID, backend, agentCommand,
				workerHost, profileName, activeBranchName, evidenceHoldContext(completionState, verdict), "",
				buildInputRequiredExitRunEntry(issue, attempt, startedAt, turn, runLogID, workerHost, backend,
					cumulativeInput, cumulativeCached, cumulativeOutput, agent.TurnResult{}))
			return
		}
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", "worker: evidence checks passed", runLogID))
		}
	}

	if completionState != "" && ctx.Err() == nil && !automationRun {
		slog.Info("worker: transitioning to completion state",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier, "target_state", completionState)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: moving issue to %q in tracker", completionState), runLogID))
		}
		// Routed through o.writeSink(): under the default directWriteSink this
		// is the exact retry loop that used to live inline here (fresh
		// context.Background()-derived context per attempt, so a cancelled
		// worker ctx does not abort an in-flight API call; ctx.Done() is only
		// watched between attempts) — see write_sink.go. Under an
		// outbox-backed sink this is a single durable enqueue; the flusher
		// owns retries.
		// issue.State is the baseline reconciliation compares against. It
		// matters most on exactly this path: the session comment above is
		// enqueued for the same issue and is therefore the per-issue FIFO
		// head, so it always flushes first and bumps the tracker's UpdatedAt
		// without changing State. Without the baseline, reconciliation read
		// that as a human edit and dropped this transition — leaving the
		// issue in its active state and re-dispatching finished work.
		transitionErr := o.writeSink().UpdateIssueState(ctx, issue.ID, issue.Identifier, completionState, issue.State)
		if transitionErr != nil {
			slog.Error("worker: completion state transition failed after retries",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"target_state", completionState, "error", transitionErr)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: state transition to %q failed: %v — issue paused", completionState, transitionErr), runLogID))
			}
			// Pause the issue so it doesn't re-enter the dispatch loop and cause
			// an infinite retry cycle. The user can resume it manually.
			// Marked BEFORE the cancelled-IDs entry the event loop reads, so
			// the reason is available by the time the pause is recorded. Both
			// are needed: cancelled-IDs is what stops the dispatch loop,
			// transitionFailed is what says this pause is recoverable without
			// a human (#42-F).
			o.markTransitionFailed(issue.Identifier)
			o.userCancelledMu.Lock()
			o.userCancelledIDs[issue.Identifier] = struct{}{}
			o.userCancelledMu.Unlock()
		} else {
			slog.Info("worker: issue moved to completion state",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", completionState)
			o.RecordIssueStatusChange(IssueStatusChange{
				Identifier:  issue.Identifier,
				FromState:   issue.State,
				ToState:     completionState,
				Source:      StatusSourceWorkerLifecycle,
				ProfileName: profileName,
				Backend:     backend,
				WorkerHost:  workerHost,
			})
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: → %s", completionState), runLogID))
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: ✓ issue moved to %q", completionState), runLogID))
			}
		}
	}

	// Log a completion summary with token usage so it appears in the log file
	// and stderr (charmbracelet/log). This is the main human-visible record of
	// a successful run — includes turns, tokens, and elapsed time.
	elapsed := time.Since(startedAt)
	completionArgs := []any{
		"issue_identifier", issue.Identifier,
		"status", "succeeded",
		"turns", turn - 1,
		"input_tokens", cumulativeInput,
		"output_tokens", cumulativeOutput,
		"elapsed", elapsed.Round(time.Second).String(),
	}
	if detectedPRURL != "" {
		completionArgs = append(completionArgs, "pr_url", detectedPRURL)
	}
	o.logger().Info("worker: completed", completionArgs...)

	// Release the log buffer for this issue to free memory (after the completion
	// state transition so any transition errors are visible in the TUI log pane).
	if o.logBuf != nil {
		o.logBuf.Remove(issue.Identifier)
	}

	// Pass branchName so the auto-clear handler uses the actual worktree branch
	// (which may be prCtx.Branch on a PR-continuation run, not issue.BranchName).
	o.sendExitWithBranchThenPROpenedAutomations(ctx, issue, attempt, TerminalSucceeded, nil, activeBranchName, detectedPRURL, openedPRURL, openedPRBranch)
}

// hookLogFn returns a function suitable for workspace.RunHook's logFn parameter.
// Each hook output line is forwarded to the per-issue log buffer as an info entry
// tagged with sessionID so hook messages are attributable to the correct Timeline run.
// Returns nil when logBuf is not configured (no-op in RunHook).
func (o *Orchestrator) hookLogFn(identifier, sessionID string) func(string) {
	if o.logBuf == nil {
		return nil
	}
	return func(line string) {
		o.logBuf.Add(identifier, makeBufLineWithSession("INFO", "hook: "+line, sessionID))
	}
}

func (o *Orchestrator) queueSuccessfulTurnInputRequired(
	ctx context.Context,
	issue domain.Issue,
	attempt int,
	result agent.TurnResult,
	backend, agentCommand, workerHost, profileName, branchName, runLogID string,
	claudeSessionID *string,
	startedAt time.Time,
	turn, cumulativeInput, cumulativeCached, cumulativeOutput int,
) bool {
	if ctx.Err() != nil {
		return false
	}

	checkText := buildSuccessfulTurnInputCheckText(result)
	if checkText == "" {
		return false
	}

	if agent.IsSentinelInputRequired(checkText) {
		o.queueInputRequiredEntry(
			ctx,
			issue,
			attempt,
			runLogID,
			claudeSessionID,
			backend,
			agentCommand,
			workerHost,
			profileName,
			branchName,
			trimInputRequiredContext(checkText),
			"",
			buildInputRequiredExitRunEntry(
				issue,
				attempt,
				startedAt,
				turn,
				runLogID,
				workerHost,
				backend,
				cumulativeInput,
				cumulativeCached,
				cumulativeOutput,
				result,
			),
		)
		return true
	}

	decision := agent.DetectInputRequiredFallback(checkText)
	if !decision.NeedsInput {
		return false
	}

	inputContext := strings.TrimSpace(decision.Question)
	if inputContext == "" {
		inputContext = checkText
	}
	o.queueInputRequiredEntry(
		ctx,
		issue,
		attempt,
		runLogID,
		claudeSessionID,
		backend,
		agentCommand,
		workerHost,
		profileName,
		branchName,
		trimInputRequiredContext(inputContext),
		decision.Reason,
		buildInputRequiredExitRunEntry(
			issue,
			attempt,
			startedAt,
			turn,
			runLogID,
			workerHost,
			backend,
			cumulativeInput,
			cumulativeCached,
			cumulativeOutput,
			result,
		),
	)
	return true
}

// explicitInputRequiredDefault is the question posted when an explicit
// input request carries no usable text at all.
const explicitInputRequiredDefault = "Agent requires human input to continue"

// explicitInputRequiredContext builds the question for a turn whose result
// has InputRequired set (the text sentinel or a vendor error event). The
// question is posted to the tracker, and FailureText is unsafe there: it ends
// in the agent's raw stderr, which can carry env exports and prompt text
// (CORE-164). So the context is agent-written text first — the result text,
// then the last assistant message — and only then the vendor's own stream
// error message, with the stderr segment split off. queueInputRequiredEntry
// then passes it through the log redactor.
func explicitInputRequiredContext(result agent.TurnResult) string {
	text := strings.TrimSpace(result.ResultText)
	if text == "" {
		text = strings.TrimSpace(result.LastText)
	}
	if text == "" && result.FailureText != "" {
		vendorMsg, _ := agent.SplitFailureText(result.FailureText)
		text = strings.TrimSpace(vendorMsg)
	}
	if text == "" {
		return explicitInputRequiredDefault
	}
	return text
}

func buildSuccessfulTurnInputCheckText(result agent.TurnResult) string {
	var blocks string
	if n := len(result.AllTextBlocks); n > 0 {
		start := max(0, n-3)
		blocks = strings.Join(result.AllTextBlocks[start:], "\n")
	}

	resultText := strings.TrimSpace(result.ResultText)
	blocks = strings.TrimSpace(blocks)
	if blocks == "" {
		return resultText
	}
	if resultText == "" {
		return blocks
	}
	return blocks + "\n" + resultText
}

func trimInputRequiredContext(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 4000 {
		return text
	}
	return text[len(text)-4000:]
}

func (o *Orchestrator) queueInputRequiredEntry(
	ctx context.Context,
	issue domain.Issue,
	attempt int,
	runLogID string,
	claudeSessionID *string,
	backend, agentCommand, workerHost, profileName, branchName, inputContext, reason string,
	runEntry *RunEntry,
) {
	// The context becomes a tracker comment, the dashboard entry and an
	// automation prompt; none of those paths runs through the log handler's
	// redactor, so scrub it here, the one funnel every input-required exit
	// (explicit, sentinel, fallback) passes through (CORE-164).
	inputContext = logging.RedactString(inputContext)
	if reason == "" {
		slog.Info("worker: agent requires input — queuing for user input",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
	} else {
		slog.Info("worker: fallback detector queued issue for user input",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier,
			"reason", reason)
	}
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLineWithSession("WARN",
			fmt.Sprintf("worker: agent requires input — %s", inputContext), runLogID))
	}
	var sid string
	if claudeSessionID != nil {
		sid = *claudeSessionID
	}
	o.sendExitWithInputRequired(ctx, runEntry, &InputRequiredEntry{
		IssueID:     issue.ID,
		Identifier:  issue.Identifier,
		SessionID:   sid,
		Context:     inputContext,
		BranchName:  branchName,
		Backend:     backend,
		Command:     agentCommand,
		WorkerHost:  workerHost,
		ProfileName: profileName,
		QueuedAt:    time.Now(),
	})
}

func buildInputRequiredExitRunEntry(
	issue domain.Issue,
	attempt int,
	startedAt time.Time,
	turn int,
	runLogID, workerHost, backend string,
	cumulativeInput, cumulativeCached, cumulativeOutput int,
	result agent.TurnResult,
) *RunEntry {
	attemptCopy := attempt
	inputTokens := cumulativeInput + result.InputTokens
	outputTokens := cumulativeOutput + result.OutputTokens
	totalTokens := inputTokens + cumulativeCached + result.CachedInputTokens + outputTokens
	return &RunEntry{
		Issue:          issue,
		SessionID:      runLogID,
		WorkerHost:     workerHost,
		Backend:        backend,
		TerminalReason: TerminalInputRequired,
		InputTokens:    inputTokens,
		OutputTokens:   outputTokens,
		TotalTokens:    totalTokens,
		TurnCount:      turn,
		RetryAttempt:   &attemptCopy,
		StartedAt:      startedAt,
	}
}

// runAfterHook executes the after_run hook and returns its error. Callers on
// the best-effort path log and ignore the result; when hooks.after_run_required
// is set, the success path uses the final turn's result as a per-unit gate.
func (o *Orchestrator) runAfterHook(ctx context.Context, hook string, timeoutMs int, wsPath, issueID, identifier, sessionID string) error {
	if wsPath == "" {
		return nil
	}
	if err := workspace.RunHook(ctx, hook, wsPath, timeoutMs, o.hookLogFn(identifier, sessionID)); err != nil {
		slog.Warn("worker: after_run hook failed", "issue_id", issueID, "error", err)
		return err
	}
	return nil
}

func prepareAgentActionRuntime(tokens interface {
	IssueScoped(issueIdentifier, runSessionID string, allowedActions []string, createIssueState, moveIssueState string, ttl time.Duration) (string, error)
}, issueIdentifier, runLogID string, allowedActions []string, createIssueState, moveIssueState string, turnTimeoutMs int) (string, string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve current executable: %w", err)
	}
	shimDir, err := os.MkdirTemp("", "itervox-agent-actions-*")
	if err != nil {
		return "", "", fmt.Errorf("create shim dir: %w", err)
	}
	shimPath := filepath.Join(shimDir, "itervox")
	script := "#!/bin/sh\nexec " + agent.ShellQuote(exePath) + " \"$@\"\n"
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(shimDir)
		return "", "", fmt.Errorf("write shim: %w", err)
	}
	token, err := tokens.IssueScoped(issueIdentifier, runLogID, allowedActions, createIssueState, moveIssueState, agentActionTokenTTL(turnTimeoutMs))
	if err != nil {
		_ = os.RemoveAll(shimDir)
		return "", "", fmt.Errorf("issue action token: %w", err)
	}
	return shimDir, token, nil
}

func agentActionTokenTTL(turnTimeoutMs int) time.Duration {
	if turnTimeoutMs <= 0 {
		return time.Hour
	}
	return max(time.Duration(turnTimeoutMs)*time.Millisecond+5*time.Minute, 15*time.Minute)
}

func prependEnvToCommand(command string, env map[string]string) string {
	const backendHintPrefix = "@@itervox-backend="

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	trimmedCommand := strings.TrimSpace(command)
	hintToken := ""
	commandRemainder := trimmedCommand
	if strings.HasPrefix(trimmedCommand, backendHintPrefix) {
		if idx := strings.IndexAny(trimmedCommand, " \t"); idx >= 0 {
			hintToken = trimmedCommand[:idx]
			commandRemainder = strings.TrimLeft(trimmedCommand[idx:], " \t")
		} else {
			hintToken = trimmedCommand
			commandRemainder = ""
		}
	}

	var b strings.Builder
	if hintToken != "" {
		b.WriteString(hintToken)
		b.WriteByte(' ')
	}
	for _, key := range keys {
		if env[key] == "" {
			continue
		}
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(agent.ShellQuote(env[key]))
		b.WriteByte(' ')
	}
	b.WriteString(commandRemainder)
	return b.String()
}

func buildAgentActionContext(actions []string, createIssueState, moveIssueState string, remoteUnavailable bool) string {
	normalized := config.NormalizeAllowedActions(actions)
	if len(normalized) == 0 {
		return ""
	}
	if remoteUnavailable {
		return "Daemon-backed itervox actions are configured for this profile, but they are not available on remote SSH workers in v1."
	}
	lines := []string{
		"Itervox daemon actions are available for this profile. They only operate on the current issue.",
		"Use the `itervox action ...` CLI only when the task actually needs a tracker or resume action.",
	}
	for _, action := range normalized {
		switch action {
		case config.AgentActionComment:
			lines = append(lines, "- `itervox action comment --body \"...\"` posts a tracker comment on the current issue.")
		case config.AgentActionCreateIssue:
			if createIssueState != "" {
				lines = append(lines, "- `itervox action create-issue --title \"...\" --body \"...\"` creates a follow-up issue in state `"+createIssueState+"`.")
			} else {
				lines = append(lines, "- `itervox action create-issue --title \"...\" --body \"...\"` creates a follow-up issue using the profile's configured target state.")
			}
		case config.AgentActionMoveState:
			if moveIssueState != "" {
				lines = append(lines, "- `itervox action move-state --state \""+moveIssueState+"\"` moves the current issue to the automation-approved tracker state.")
			} else {
				lines = append(lines, "- `itervox action move-state --state \"...\"` moves the current issue to a new tracker state.")
			}
		case config.AgentActionProvideInput:
			lines = append(lines, "- `itervox action provide-input --message \"...\"` answers an input-required prompt and resumes the blocked run.")
		case config.AgentActionCommentPR:
			lines = append(lines, "- `itervox action comment-pr --summary \"...\" --findings findings.json` posts a structured review comment (summary + sorted findings) on the issue's open GitHub PR when one is known, otherwise on the tracker issue.")
		case config.AgentActionMergePR:
			lines = append(lines, "- `itervox action merge-pr --pr <number>` finalizes a PR through the daemon's guarded merge_pr action.")
		}
	}
	return strings.Join(lines, "\n")
}

func renderProfilePromptBlocks(profile config.AgentProfile, issue domain.Issue, attempt *int, extra map[string]any) []string {
	var blocks []string
	if profile.Soul != "" {
		blocks = append(blocks, prompt.RenderPromptOverlay(profile.Soul, issue, attempt, extra))
	}
	if profile.Instructions != "" {
		blocks = append(blocks, prompt.RenderPromptOverlay(profile.Instructions, issue, attempt, extra))
	}
	if len(blocks) == 0 && profile.Prompt != "" {
		blocks = append(blocks, prompt.RenderPromptOverlay(profile.Prompt, issue, attempt, extra))
	}
	return blocks
}

// generateRunID returns a short random ID that is assigned to a worker run
// before the agent subprocess starts, enabling all log entries — including
// early hook/worker messages — to be tagged with the same session ID.
func generateRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b)
}

// turnFailedLogStderrTail bounds the stderr carried by the WARN "worker: turn
// failed" line (CORE-167). The root cause is at the end of stderr; the full
// (redacted, 64 KiB-bounded) text is logged at DEBUG.
const turnFailedLogStderrTail = 2 << 10

// boundedTurnFailureLog renders cause for the WARN line: the agent-reported
// part whole, the stderr part cut to its last turnFailedLogStderrTail bytes,
// everything redacted.
func boundedTurnFailureLog(cause error) string {
	text := logging.RedactString(cause.Error())
	agentPart, stderr := agent.SplitFailureText(text)
	if len(stderr) <= turnFailedLogStderrTail {
		return text
	}
	cut := len(stderr) - turnFailedLogStderrTail
	for cut < len(stderr) && !utf8.RuneStart(stderr[cut]) {
		cut++
	}
	return agentPart + " | stderr: [...truncated...]" + stderr[cut:]
}

// turnFailureCause builds a failed turn's exit cause (CORE-029). The vendor
// FailureText is kept even when the runner returned an error, because the
// exhausted-retry rate-limit classifier only sees the exit cause's text; the
// runner error is wrapped so errors.Is(cause, context.Canceled) still routes
// orchestrator-driven cancellations to the claim-release branch. When the
// FailureText already contains the runner error's text (the agent-side
// fallback for a read error with no parsed diagnostic), it is not repeated.
func turnFailureCause(turn int, failureText string, runErr error) error {
	switch {
	case runErr == nil && failureText == "":
		return fmt.Errorf("turn %d: agent reported failure", turn)
	case runErr == nil:
		return fmt.Errorf("turn %d: %s", turn, failureText)
	case failureText == "":
		return runErr
	case strings.Contains(failureText, runErr.Error()):
		return &turnFailure{msg: fmt.Sprintf("turn %d: %s", turn, failureText), err: runErr}
	default:
		return fmt.Errorf("turn %d: %s: %w", turn, failureText, runErr)
	}
}

// turnFailure is an exit cause whose text already includes its wrapped
// error's text.
type turnFailure struct {
	msg string
	err error
}

func (e *turnFailure) Error() string { return e.msg }
func (e *turnFailure) Unwrap() error { return e.err }

func (o *Orchestrator) sendExit(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error) {
	o.sendExitWithBranch(ctx, issue, attempt, reason, err, "", "")
}

func (o *Orchestrator) sendExitWithBranch(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error, branchName string, prURL string) {
	o.deliverExit(ctx, issue, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		RunEntry: &RunEntry{
			Issue:          issue,
			BranchName:     branchName,
			PRURL:          prURL,
			TerminalReason: reason,
			RetryAttempt:   &attempt,
		},
		Error: err,
	})
}

// sendExitWithLimit sends a failed exit carrying the turn's vendor limit
// signal (CORE-051). The worker never mutates State: the event loop decides.
func (o *Orchestrator) sendExitWithLimit(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error, limit *agent.LimitSignal) {
	o.deliverExit(ctx, issue, OrchestratorEvent{
		Type:     EventWorkerExited,
		IssueID:  issue.ID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: reason, RetryAttempt: &attempt},
		Error:    err,
		Limit:    limit,
	})
}

// formatResetsAt renders a limit's reset time for logs ("" when unknown).
func formatResetsAt(limit *agent.LimitSignal) string {
	if limit == nil || limit.ResetsAt.IsZero() {
		return ""
	}
	return limit.ResetsAt.UTC().Format(time.RFC3339)
}

// deliverExit sends a worker exit event to the event loop.
func (o *Orchestrator) deliverExit(ctx context.Context, issue domain.Issue, ev OrchestratorEvent) {
	// If the worker context is already cancelled (e.g. user-triggered pause via
	// CancelIssue), the exit event must still reach the event loop so that
	// PausedIdentifiers is set correctly.  Fall back to a background-derived
	// context so we never drop the exit notification just because the worker's
	// own context was cancelled.
	sendCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(context.Background(), hookFallbackTimeout)
		defer cancel()
	}
	// M4-close D1: key off loopExited, not the Run ctx. After a forced stop
	// the loop keeps collecting exits (collectExitsAfterCancel) while its ctx
	// is already done. Nil before Run: blocks forever, the send wins.
	orchDone := o.loopExitedCh()
	select {
	case <-orchDone:
		// Checked first: once the loop is gone a buffered send would vanish.
		slog.Warn("worker: exit event dropped (orchestrator exited)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
		return
	default:
	}
	select {
	case o.events <- ev:
	case <-orchDone:
		slog.Warn("worker: exit event dropped (orchestrator exited)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
	case <-sendCtx.Done():
		slog.Warn("worker: exit event not delivered (orchestrator shutting down)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
	}
}

func (o *Orchestrator) sendExitWithBranchThenPROpenedAutomations(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error, branchName string, prURL string, openedPRURL string, openedPRBranch string) {
	o.sendExitWithBranch(ctx, issue, attempt, reason, err, branchName, prURL)
	// Dispatch when this completed run has a PR (any
	// detected URL counts), not only when the tracker-comment write succeeded:
	// `resolvedURL` below falls back to the detected prURL when openedPRURL is
	// empty, so a failed/skipped CreateComment never swallows the dispatch.
	//
	// WHY the gate below is STRICT (todolist6 codex-B4, gaps_11 G-18-B4):
	// pr_opened fires ONLY for TerminalSucceeded runs. The debated liberal
	// reading — fire on any detected PR URL regardless of terminal reason —
	// was rejected because a run that exits TerminalFailed or TerminalStalled
	// likely left the PR incomplete; auto-dispatching a reviewer (or any
	// pr_opened automation) against half-finished work wastes an agent slot
	// and produces misleading review feedback. TerminalInputRequired is also
	// excluded: the same worker will resume and re-reach this path, firing
	// once it actually succeeds (the PROpenedDispatched ledger dedups by
	// issue + PR URL + automation ID, so the eventual fire is not doubled).
	// Revisit only if operators explicitly confirm a liberal reading for
	// input_required/stalled PRs. (The only production call site today passes
	// TerminalSucceeded, so this gate also guards future call sites against
	// silently loosening that contract.)
	if err != nil || reason != TerminalSucceeded {
		return
	}
	resolvedURL := cmp.Or(openedPRURL, prURL)
	if resolvedURL == "" {
		return
	}
	baseBranch := ""
	if o.cfg != nil {
		baseBranch = o.cfg.Agent.BaseBranch
	}
	o.DispatchPROpenedAutomations(ctx, issue, resolvedURL, cmp.Or(openedPRBranch, branchName), baseBranch)
}

func (o *Orchestrator) sendExitWithInputRequired(ctx context.Context, runEntry *RunEntry, entry *InputRequiredEntry) {
	if runEntry == nil {
		return
	}
	issue := runEntry.Issue
	ev := OrchestratorEvent{
		Type:               EventWorkerExited,
		IssueID:            issue.ID,
		RunEntry:           runEntry,
		InputRequiredEntry: entry,
	}
	sendCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(context.Background(), hookFallbackTimeout)
		defer cancel()
	}
	orchDone := o.loopExitedCh() // M4-close D1: see deliverExit
	select {
	case o.events <- ev:
	case <-orchDone:
		slog.Warn("worker: input-required event dropped (orchestrator exited)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
	case <-sendCtx.Done():
		slog.Warn("worker: input-required event not delivered (orchestrator shutting down)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier)
	}
}
