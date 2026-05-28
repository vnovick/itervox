package orchestrator

import (
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

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/prdetector"
	"github.com/vnovick/itervox/internal/prompt"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// Worker-level timeout and retry constants.
const (
	// hookFallbackTimeout is used when no explicit hook timeout is configured.
	hookFallbackTimeout = 30 * time.Second
	// postRunTimeout bounds post-run actions: branch push, PR comment, state transition.
	postRunTimeout = 60 * time.Second
	// maxTransitionAttempts is how many times the worker retries moving the issue
	// to the completion state before giving up.
	maxTransitionAttempts = 4
)

// operatorReplyEnvelope is the orchestrator-controlled prompt block that tells
// every dispatched agent how the human operator will reply when the agent
// exits `input_required`. v0.2.0 todolist5 B3.
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
func (o *Orchestrator) runWorker(ctx context.Context, issue domain.Issue, attempt int, workerHost string, agentCommand string, backend string, profileName string, skipPRCheck bool, resume *ResumeContext, automation *AutomationDispatch) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("worker panic: %v", r)
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
		ws, err := o.workspace.EnsureWorkspace(ctx, issue.Identifier, branchName)
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
		renderedPrompt, err := prompt.Render(promptTemplate, issue, attemptPtr)
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

		// v0.2.0 todolist5 B3 — operator-reply envelope notice. Lives in the
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

		// Append the active profile's prompt (role context) whenever a named
		// profile is selected. The pre-removal `agent_mode == "teams"` gate
		// has been deleted (agent_mode is gone — see CHANGELOG); the
		// subagent roster context below also injects unconditionally so
		// multi-profile setups always tell the agent who its peers are.
		if profileName != "" {
			if profile, ok := profilesSnap[profileName]; ok {
				for _, block := range renderProfilePromptBlocks(profile, issue, attemptPtr) {
					if block != "" {
						renderedPrompt += "\n\n" + block
					}
				}
			}
		}
		if automation != nil && automation.Instructions != "" {
			renderedPrompt += "\n\n" + prompt.RenderPromptOverlay(
				automation.Instructions,
				issue,
				attemptPtr,
				automationTriggerBindings(automation),
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
		onProgress := func(partial agent.TurnResult) {
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
			}:
			default:
				slog.Debug("orchestrator: worker update event dropped (channel full)", "issue_id", issue.ID)
			}
		}
		turnStart := time.Now()
		logDir := ""
		if o.agentLogDir != "" {
			logDir = filepath.Join(o.agentLogDir, workspace.SanitizeKey(issue.Identifier))
		}
		result, runErr := o.runner.RunTurn(ctx, workerLog, onProgress, claudeSessionID, renderedPrompt, wsPath,
			agentCommand, workerHost, logDir, readTimeoutMs, turnTimeoutMs)

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
			}
		}

		// Accumulate all Claude text blocks for the final session comment.
		allTextBlocks = append(allTextBlocks, result.AllTextBlocks...)

		// after_run hook (best-effort, logged and ignored)
		o.runAfterHook(ctx, afterRunHook, hookTimeoutMs, wsPath, issue.ID, issue.Identifier, runLogID)

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

		if result.Failed {
			// A result error with no failure text and no tokens produced means the
			// claude CLI was asked to --resume a session that had already concluded.
			// Treat this as a clean session end rather than a real failure so the
			// issue does not land in the retry queue.
			if result.FailureText == "" && result.InputTokens == 0 && result.OutputTokens == 0 {
				slog.Info("worker: empty result error on 0-token turn — treating as clean session end",
					"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
				break
			}
			cause := runErr
			if cause == nil {
				msg := fmt.Sprintf("turn %d: agent reported failure", turn)
				if result.FailureText != "" {
					msg = fmt.Sprintf("turn %d: %s", turn, result.FailureText)
				}
				cause = errors.New(msg)
			}
			slog.Warn("worker: turn failed",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"turn", turn, "error", cause)
			// Emit full error detail to the per-issue log buffer (#7).
			if o.logBuf != nil {
				detail := cause.Error()
				if result.FailureText != "" && !strings.Contains(detail, result.FailureText) {
					detail = detail + " | " + result.FailureText
				}
				o.logBuf.Add(issue.Identifier, formatBufLine("WARN", "worker: turn failed", []any{"detail", detail, "session_id", runLogID}))
			}
			o.sendExit(ctx, issue, attempt, TerminalFailed, cause)
			return
		}

		// A turn that produces no tokens means the agent has nothing more to do
		// (the session was already concluded). Break early for a clean exit.
		if result.InputTokens == 0 && result.OutputTokens == 0 {
			slog.Info("worker: 0-token turn — session concluded, exiting loop",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn)
			break
		}

		// InputRequired means the agent needs human input to continue (e.g.
		// permission prompt, API key). Send TerminalInputRequired so the event
		// loop queues the issue for user input instead of retrying or completing.
		if result.InputRequired {
			inputContext := result.FailureText
			if inputContext == "" {
				inputContext = result.ResultText
			}
			if inputContext == "" {
				inputContext = "Agent requires human input to continue"
			}
			slog.Info("worker: agent requires input — queuing for user input",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier, "turn", turn,
				"context", inputContext)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("WARN",
					fmt.Sprintf("worker: agent requires input — %s", inputContext), runLogID))
			}
			var sid string
			if claudeSessionID != nil {
				sid = *claudeSessionID
			}
			o.sendExitWithInputRequired(ctx, buildInputRequiredExitRunEntry(
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
			), &InputRequiredEntry{
				IssueID:     issue.ID,
				Identifier:  issue.Identifier,
				SessionID:   sid,
				Context:     inputContext,
				BranchName:  activeBranchName,
				Backend:     backend,
				Command:     agentCommand,
				WorkerHost:  workerHost,
				ProfileName: profileName,
				QueuedAt:    time.Now(),
			})
			return
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
			},
		}:
		default:
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
			} else if _, err := o.tracker.CreateComment(ctx, issue.ID, tracker.MarkManagedComment(prComment)); err != nil {
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

	// Build session summary once — reused for both PR comment and tracker comment.
	sessionComment := formatSessionComment(allTextBlocks, issue.Identifier)

	// Push the PR branch and post a summary comment on the existing PR (best-effort).
	// Use a fresh background-derived context with a timeout so that a cancellation
	// of the worker context (e.g. user pause) between the ctx.Err() guard and
	// command execution does not silently skip the post-run cleanup.
	if prCtx != nil && ctx.Err() == nil && !automationRun {
		postRunCtx, postRunCancel := context.WithTimeout(context.Background(), postRunTimeout)
		defer postRunCancel()
		// Push so the remote branch reflects the agent's changes.
		if wsPath != "" {
			pushCmd := exec.CommandContext(postRunCtx, "git", "push", "origin", prCtx.Branch)
			pushCmd.Dir = wsPath
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
		if _, err := o.tracker.CreateComment(ctx, issue.ID, tracker.MarkManagedComment(sessionComment)); err != nil {
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
	o.cfgMu.RUnlock()
	if completionState != "" && ctx.Err() == nil && !automationRun {
		slog.Info("worker: transitioning to completion state",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier, "target_state", completionState)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", fmt.Sprintf("worker: moving issue to %q in tracker", completionState), runLogID))
		}
		// Use a fresh context for the tracker API calls so that a cancelled worker
		// context (user paused/terminated mid-retry) does not immediately fail the
		// request — the individual API call should complete or time out on its own.
		// The retry select still watches ctx.Done() to stop between attempts if the
		// user terminates while we're waiting for the next backoff window.
		transCtx, cancelTrans := context.WithTimeout(context.Background(), postRunTimeout)
		defer cancelTrans()
		var transitionErr error
	transitionLoop:
		for i := range maxTransitionAttempts {
			if i > 0 {
				delay := time.Duration(1<<uint(i-1)) * 2 * time.Second // 2s, 4s, 8s
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					transitionErr = ctx.Err()
					break transitionLoop
				}
			}
			if transitionErr = o.tracker.UpdateIssueState(transCtx, issue.ID, completionState); transitionErr == nil {
				break transitionLoop
			}
			slog.Warn("worker: completion state transition attempt failed, retrying",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"target_state", completionState, "attempt", i+1, "error", transitionErr)
		}
		if transitionErr != nil {
			slog.Error("worker: completion state transition failed after retries",
				"issue_id", issue.ID, "issue_identifier", issue.Identifier,
				"target_state", completionState, "error", transitionErr)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLineWithSession("ERROR", fmt.Sprintf("worker: state transition to %q failed: %v — issue paused", completionState, transitionErr), runLogID))
			}
			// Pause the issue so it doesn't re-enter the dispatch loop and cause
			// an infinite retry cycle. The user can resume it manually.
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
	slog.Info("worker: completed", completionArgs...)

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

func (o *Orchestrator) runAfterHook(ctx context.Context, hook string, timeoutMs int, wsPath, issueID, identifier, sessionID string) {
	if wsPath == "" {
		return
	}
	if err := workspace.RunHook(ctx, hook, wsPath, timeoutMs, o.hookLogFn(identifier, sessionID)); err != nil {
		slog.Warn("worker: after_run hook failed (ignored)", "issue_id", issueID, "error", err)
	}
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
	script := "#!/bin/sh\nexec " + shellQuote(exePath) + " \"$@\"\n"
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
		b.WriteString(shellQuote(env[key]))
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
		}
	}
	return strings.Join(lines, "\n")
}

func renderProfilePromptBlocks(profile config.AgentProfile, issue domain.Issue, attempt *int) []string {
	var blocks []string
	if profile.Soul != "" {
		blocks = append(blocks, prompt.RenderProfilePrompt(profile.Soul, issue, attempt))
	}
	if profile.Instructions != "" {
		blocks = append(blocks, prompt.RenderProfilePrompt(profile.Instructions, issue, attempt))
	}
	if len(blocks) == 0 && profile.Prompt != "" {
		blocks = append(blocks, prompt.RenderProfilePrompt(profile.Prompt, issue, attempt))
	}
	return blocks
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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

func (o *Orchestrator) sendExit(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error) {
	o.sendExitWithBranch(ctx, issue, attempt, reason, err, "", "")
}

func (o *Orchestrator) sendExitWithBranch(ctx context.Context, issue domain.Issue, attempt int, reason TerminalReason, err error, branchName string, prURL string) {
	ev := OrchestratorEvent{
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
	}
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
	// Nil-receive channel blocks forever — safe fallback when Run hasn't started.
	var orchDone <-chan struct{}
	if p := o.runCtx.Load(); p != nil {
		orchDone = (*p).Done()
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
	if reason != TerminalSucceeded || err != nil || openedPRURL == "" {
		return
	}
	baseBranch := ""
	if o.cfg != nil {
		baseBranch = o.cfg.Agent.BaseBranch
	}
	o.DispatchPROpenedAutomations(ctx, issue, openedPRURL, openedPRBranch, baseBranch)
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
	var orchDone <-chan struct{}
	if p := o.runCtx.Load(); p != nil {
		orchDone = (*p).Done()
	}
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
