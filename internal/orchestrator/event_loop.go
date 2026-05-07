package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// Run executes the orchestrator event loop until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.started.Store(true) // guard SetHistoryFile / SetHistoryKey against post-Run calls
	o.runCtx.Store(&ctx)
	o.loadHistoryFromDisk()
	state := NewState(o.cfg)
	state = o.loadPausedFromDisk(state)
	state = o.loadInputRequiredFromDisk(state)
	tick := time.NewTimer(0)
	defer tick.Stop()

	var loopErr error
	for {
		select {
		case <-ctx.Done():
			loopErr = ctx.Err()
		case <-tick.C:
			state = o.onTick(ctx, state)
			o.storeSnap(state)
			tick.Reset(time.Duration(state.PollIntervalMs) * time.Millisecond)
		case <-o.refresh:
			// Immediate re-poll triggered by the web dashboard refresh button.
			state = o.onTick(ctx, state)
			o.storeSnap(state)
			tick.Reset(time.Duration(state.PollIntervalMs) * time.Millisecond)
		case ev := <-o.events:
			state = o.handleEvent(ctx, state, ev)
			o.storeSnap(state)
		}
		if loopErr != nil {
			break
		}
	}
	o.autoClearWg.Wait()
	o.discardWg.Wait()
	return loopErr
}

func (o *Orchestrator) onTick(ctx context.Context, state State) State {
	now := time.Now()

	// Snapshot runtime-mutable cfg fields into the event-loop State so that
	// AvailableSlots and dispatch helpers read a stable, lock-free copy for
	// the entire tick (no need to hold cfgMu inside the hot dispatch path).
	o.cfgMu.RLock()
	state.MaxConcurrentAgents = o.cfg.Agent.MaxConcurrentAgents
	state.ActiveStates = append([]string{}, o.cfg.Tracker.ActiveStates...)
	state.TerminalStates = append([]string{}, o.cfg.Tracker.TerminalStates...)
	o.cfgMu.RUnlock()

	// 1. Fire any retries whose DueAt has passed.
	state = o.fireRetries(ctx, state, now)

	// 2. Stall detection and tracker-state reconciliation.
	state = ReconcileStalls(state, o.cfg, now, o.events, o.logBuf)
	state = ReconcileTrackerStates(ctx, state, o.tracker, o.events, o.logBuf)

	// 3. Fetch candidates and dispatch eligible issues.
	issues, err := o.tracker.FetchCandidateIssues(ctx)
	if err != nil {
		slog.Warn("orchestrator: fetch candidates failed", "error", err)
		return state
	}

	// Build the current active-identifier set for this tick. We compare it
	// against the previous tick's set in the auto-resume guard below, then
	// store it for the next tick.
	currentActive := make(map[string]struct{}, len(issues))
	for i := range issues {
		currentActive[issues[i].Identifier] = struct{}{}
	}

	// Auto-resume any paused issue that the tracker has moved back to an active
	// state (e.g. user manually set it back to "Todo"). A tracker-side state
	// change is treated as an implicit resume — clear the daemon-side pause so
	// the issue can be dispatched on this tick without requiring a manual resume
	// from the TUI.
	//
	// Guard: only auto-resume if the issue was NOT active on the previous tick.
	// If the issue was already active last tick it was active when the user
	// paused it (e.g. GitHub "todo" label stays throughout an agent run).
	// In that case we must not auto-resume — we wait until the issue leaves
	// active_states and then comes back.
	for i := range issues {
		issue := &issues[i]
		if _, paused := state.PausedIdentifiers[issue.Identifier]; paused {
			if _, wasActive := state.PrevActiveIdentifiers[issue.Identifier]; wasActive {
				// Was already active last tick — user paused it while it was
				// in active_states. Don't auto-resume.
				continue
			}
			delete(state.PausedIdentifiers, issue.Identifier)
			delete(state.PausedOpenPRs, issue.Identifier)
			// Keep PausedSessions so that auto-resume from a tracker state change
			// can also reuse the captured session ID. Dispatch will consume it.
			slog.Info("orchestrator: auto-resumed issue re-activated in tracker",
				"identifier", issue.Identifier, "state", issue.State)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLine("INFO",
					fmt.Sprintf("worker: issue moved to %q in tracker — auto-resumed", issue.State)))
			}
		}
	}

	state.PrevActiveIdentifiers = currentActive

	// Check for tracker comment replies to input-required issues.
	// If a user replied via Linear/GitHub, auto-resume the agent.
	state = o.checkTrackerReplies(ctx, state)

	slots := AvailableSlots(state)
	slog.Debug("orchestrator: tick",
		"fetched", len(issues),
		"running", len(state.Running),
		"slots", slots,
		"max_concurrent", state.MaxConcurrentAgents,
	)

	dispatched := 0
	for _, issue := range SortForDispatch(issues) {
		if AvailableSlots(state) <= 0 {
			slog.Debug("orchestrator: no slots available, stopping dispatch",
				"running", len(state.Running),
				"max_concurrent", state.MaxConcurrentAgents,
			)
			break
		}
		if !IsEligible(issue, state, o.cfg) {
			reason := IneligibleReason(issue, state, o.cfg)
			slog.Info("orchestrator: issue not eligible, skipping",
				"identifier", issue.Identifier,
				"state", issue.State,
				"reason", reason,
			)
			continue
		}
		// Guard: if the latest comment is an unresolved Itervox input-required
		// comment, restore the issue to InputRequiredIssues instead of dispatching
		// a fresh worker. This recovers from daemon restarts / state loss.
		if entry := o.recoverInputRequired(ctx, issue); entry != nil {
			state.InputRequiredIssues[issue.Identifier] = entry
			slog.Info("orchestrator: recovered input-required from tracker comment",
				"identifier", issue.Identifier)
			if o.logBuf != nil {
				o.logBuf.Add(issue.Identifier, makeBufLine("INFO",
					"worker: awaiting user input (recovered from tracker comment)"))
			}
			continue
		}
		state = o.dispatch(ctx, state, issue, 0)
		dispatched++
	}
	if dispatched > 0 || len(issues) > 0 {
		slog.Info("orchestrator: dispatch complete",
			"fetched", len(issues),
			"dispatched", dispatched,
			"running", len(state.Running),
			"slots_remaining", AvailableSlots(state),
		)
	}
	return state
}

// fireRetries processes all RetryAttempts whose DueAt <= now.
func (o *Orchestrator) fireRetries(ctx context.Context, state State, now time.Time) State {
	// Collect IDs first to avoid non-deterministic behaviour when ScheduleRetry
	// writes back to the same map key during iteration.
	ids := make([]string, 0, len(state.RetryAttempts))
	for id := range state.RetryAttempts {
		ids = append(ids, id)
	}
	for _, issueID := range ids {
		entry, ok := state.RetryAttempts[issueID]
		if !ok {
			continue // removed by an earlier iteration (e.g. CancelRetry)
		}
		// Skip retries for paused issues and release the claim.
		if _, paused := state.PausedIdentifiers[entry.Identifier]; paused {
			state = CancelRetry(state, issueID)
			continue
		}
		if now.Before(entry.DueAt) {
			continue
		}

		refreshed, err := o.tracker.FetchIssueStatesByIDs(ctx, []string{issueID})
		if err != nil {
			slog.Warn("retry: tracker fetch failed, rescheduling",
				"issue_id", issueID, "error", err)
			state = ScheduleRetry(state, issueID, entry.Attempt+1, entry.Identifier,
				"retry poll failed", now, BackoffMs(entry.Attempt+1, o.cfg.Agent.MaxRetryBackoffMs))
			continue
		}

		if len(refreshed) == 0 || !isActiveState(refreshed[0].State, state) {
			slog.Info("retry: issue no longer active, releasing claim", "issue_id", issueID)
			state = CancelRetry(state, issueID)
			continue
		}

		if AvailableSlots(state) <= 0 {
			slog.Debug("retry: no slots, rescheduling", "issue_id", issueID)
			state = ScheduleRetry(state, issueID, entry.Attempt, entry.Identifier,
				"no available orchestrator slots", now, 1000)
			continue
		}

		delete(state.RetryAttempts, issueID)
		state = o.dispatch(ctx, state, refreshed[0], entry.Attempt)
	}
	return state
}

// itervoxCommentPrefix is the prefix used by Itervox when posting input-required
// question comments. Used to identify and skip own comments when detecting user replies.
const itervoxCommentPrefix = "🤖 **Agent needs your input**"

// findLatestItervoxComment returns the index and CreatedAt of the most recently
// created comment whose body starts with itervoxCommentPrefix. Returns -1 and
// the zero time when no such comment exists or none have a CreatedAt timestamp.
// Iterating the full slice (rather than a reverse-index walk) makes the result
// independent of tracker comment ordering — Linear's GraphQL connection returns
// comments newest-first by default, GitHub returns oldest-first.
func findLatestItervoxComment(comments []domain.Comment) (int, time.Time) {
	latestIdx := -1
	var latestT time.Time
	for i := range comments {
		c := &comments[i]
		if !strings.HasPrefix(c.Body, itervoxCommentPrefix) {
			continue
		}
		if c.CreatedAt == nil {
			continue
		}
		if latestIdx < 0 || c.CreatedAt.After(latestT) {
			latestIdx = i
			latestT = *c.CreatedAt
		}
	}
	return latestIdx, latestT
}

// firstReplyAfter returns the body of the first non-itervox comment whose
// CreatedAt is strictly after t, or "" if none. Comments with nil CreatedAt
// are conservatively skipped — without a timestamp we cannot prove they were
// posted after the question, so we must not treat them as replies.
func firstReplyAfter(comments []domain.Comment, t time.Time) string {
	for i := range comments {
		c := &comments[i]
		if strings.HasPrefix(c.Body, itervoxCommentPrefix) {
			continue
		}
		if c.CreatedAt == nil {
			continue
		}
		if c.CreatedAt.After(t) {
			return c.Body
		}
	}
	return ""
}

// recoverInputRequired fetches the full issue detail (with comments) and checks
// if the latest comment is an unresolved Itervox input-required question.
// If so, returns an InputRequiredEntry reconstructed from the comment,
// preventing a wasteful fresh dispatch. Returns nil if no recovery is needed.
//
// Identifies the unanswered question by CreatedAt timestamp, not array
// position, so the recovery path is robust to tracker comment ordering
// (e.g. Linear returns comments newest-first; GitHub returns oldest-first).
func (o *Orchestrator) recoverInputRequired(ctx context.Context, issue domain.Issue) *InputRequiredEntry {
	detailed, err := o.tracker.FetchIssueDetail(ctx, issue.ID)
	if err != nil {
		slog.Warn("orchestrator: recoverInputRequired detail fetch failed",
			"identifier", issue.Identifier, "error", err)
		return nil
	}
	if len(detailed.Comments) == 0 {
		return nil
	}
	latestIdx, latestT := findLatestItervoxComment(detailed.Comments)
	if latestIdx < 0 {
		return nil // no Itervox question comment found
	}
	if firstReplyAfter(detailed.Comments, latestT) != "" {
		return nil // user already replied — safe to dispatch fresh
	}
	// Extract the question context from the comment body.
	body := detailed.Comments[latestIdx].Body
	questionCtx := strings.TrimPrefix(body, itervoxCommentPrefix)
	questionCtx = strings.TrimSpace(questionCtx)
	// Strip the trailing instruction line.
	if idx := strings.LastIndex(questionCtx, "\n---\n"); idx >= 0 {
		questionCtx = strings.TrimSpace(questionCtx[:idx])
	}
	return &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    questionCtx,
		QueuedAt:   time.Now(),
	}
}

// checkTrackerReplies polls tracker comments for each InputRequiredIssues entry.
// If a non-Itervox	 comment appeared after the agent's question, treat it as
// the user's reply and resume the agent — same as ProvideInput from the dashboard.
func (o *Orchestrator) checkTrackerReplies(ctx context.Context, state State) State {
	if len(state.InputRequiredIssues) == 0 {
		return state
	}
	for identifier, entry := range state.InputRequiredIssues {
		detailed, err := o.tracker.FetchIssueDetail(ctx, entry.IssueID)
		if err != nil {
			slog.Warn("orchestrator: tracker-reply check failed",
				"identifier", identifier, "error", err)
			continue
		}
		// Identify the unanswered question by CreatedAt timestamp, not array
		// position — Linear returns comments newest-first, GitHub oldest-first;
		// timestamp comparison is correct for both.
		_, latestT := findLatestItervoxComment(detailed.Comments)
		if latestT.IsZero() {
			continue // no question comment found — wait
		}
		userReply := firstReplyAfter(detailed.Comments, latestT)
		if userReply == "" {
			continue // no reply yet
		}
		slog.Info("orchestrator: tracker comment reply detected, resuming agent",
			"identifier", identifier, "reply_length", len(userReply))
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLine("INFO",
				"worker: user replied via tracker comment — resuming agent"))
		}
		delete(state.InputRequiredIssues, identifier)

		sid := entry.SessionID
		var sessionPtr *string
		if sid != "" {
			sessionPtr = &sid
		}
		workerCtx, workerCancel := context.WithCancel(ctx)
		state.Claimed[entry.IssueID] = struct{}{}
		state.Running[entry.IssueID] = &RunEntry{
			Issue:      *detailed,
			SessionID:  entry.SessionID,
			WorkerHost: entry.WorkerHost,
			Backend:    entry.Backend,
			StartedAt:  time.Now(),
		}
		o.workerCancelsMu.Lock()
		o.workerCancels[identifier] = workerCancel
		o.workerCancelsMu.Unlock()
		runnerCommand := entry.Command
		if entry.Backend != "" {
			runnerCommand = agent.CommandWithBackendHint(entry.Command, entry.Backend)
		}
		go o.runWorkerWithResume(workerCtx, *detailed, entry.WorkerHost, runnerCommand, entry.Backend, entry.ProfileName, sessionPtr, userReply)
		if o.OnStateChange != nil {
			o.OnStateChange()
		}
	}
	return state
}

func (o *Orchestrator) dispatch(ctx context.Context, state State, issue domain.Issue, attempt int) State {
	workerCtx, workerCancel := context.WithCancel(ctx)

	// Check if this issue has been queued for forced re-analysis (bypasses open-PR guard).
	skipPRCheck := false
	if _, forced := state.ForceReanalyze[issue.Identifier]; forced {
		skipPRCheck = true
		delete(state.ForceReanalyze, issue.Identifier)
		delete(state.PausedOpenPRs, issue.Identifier)
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLine("INFO", "worker: forced re-analysis requested"))
		}
	}

	// SSH host selection. Empty string = run locally.
	// SSHHosts and DispatchStrategy are now runtime-mutable so they must be
	// read under cfgMu, same as the other runtime-mutable cfg fields below.
	o.cfgMu.RLock()
	hosts := append([]string{}, o.cfg.Agent.SSHHosts...)
	dispatchStrategy := o.cfg.Agent.DispatchStrategy
	agentCommand := o.cfg.Agent.Command
	defaultBackend := o.cfg.Agent.Backend
	o.cfgMu.RUnlock()

	workerHost := ""
	if len(hosts) > 0 {
		if dispatchStrategy == "least-loaded" {
			workerHost = selectLeastLoadedHost(hosts, state.Running)
		} else {
			// Default: round-robin.
			workerHost = hosts[o.sshHostIdx%len(hosts)]
			o.sshHostIdx++
		}
	}
	runnerCommand := agentCommand
	backend := agent.BackendFromCommand(agentCommand)
	if defaultBackend != "" {
		backend = defaultBackend
		runnerCommand = agent.CommandWithBackendHint(agentCommand, defaultBackend)
	}
	o.issueProfilesMu.Lock()
	profileName := o.issueProfiles[issue.Identifier]
	o.issueProfilesMu.Unlock()
	if profileName != "" {
		o.cfgMu.RLock()
		profile, ok := o.cfg.Agent.Profiles[profileName]
		o.cfgMu.RUnlock()
		if !ok {
			slog.Warn("orchestrator: profile not found, using default",
				"identifier", issue.Identifier, "profile", profileName)
			profileName = "" // clear so the worker does not reference a missing profile
		} else {
			if profile.Command != "" {
				agentCommand = profile.Command
				runnerCommand = agentCommand
				backend = agent.BackendFromCommand(agentCommand)
			}
			if profile.Backend != "" {
				backend = profile.Backend
				runnerCommand = agent.CommandWithBackendHint(agentCommand, profile.Backend)
			}
			slog.Info("orchestrator: using profile",
				"identifier", issue.Identifier, "profile", profileName, "command", agentCommand, "backend", backend)
		}
	}

	// Per-issue backend override takes highest priority.
	o.issueBackendsMu.RLock()
	if issueBackend := o.issueBackends[issue.Identifier]; issueBackend != "" {
		backend = issueBackend
		runnerCommand = agent.CommandWithBackendHint(agentCommand, issueBackend)
		slog.Info("orchestrator: using per-issue backend override",
			"identifier", issue.Identifier, "backend", issueBackend)
	}
	o.issueBackendsMu.RUnlock()

	if o.DryRun {
		workerCancel()
		slog.Info("orchestrator: [DRY-RUN] would dispatch agent",
			"identifier", issue.Identifier, "issue_id", issue.ID,
			"command", agentCommand, "worker_host", workerHost, "backend", backend)
		state.Claimed[issue.ID] = struct{}{} // claim so it doesn't re-dispatch this tick
		return state
	}

	state.Claimed[issue.ID] = struct{}{}
	state.Running[issue.ID] = &RunEntry{
		Issue:        issue,
		WorkerHost:   workerHost,
		Backend:      backend,
		StartedAt:    time.Now(),
		RetryAttempt: &attempt,
		WorkerCancel: workerCancel,
	}

	// Register the cancel func in the concurrent-safe map so CancelIssue (called
	// from HTTP handler goroutines) can reach it without going through the snapshot,
	// which intentionally omits WorkerCancel to avoid unsafe cross-goroutine sharing.
	o.workerCancelsMu.Lock()
	o.workerCancels[issue.Identifier] = workerCancel
	o.workerCancelsMu.Unlock()

	if o.OnDispatch != nil {
		o.OnDispatch(issue.ID)
	}

	// Note: we intentionally do NOT clear the log buffer here. Every log entry is
	// tagged with a per-run runLogID (set by runWorker), so the client can filter
	// by session ID to isolate each run's logs. Clearing here would destroy the
	// log history of cancelled/terminated runs before users can inspect them.

	// If this issue is being resumed from manual pause and we captured a session
	// ID, pass it through so the agent continues the same session via --resume.
	resumeSessionID := ""
	if entry, ok := state.PausedSessions[issue.Identifier]; ok && entry != nil {
		resumeSessionID = entry.SessionID
		// Consume the entry — once dispatched, the session info is no longer
		// needed (the worker now owns the session via its RunEntry).
		delete(state.PausedSessions, issue.Identifier)
	}
	go o.runWorker(workerCtx, issue, attempt, workerHost, runnerCommand, backend, profileName, skipPRCheck, resumeSessionID)
	return state
}

// dispatchReviewerForIssue dispatches a reviewer worker for the given issue
// using the specified profile. The reviewer enters the regular worker queue with
// Kind="reviewer" and gets full retry/pause/resume support.
func (o *Orchestrator) dispatchReviewerForIssue(ctx context.Context, state *State, issue domain.Issue, profileName string, now time.Time) {
	// Resolve the reviewer profile's command and backend.
	o.cfgMu.RLock()
	profile, ok := o.cfg.Agent.Profiles[profileName]
	defaultCommand := o.cfg.Agent.Command
	defaultBackend := o.cfg.Agent.Backend
	o.cfgMu.RUnlock()

	if !ok {
		slog.Warn("orchestrator: reviewer profile not found, skipping auto-review",
			"issue_identifier", issue.Identifier, "profile", profileName)
		return
	}

	agentCommand := defaultCommand
	backend := agent.BackendFromCommand(agentCommand)
	if defaultBackend != "" {
		backend = defaultBackend
	}
	runnerCommand := agentCommand
	if profile.Command != "" {
		agentCommand = profile.Command
		runnerCommand = agentCommand
		backend = agent.BackendFromCommand(agentCommand)
	}
	if profile.Backend != "" {
		backend = profile.Backend
		runnerCommand = agent.CommandWithBackendHint(agentCommand, profile.Backend)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)

	if o.DryRun {
		workerCancel()
		slog.Info("orchestrator: [DRY-RUN] would dispatch reviewer",
			"identifier", issue.Identifier, "profile", profileName)
		return
	}

	state.Claimed[issue.ID] = struct{}{}
	attempt := 0
	state.Running[issue.ID] = &RunEntry{
		Issue:        issue,
		Kind:         "reviewer",
		WorkerHost:   "",
		Backend:      backend,
		StartedAt:    now,
		RetryAttempt: &attempt,
		WorkerCancel: workerCancel,
	}

	o.workerCancelsMu.Lock()
	o.workerCancels[issue.Identifier] = workerCancel
	o.workerCancelsMu.Unlock()

	slog.Info("orchestrator: dispatching reviewer",
		"issue_identifier", issue.Identifier, "profile", profileName, "backend", backend)

	// Set the issue's profile to the reviewer profile so runWorker uses the
	// reviewer's prompt (appended via the profile system).
	o.issueProfilesMu.Lock()
	o.issueProfiles[issue.Identifier] = profileName
	o.issueProfilesMu.Unlock()

	go o.runWorker(workerCtx, issue, attempt, "", runnerCommand, backend, profileName, false, "")
}

// transitionToWorking moves the issue to the configured working state (e.g. "In Progress").
// Called in the dispatch goroutine after claiming; errors are logged and ignored.
func (o *Orchestrator) transitionToWorking(ctx context.Context, issue domain.Issue) {
	target := o.cfg.Tracker.WorkingState
	if target == "" || strings.EqualFold(issue.State, target) {
		return
	}
	if err := o.tracker.UpdateIssueState(ctx, issue.ID, target); err != nil {
		slog.Warn("orchestrator: state transition failed (ignored)",
			"issue_id", issue.ID, "issue_identifier", issue.Identifier,
			"target_state", target, "error", err)
		return
	}
	slog.Info("orchestrator: issue transitioned",
		"issue_id", issue.ID, "issue_identifier", issue.Identifier,
		"from", issue.State, "to", target)
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLine("INFO", fmt.Sprintf("worker: → %s", target)))
	}
}

func (o *Orchestrator) handleEvent(ctx context.Context, state State, ev OrchestratorEvent) State {
	switch ev.Type {
	case EventWorkerUpdate:
		if entry, ok := state.Running[ev.IssueID]; ok && ev.RunEntry != nil {
			now := time.Now()
			entry.LastEventAt = &now
			if ev.RunEntry.TurnCount > 0 {
				entry.TurnCount = ev.RunEntry.TurnCount
			}
			if ev.RunEntry.TotalTokens > 0 {
				entry.TotalTokens = ev.RunEntry.TotalTokens
				entry.InputTokens = ev.RunEntry.InputTokens
				entry.OutputTokens = ev.RunEntry.OutputTokens
			}
			if ev.RunEntry.LastMessage != "" {
				entry.LastMessage = ev.RunEntry.LastMessage
			}
			if ev.RunEntry.SessionID != "" {
				entry.SessionID = ev.RunEntry.SessionID
			}
			if ev.RunEntry.AgentSessionID != "" {
				entry.AgentSessionID = ev.RunEntry.AgentSessionID
			}
		}

	case EventForceReanalyze:
		// Runs in the event loop goroutine — safe to mutate state maps directly.
		if _, isPaused := state.PausedIdentifiers[ev.Identifier]; isPaused {
			delete(state.PausedIdentifiers, ev.Identifier)
			// Force-reanalyze starts fresh — drop any captured session so dispatch
			// runs runWorker without --resume.
			delete(state.PausedSessions, ev.Identifier)
			state.ForceReanalyze[ev.Identifier] = struct{}{}
			// Persist immediately so a crash between ticks doesn't re-pause the issue.
			o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
			slog.Info("orchestrator: issue un-paused for forced re-analysis",
				"identifier", ev.Identifier)
			if o.OnStateChange != nil {
				o.OnStateChange()
			}
		}

	case EventResumeIssue:
		if _, isPaused := state.PausedIdentifiers[ev.Identifier]; isPaused {
			delete(state.PausedIdentifiers, ev.Identifier)
			o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
			slog.Info("orchestrator: issue resumed", "identifier", ev.Identifier)
			if o.OnStateChange != nil {
				o.OnStateChange()
			}
		}

	case EventTerminatePaused:
		if _, isPaused := state.PausedIdentifiers[ev.Identifier]; isPaused {
			delete(state.PausedIdentifiers, ev.Identifier)
			// Terminate discards the issue entirely; drop any captured session.
			delete(state.PausedSessions, ev.Identifier)
			o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
			slog.Info("orchestrator: paused issue terminated (claim released)", "identifier", ev.Identifier)
			// Move the issue back to Backlog (or first active state if no backlog
			// is configured) to remove the in-progress label and prevent it from
			// being immediately re-dispatched or left with a stale working label.
			// Skip if we don't have the issue UUID (legacy disk entry).
			if ev.IssueID != "" {
				state = o.asyncDiscardAndTransition(state, ev.IssueID, ev.Identifier)
			}
			if o.OnStateChange != nil {
				o.OnStateChange()
			}
		}

	case EventTerminateRunning:
		// Find the running worker and atomically mark it as user-terminated
		// before cancelling its context. Because this executes in the same
		// event-loop goroutine as EventWorkerExited, it is impossible for the
		// exit event to arrive and be processed between the state.Running check
		// and the userTerminatedIDs write — the TOCTOU window from GO-R5-3 is
		// gone. If the worker already exited naturally and EventWorkerExited was
		// queued before this event, state.Running will no longer contain the
		// entry and we take the no-op path below.
		for _, entry := range state.Running {
			if entry.Issue.Identifier == ev.Identifier && entry.WorkerCancel != nil {
				o.userTerminatedMu.Lock()
				o.userTerminatedIDs[ev.Identifier] = struct{}{}
				o.userTerminatedMu.Unlock()
				entry.WorkerCancel()
				slog.Info("orchestrator: running worker terminated by user",
					"identifier", ev.Identifier)
				return state
			}
		}
		// Worker already exited before this event was processed — natural exit
		// won the race; no flag to set, no cancel needed.
		slog.Debug("orchestrator: EventTerminateRunning — worker already exited",
			"identifier", ev.Identifier)

	case EventCancelRetry:
		// Remove the retry entry and claim, then pause the issue so it won't be
		// automatically re-dispatched until the user explicitly resumes it.
		state = CancelRetry(state, ev.IssueID)
		state.PausedIdentifiers[ev.Identifier] = ev.IssueID
		o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
		slog.Info("orchestrator: retry-queue issue cancelled and paused", "identifier", ev.Identifier)
		if o.OnStateChange != nil {
			o.OnStateChange()
		}

	case EventProvideInput:
		entry, ok := state.InputRequiredIssues[ev.Identifier]
		if !ok {
			slog.Warn("orchestrator: provide-input for unknown identifier", "identifier", ev.Identifier)
			return state
		}
		delete(state.InputRequiredIssues, ev.Identifier)
		slog.Info("orchestrator: user provided input, resuming agent",
			"identifier", ev.Identifier, "session_id", entry.SessionID)
		// Post the user's reply as a tracker comment so the conversation
		// is visible in Linear/GitHub alongside the agent's question.
		go func(issueID, ident, msg string) {
			postCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
			defer cancel()
			if err := o.tracker.CreateComment(postCtx, issueID, msg); err != nil {
				slog.Warn("orchestrator: failed to post user input as tracker comment",
					"identifier", ident, "error", err)
			}
		}(entry.IssueID, ev.Identifier, ev.Message)
		// Dispatch a resumed worker with the user's message as the prompt
		// and the saved session ID for --resume.
		sid := entry.SessionID
		var sessionPtr *string
		if sid != "" {
			sessionPtr = &sid
		}
		workerCtx, workerCancel := context.WithCancel(ctx)
		state.Claimed[entry.IssueID] = struct{}{}
		state.Running[entry.IssueID] = &RunEntry{
			Issue:      domain.Issue{ID: entry.IssueID, Identifier: entry.Identifier},
			SessionID:  entry.SessionID,
			WorkerHost: entry.WorkerHost,
			Backend:    entry.Backend,
			StartedAt:  time.Now(),
		}
		o.workerCancelsMu.Lock()
		o.workerCancels[entry.Identifier] = workerCancel
		o.workerCancelsMu.Unlock()
		// Build the command with backend hint if needed.
		runnerCommand := entry.Command
		if entry.Backend != "" {
			runnerCommand = agent.CommandWithBackendHint(entry.Command, entry.Backend)
		}
		issue := domain.Issue{ID: entry.IssueID, Identifier: entry.Identifier}
		go o.runWorkerWithResume(workerCtx, issue, entry.WorkerHost, runnerCommand, entry.Backend, entry.ProfileName, sessionPtr, ev.Message)
		if o.OnStateChange != nil {
			o.OnStateChange()
		}

	case EventDismissInput:
		entry, ok := state.InputRequiredIssues[ev.Identifier]
		if !ok {
			slog.Warn("orchestrator: dismiss-input for unknown identifier", "identifier", ev.Identifier)
			return state
		}
		delete(state.InputRequiredIssues, ev.Identifier)
		state.PausedIdentifiers[ev.Identifier] = entry.IssueID
		o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
		slog.Info("orchestrator: input-required issue dismissed and paused", "identifier", ev.Identifier)
		if o.OnStateChange != nil {
			o.OnStateChange()
		}

	case EventDiscardComplete:
		delete(state.DiscardingIdentifiers, ev.Identifier)
		slog.Info("orchestrator: discard complete, issue released", "identifier", ev.Identifier)

	case EventDispatchReviewer:
		// Manual reviewer dispatch via API. Fetch the issue and dispatch.
		if ev.ReviewerProfile == "" {
			slog.Warn("orchestrator: dispatch-reviewer event with empty profile", "identifier", ev.Identifier)
			return state
		}
		// Check if issue is already running.
		for _, entry := range state.Running {
			if entry.Issue.Identifier == ev.Identifier {
				slog.Warn("orchestrator: cannot dispatch reviewer — issue already running", "identifier", ev.Identifier)
				return state
			}
		}
		// Fetch the issue from tracker.
		o.cfgMu.RLock()
		allStates := append(append([]string{}, o.cfg.Tracker.ActiveStates...), o.cfg.Tracker.TerminalStates...)
		if o.cfg.Tracker.CompletionState != "" {
			allStates = append(allStates, o.cfg.Tracker.CompletionState)
		}
		o.cfgMu.RUnlock()
		issues, err := o.tracker.FetchIssuesByStates(ctx, allStates)
		if err != nil {
			slog.Warn("orchestrator: reviewer fetch failed", "identifier", ev.Identifier, "error", err)
			return state
		}
		var found *domain.Issue
		for i := range issues {
			if issues[i].Identifier == ev.Identifier {
				found = &issues[i]
				break
			}
		}
		if found == nil {
			slog.Warn("orchestrator: reviewer issue not found", "identifier", ev.Identifier)
			return state
		}
		o.dispatchReviewerForIssue(ctx, &state, *found, ev.ReviewerProfile, time.Now())

	case EventWorkerExited:
		// Capture the live entry before deletion so we can record history.
		liveEntry := state.Running[ev.IssueID]
		delete(state.Running, ev.IssueID)

		if ev.RunEntry == nil {
			// Emitted by ReconcileTrackerStates (not-found/non-active path);
			// claim and retry already managed by the reconcile function.
			return state
		}

		// Remove the cancel func from the concurrent-safe map now that the worker
		// has exited — CancelIssue will no longer find a cancel to invoke.
		o.workerCancelsMu.Lock()
		delete(o.workerCancels, ev.RunEntry.Issue.Identifier)
		o.workerCancelsMu.Unlock()

		now := time.Now()
		issue := ev.RunEntry.Issue
		attempt := 0
		if ev.RunEntry.RetryAttempt != nil {
			attempt = *ev.RunEntry.RetryAttempt
		}

		// Check if this exit was caused by a user kill (CancelIssue → pause)
		// or a hard terminate (TerminateIssue → release claim, no pause).
		o.userCancelledMu.Lock()
		_, wasCancelledByUser := o.userCancelledIDs[issue.Identifier]
		if wasCancelledByUser {
			delete(o.userCancelledIDs, issue.Identifier)
		}
		o.userCancelledMu.Unlock()

		o.userTerminatedMu.Lock()
		_, wasTerminatedByUser := o.userTerminatedIDs[issue.Identifier]
		if wasTerminatedByUser {
			delete(o.userTerminatedIDs, issue.Identifier)
		}
		o.userTerminatedMu.Unlock()

		if wasCancelledByUser {
			state.PausedIdentifiers[issue.Identifier] = issue.ID
			// Capture session info so resume can continue the same agent session
			// via --resume / `exec resume` instead of starting from scratch.
			// Only meaningful when the agent has actually established a session
			// (AgentSessionID is non-empty — set by the worker after the agent
			// reports its session ID on the first turn).
			if liveEntry != nil && liveEntry.AgentSessionID != "" {
				state.PausedSessions[issue.Identifier] = &PausedSessionInfo{
					IssueID:    issue.ID,
					SessionID:  liveEntry.AgentSessionID,
					WorkerHost: liveEntry.WorkerHost,
					Backend:    liveEntry.Backend,
				}
				// Resolve command + profile for resume. The live RunEntry doesn't
				// store these, so look them up from cfg + per-issue overrides the
				// same way dispatch() does.
				o.cfgMu.RLock()
				resumeCommand := o.cfg.Agent.Command
				profiles := o.cfg.Agent.Profiles
				o.cfgMu.RUnlock()
				profileName := state.IssueProfiles[issue.Identifier]
				if profileName != "" {
					if profile, ok := profiles[profileName]; ok && profile.Command != "" {
						resumeCommand = profile.Command
					}
				}
				state.PausedSessions[issue.Identifier].Command = resumeCommand
				state.PausedSessions[issue.Identifier].ProfileName = profileName
			}
			delete(state.Claimed, ev.IssueID)
			savedSession := ""
			if entry := state.PausedSessions[issue.Identifier]; entry != nil {
				savedSession = entry.SessionID
			}
			slog.Info("orchestrator: issue paused by user kill",
				"issue_id", ev.IssueID, "identifier", issue.Identifier,
				"session_id", savedSession)
			o.recordHistory(liveEntry, issue, now, "cancelled")
			return state
		}

		if wasTerminatedByUser {
			delete(state.Claimed, ev.IssueID)
			slog.Info("orchestrator: issue terminated by user (claim released)",
				"issue_id", ev.IssueID, "identifier", issue.Identifier)
			o.recordHistory(liveEntry, issue, now, "cancelled")

			// Move the issue to backlog so the working-state label is cleared and
			// the issue is not immediately re-dispatched on the next poll cycle.
			state = o.asyncDiscardAndTransition(state, ev.IssueID, issue.Identifier)
			return state
		}

		switch ev.RunEntry.TerminalReason {
		case TerminalCanceledByReconciliation:
			// Reconcile already released the claim; just log.
			delete(state.Claimed, ev.IssueID)
			slog.Info("orchestrator: worker canceled by reconciliation",
				"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
			// Not recorded — the issue will be re-dispatched.

		case TerminalSucceeded:
			// Release the claim — the issue completed successfully.
			// Do NOT schedule a retry; successful completions must not appear in
			// the retry queue and must not cause infinite re-dispatch loops.
			delete(state.Claimed, ev.IssueID)
			var turns, inTok, outTok int
			if liveEntry != nil {
				turns = liveEntry.TurnCount
				inTok = liveEntry.InputTokens
				outTok = liveEntry.OutputTokens
			}
			successArgs := []any{
				"issue_id", ev.IssueID, "issue_identifier", issue.Identifier,
				"turns", turns, "input_tokens", inTok, "output_tokens", outTok,
			}
			if ev.RunEntry != nil && ev.RunEntry.PRURL != "" {
				successArgs = append(successArgs, "pr_url", ev.RunEntry.PRURL)
			}
			slog.Info("orchestrator: worker succeeded, claim released", successArgs...)
			o.recordHistory(liveEntry, issue, now, "succeeded")
			// Auto-clear workspace if configured — removes the cloned directory
			// but leaves logs intact (they live under the logs dir, not here).
			o.cfgMu.RLock()
			autoClear := o.cfg.Workspace.AutoClearWorkspace
			o.cfgMu.RUnlock()
			if autoClear && o.workspace != nil {
				// Run in a goroutine — os.RemoveAll can be slow on large workspaces
				// and must not block the event loop (which would stall all workers).
				wm := o.workspace
				id := issue.Identifier
				// Use the actual worktree branch propagated via sendExitWithBranch.
				// PR-continuation runs use prCtx.Branch, which differs from
				// issue.BranchName; re-deriving the branch here would delete the
				// wrong branch and leak the PR branch permanently (GO-H2).
				bn := ev.RunEntry.BranchName
				if bn == "" {
					bn = workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
				}
				o.autoClearWg.Add(1)
				go func() {
					defer o.autoClearWg.Done()
					rmCtx, rmCancel := context.WithTimeout(context.Background(), hookFallbackTimeout)
					defer rmCancel()
					if err := wm.RemoveWorkspace(rmCtx, id, bn); err != nil {
						slog.Warn("orchestrator: auto-clear workspace failed",
							"identifier", id, "error", err)
					} else {
						slog.Info("orchestrator: workspace auto-cleared",
							"identifier", id)
					}
				}()
			}

			// Auto-review: if configured, dispatch a reviewer worker for this issue.
			// Only trigger when the completed worker was NOT itself a reviewer
			// (prevents infinite review loops).
			if liveEntry == nil || liveEntry.Kind != "reviewer" {
				o.cfgMu.RLock()
				reviewerProfile := o.cfg.Agent.ReviewerProfile
				autoReview := o.cfg.Agent.AutoReview
				o.cfgMu.RUnlock()
				if autoReview && reviewerProfile != "" {
					o.dispatchReviewerForIssue(ctx, &state, issue, reviewerProfile, now)
				}
			}

		case TerminalStalled:
			// ReconcileStalls already handled claim deletion and retry scheduling
			// inline. All we need to do here is record history so stall kills appear
			// in the run-history ring buffer. Use ev.RunEntry (not liveEntry, which
			// is nil because ReconcileStalls already deleted it from state.Running).
			o.recordHistory(ev.RunEntry, issue, now, "stalled")

		case TerminalInputRequired:
			delete(state.Claimed, ev.IssueID)
			entry := ev.InputRequiredEntry
			if entry == nil {
				break
			}
			// Post the agent's question as a tracker comment so it's visible
			// in Linear/GitHub. The dashboard shows a reply UI; user replies
			// are also posted as tracker comments before resuming the agent.
			commentText := fmt.Sprintf("🤖 **Agent needs your input**\n\n%s\n\n---\n_Reply via the Itervox dashboard to continue._", entry.Context)
			go func(issueID, ident string) {
				postCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
				defer cancel()
				if err := o.tracker.CreateComment(postCtx, issueID, commentText); err != nil {
					slog.Warn("orchestrator: failed to post input-required comment", "identifier", ident, "error", err)
				}
			}(entry.IssueID, issue.Identifier)
			state.InputRequiredIssues[issue.Identifier] = entry
			slog.Info("orchestrator: issue queued for human input",
				"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
			o.recordHistory(liveEntry, issue, now, "input_required")

		default: // TerminalFailed (and any other unhandled terminal reasons)
			// context.Canceled means the worker was stopped by the orchestrator
			// (stall timeout, reload, shutdown) — not a real failure. Release the
			// claim so the issue can be dispatched fresh on the next poll cycle.
			if ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
				delete(state.Claimed, ev.IssueID)
				slog.Info("orchestrator: worker context cancelled, claim released for re-dispatch",
					"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
				// Not recorded — the issue will be re-dispatched.
			} else {
				errMsg := ""
				if ev.Error != nil {
					errMsg = ev.Error.Error()
				}
				nextAttempt := attempt + 1
				maxRetries := o.cfg.Agent.MaxRetries
				if maxRetries > 0 && nextAttempt > maxRetries {
					// Max retries exhausted — move to failed state or pause.
					slog.Warn("worker: max retries exhausted",
						"issue_id", issue.ID, "issue_identifier", issue.Identifier,
						"attempts", attempt, "max_retries", maxRetries)
					if o.logBuf != nil {
						o.logBuf.Add(issue.Identifier, makeBufLine("ERROR",
							fmt.Sprintf("worker: max retries exhausted (%d/%d) — moving to failed state", attempt, maxRetries)))
					}
					o.commentMaxRetriesExhausted(issue, attempt, errMsg)
					failedState := o.cfg.Tracker.FailedState
					if failedState != "" {
						state = o.asyncDiscardAndTransitionTo(state, ev.IssueID, issue.Identifier, failedState)
					} else {
						state.PausedIdentifiers[issue.Identifier] = issue.ID
						o.savePausedToDisk(copyStringMap(state.PausedIdentifiers))
					}
					delete(state.Claimed, ev.IssueID)
					o.recordHistory(liveEntry, issue, now, "failed")
				} else {
					backoff := BackoffMs(nextAttempt, o.cfg.Agent.MaxRetryBackoffMs)
					state = ScheduleRetry(state, ev.IssueID, nextAttempt, issue.Identifier, errMsg, now, backoff)
					slog.Info("orchestrator: worker failed, retry scheduled",
						"issue_id", ev.IssueID, "issue_identifier", issue.Identifier,
						"attempt", nextAttempt, "backoff_ms", backoff,
						"turns", liveEntry.TurnCount, "input_tokens", liveEntry.InputTokens, "output_tokens", liveEntry.OutputTokens)
					o.recordHistory(liveEntry, issue, now, "failed")
				}
			}
		}
	}
	return state
}

// commentMaxRetriesExhausted posts a comment on the issue explaining that
// the maximum number of retries has been exhausted.
// Uses context.Background() intentionally: this notification must be delivered
// even during graceful shutdown so the issue owner knows why retries stopped.
func (o *Orchestrator) commentMaxRetriesExhausted(issue domain.Issue, attempts int, lastErr string) {
	comment := fmt.Sprintf(
		"Itervox: maximum retries exhausted (%d attempts). Last error:\n\n%s\n\nIssue has been moved to failed state. Re-open or move back to an active state to retry.",
		attempts, lastErr)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := o.tracker.CreateComment(ctx, issue.ID, comment); err != nil {
		slog.Warn("worker: failed to post max-retries comment", "issue_id", issue.ID, "error", err)
	}
}

// asyncDiscardAndTransitionTo is like asyncDiscardAndTransition but transitions
// the issue to a caller-specified target state instead of computing backlog/active.
// Returns the (potentially mutated) state. No-op when issueID or targetState is empty.
//
// Uses context.Background() intentionally: the tracker state transition must
// complete even during graceful shutdown to avoid leaving issues in an
// inconsistent state. The timeout ensures the goroutine is bounded.
func (o *Orchestrator) asyncDiscardAndTransitionTo(state State, issueID, identifier, targetState string) State {
	if issueID == "" || targetState == "" {
		return state
	}
	state.DiscardingIdentifiers[identifier] = struct{}{}
	o.discardWg.Add(1)
	go func() {
		defer o.discardWg.Done()
		updateCtx, updateCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := o.tracker.UpdateIssueState(updateCtx, issueID, targetState); err != nil {
			slog.Warn("orchestrator: failed to transition issue to failed state",
				"identifier", identifier, "target_state", targetState, "error", err)
		} else {
			slog.Info("orchestrator: issue transitioned to failed state",
				"identifier", identifier, "state", targetState)
		}
		updateCancel()
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer sendCancel()
		select {
		case o.events <- OrchestratorEvent{Type: EventDiscardComplete, Identifier: identifier}:
		case <-sendCtx.Done():
			slog.Warn("orchestrator: discard complete event lost, identifier may be stuck",
				"identifier", identifier)
		}
	}()
	return state
}

// asyncDiscardAndTransition snapshots backlog/active states under cfgMu,
// computes the target state, marks the identifier in DiscardingIdentifiers,
// and spawns a goroutine to update the tracker and send EventDiscardComplete.
// Returns the (potentially mutated) state. No-op when issueID is empty or no
// target state can be determined.
//
// Uses context.Background() intentionally: the tracker state transition must
// complete even during graceful shutdown to avoid leaving issues in an
// inconsistent state. The timeout ensures the goroutine is bounded.
func (o *Orchestrator) asyncDiscardAndTransition(state State, issueID, identifier string) State {
	if issueID == "" {
		return state
	}
	o.cfgMu.RLock()
	backlogStates := append([]string{}, o.cfg.Tracker.BacklogStates...)
	activeStates := append([]string{}, o.cfg.Tracker.ActiveStates...)
	o.cfgMu.RUnlock()

	var targetState string
	if len(backlogStates) > 0 {
		targetState = backlogStates[0]
	} else if len(activeStates) > 0 {
		targetState = activeStates[0]
	}
	if targetState == "" {
		return state
	}

	state.DiscardingIdentifiers[identifier] = struct{}{}
	o.discardWg.Add(1)
	go func() {
		defer o.discardWg.Done()
		updateCtx, updateCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := o.tracker.UpdateIssueState(updateCtx, issueID, targetState); err != nil {
			slog.Warn("orchestrator: failed to transition discarded issue",
				"identifier", identifier, "target_state", targetState, "error", err)
		} else {
			slog.Info("orchestrator: discarded issue transitioned",
				"identifier", identifier, "state", targetState)
		}
		updateCancel()
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer sendCancel()
		select {
		case o.events <- OrchestratorEvent{Type: EventDiscardComplete, Identifier: identifier}:
		case <-sendCtx.Done():
			slog.Warn("orchestrator: discard complete event lost, identifier may be stuck",
				"identifier", identifier)
		}
	}()
	return state
}

// recordHistory appends a completed run to the history ring buffer.
// liveEntry may be nil if the worker exited before the first update.
func (o *Orchestrator) recordHistory(liveEntry *RunEntry, issue domain.Issue, finishedAt time.Time, status string) {
	o.historyMu.RLock()
	key := o.historyKey
	o.historyMu.RUnlock()
	run := CompletedRun{
		Identifier:   issue.Identifier,
		Title:        issue.Title,
		FinishedAt:   finishedAt,
		Status:       status,
		ProjectKey:   key,
		AppSessionID: o.appSessionID,
	}
	if liveEntry != nil {
		run.StartedAt = liveEntry.StartedAt
		run.ElapsedMs = finishedAt.Sub(liveEntry.StartedAt).Milliseconds()
		run.TurnCount = liveEntry.TurnCount
		run.TotalTokens = liveEntry.TotalTokens
		run.InputTokens = liveEntry.InputTokens
		run.OutputTokens = liveEntry.OutputTokens
		run.WorkerHost = liveEntry.WorkerHost
		run.Backend = liveEntry.Backend
		run.Kind = liveEntry.Kind
		run.SessionID = liveEntry.SessionID
	} else {
		run.StartedAt = finishedAt
	}
	o.addCompletedRun(run)
}

// buildSubAgentContext generates a "## Available Sub-Agents" section that is
// appended to the rendered prompt when agent teams mode is active.
// activeProfile is excluded from the list so the agent doesn't try to spawn itself.
// Returns an empty string when there are no other profiles to list.
func buildSubAgentContext(profiles map[string]config.AgentProfile, activeProfile string, backend string) string {
	if len(profiles) == 0 {
		return ""
	}
	toolName := "Task"
	if backend == "codex" {
		toolName = "spawn_agent"
	}
	var b strings.Builder
	b.WriteString("## Available Sub-Agents\n\n")
	b.WriteString("You can spawn the following specialised sub-agents using the ")
	b.WriteString(toolName)
	b.WriteString(" tool:\n\n")
	for name, p := range profiles {
		if name == activeProfile {
			continue
		}
		if p.Prompt != "" {
			b.WriteString("- **" + name + "**: " + p.Prompt + "\n")
		} else {
			b.WriteString("- **" + name + "**\n")
		}
	}
	b.WriteString("\nUse the ")
	b.WriteString(toolName)
	b.WriteString(" tool with the sub-agent description when you need specialised help.")
	return b.String()
}

// StartupTerminalCleanup fetches terminal issues and removes their workspaces.
// Runs in the background with a 15-second timeout so it never blocks startup.
func StartupTerminalCleanup(ctx context.Context, tr tracker.Tracker, terminalStates []string, removeWorkspace func(string) error) {
	go func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		issues, err := tr.FetchIssuesByStates(cleanupCtx, terminalStates)
		if err != nil {
			slog.Warn("startup: terminal workspace cleanup fetch failed, continuing", "error", err)
			return
		}
		for _, issue := range issues {
			if err := removeWorkspace(issue.Identifier); err != nil {
				slog.Warn("startup: failed to remove workspace",
					"identifier", issue.Identifier, "error", err)
			}
		}
	}()
}
