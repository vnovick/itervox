package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
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
	state = o.loadAutoSwitchedFromDisk(state)
	state = o.loadInputRequiredFromDisk(state)
	state = o.loadAutomationQueueFromDisk(state)
	o.replayPersistedInputRequiredAutomations(ctx, &state, time.Now())
	tick := time.NewTimer(0)
	defer tick.Stop()

	var loopErr error
	for {
		// Prioritize cancellation over any pending tick/event/refresh. Without
		// this pre-check, Go's select is non-deterministic when ctx.Done() is
		// ready concurrently with another channel — a trailing event can run
		// AFTER cancel, mutate state (e.g. EventWorkerUpdate clearing
		// PendingInputResumes at line ~791 via progress flags), and storeSnap
		// persist the post-mutation state to disk. That breaks the
		// provide-input durability contract: a user's queued reply must
		// survive a cancel/restart cycle even if their worker was mid-turn.
		if err := ctx.Err(); err != nil {
			loopErr = err
			break
		}
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
	o.commentWg.Wait()
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

	// 1. Revert expired auto-switch overrides before retries or fresh
	// dispatch can reuse a stale fallback profile/backend.
	o.revertExpiredAutoSwitchesForTick(&state, now)

	// 2. Fire any retries whose DueAt has passed.
	state = o.fireRetries(ctx, state, now)

	// 3. Stall detection and tracker-state reconciliation. Pass the
	// orchestrator's cancelAndCleanupWorker so reconcile-driven cancels
	// release the workerCancels-map entry even if the EventWorkerExited
	// send drops under load (T-09).
	state = ReconcileStalls(state, o.cfg, now, o.events, o.cancelAndCleanupWorker, o.logBuf)
	state = ReconcileTrackerStates(ctx, state, o.tracker, o.events, o.cancelAndCleanupWorker, o.logBuf)

	// 4. Fetch candidates and dispatch eligible issues.
	issues, err := o.tracker.FetchCandidateIssues(ctx)
	if err != nil {
		slog.Warn("orchestrator: fetch candidates failed", "error", err)
		return state
	}
	for i := range issues {
		o.auditFetchedIssueDependenciesAndDispatch(ctx, &state, issues[i], now)
	}
	o.auditBlockersResolvedAutomationSources(ctx, &state, now)
	o.refreshKnownDependencyAudits(ctx, &state, now)

	// v0.2.0 audit P1-1 — build a lookup table over the candidate fetch so
	// drainAutomationQueue can reuse the snapshots instead of issuing a
	// per-entry tracker fetch. Indexed by both ID and Identifier so queue
	// entries can match either shape (Linear uses ID, GitHub uses Identifier).
	candidateIssues := make(map[string]domain.Issue, len(issues)*2)
	for i := range issues {
		if issues[i].ID != "" {
			candidateIssues[issues[i].ID] = issues[i]
		}
		if issues[i].Identifier != "" {
			candidateIssues[issues[i].Identifier] = issues[i]
		}
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

	// gaps_11 G-2 — capture the prior tick's poll set BEFORE overwriting it.
	// pruneAbsentTrackerIssues (below) needs the genuinely-previous set for
	// its two-tick grace window; reading state.PrevActiveIdentifiers after
	// this assignment would compare the current set against itself.
	prevActive := state.PrevActiveIdentifiers
	state.PrevActiveIdentifiers = currentActive

	// Check for tracker comment replies to input-required issues.
	// If a user replied via Linear/GitHub, auto-resume the agent.
	state = o.checkTrackerReplies(ctx, state)
	state = o.processPendingInputResumes(ctx, state, now)
	o.drainAutomationQueueWithCandidates(ctx, &state, now, candidateIssues)

	slots := AvailableSlots(state)
	slog.Debug("orchestrator: tick",
		"fetched", len(issues),
		"running", len(state.Running),
		"slots", slots,
		"max_concurrent", state.MaxConcurrentAgents,
	)

	dispatched := 0
	// P2 — when prefer_high_outdegree is enabled, sort with the outdegree
	// tiebreaker so foundation issues that unblock siblings dispatch first.
	o.cfgMu.RLock()
	preferOutdegree := o.cfg.Agent.PreferHighOutdegreeSort
	o.cfgMu.RUnlock()
	sorted := SortForDispatchWithOutdegree(issues, preferOutdegree)
	for _, issue := range sorted {
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
			// Recovery path: no live RunEntry — the previous worker is gone
			// (daemon restart / state loss). B1 self-reentry guard does not
			// apply here; pass nil so the dispatch proceeds normally.
			o.dispatchMatchingInputRequiredAutomations(ctx, &state, issue, entry, now, nil)
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
	// Gap §1.1 + §1.2 — opportunistic janitor for the rate-limit
	// switch-history + cooldown maps. Cheap: one pass per tick over
	// typically <100 entries, and short-circuits when the cap is 0.
	o.PruneRateLimitedMaps(now)

	// v0.2.0 audit P0-1 + P0-2 — bound long-lived runtime maps so a daemon
	// observing 10k+ issues over months does not pay snapshot/SSE bandwidth
	// for every issue it has ever seen. `currentActive` is already built
	// above as the live candidate set; the dependency-audit janitor uses
	// terminal-state + queue/Running membership instead.
	statusRemoved, prevRemoved := pruneIssueStatusHistory(&state, currentActive, now, issueStatusHistoryRetention)
	auditRemoved := pruneTerminalDependencyAudit(&state)

	// Sweep ledgers for identifiers whose last-observed
	// tracker state is terminal (agent moved issue to "Done" via direct API),
	// and also sweep ledgers for identifiers absent from
	// the tracker entirely (deleted / hard-trashed / archived issues).
	// Running workers are not cancelled — they complete naturally.
	terminalCounts := pruneTerminalRuntimeLedgers(&state, buildTerminalIdentifierSet(&state))
	absentCounts := pruneAbsentTrackerIssues(&state, currentActive, prevActive)
	ledger := LedgerJanitorCounts{
		InputRequired: terminalCounts.InputRequired + absentCounts.InputRequired,
		Retry:         terminalCounts.Retry + absentCounts.Retry,
		Queue:         terminalCounts.Queue + absentCounts.Queue,
		Paused:        terminalCounts.Paused + absentCounts.Paused,
		Profile:       absentCounts.Profile,
		Backend:       absentCounts.Backend,
	}

	if statusRemoved > 0 || prevRemoved > 0 || auditRemoved > 0 ||
		ledger.InputRequired > 0 || ledger.Retry > 0 || ledger.Queue > 0 ||
		ledger.Paused > 0 || ledger.Profile > 0 || ledger.Backend > 0 {
		slog.Debug("orchestrator: janitor pass",
			"status_history_removed", statusRemoved,
			"prev_states_removed", prevRemoved,
			"dependency_audit_removed", auditRemoved,
			"input_required_removed", ledger.InputRequired,
			"retry_removed", ledger.Retry,
			"automation_queue_removed", ledger.Queue,
			"paused_removed", ledger.Paused,
			"profile_removed", ledger.Profile,
			"backend_removed", ledger.Backend,
		)
	}
	return state
}

// buildTerminalIdentifierSet walks PrevIssueStates and returns identifiers
// whose last-observed state is terminal. The terminal predicate matches the
// completion-state path used elsewhere in the orchestrator (case-insensitive
// against CompletionState / FailedState / TerminalStates).
func buildTerminalIdentifierSet(state *State) map[string]struct{} {
	if len(state.PrevIssueStates) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(state.PrevIssueStates))
	for ident, lastState := range state.PrevIssueStates {
		if lastState == "" {
			continue
		}
		if isTerminalState(lastState, *state) {
			out[ident] = struct{}{}
		}
	}
	return out
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
		// ORCH-2 belt-and-braces: never dispatch a retry over a live worker.
		// The claim/running maps are authoritative; if the issue is already
		// Running (e.g. a late Canceled exit raced with this retry being
		// scheduled), skip without cancelling — the retry should fire later
		// once the running worker actually exits and clears state.Running.
		if _, running := state.Running[issueID]; running {
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
		o.auditFetchedIssueDependenciesAndDispatch(ctx, &state, refreshed[0], now)

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

func buildInputRequiredComment(entry *InputRequiredEntry) string {
	if entry == nil {
		return ""
	}
	return fmt.Sprintf("🤖 **Agent needs your input**\n\n%s\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._", entry.Context)
}

func buildPendingInputResumeEntry(entry *InputRequiredEntry, userMessage string) *PendingInputResumeEntry {
	if entry == nil {
		return nil
	}
	return &PendingInputResumeEntry{
		IssueID:            entry.IssueID,
		Identifier:         entry.Identifier,
		SessionID:          entry.SessionID,
		Context:            entry.Context,
		UserMessage:        userMessage,
		BranchName:         entry.BranchName,
		Backend:            entry.Backend,
		Command:            entry.Command,
		WorkerHost:         entry.WorkerHost,
		ProfileName:        entry.ProfileName,
		QuestionCommentID:  entry.QuestionCommentID,
		QuestionAuthorID:   entry.QuestionAuthorID,
		QuestionAuthorName: entry.QuestionAuthorName,
		QueuedAt:           time.Now(),
	}
}

func withRecordedQuestionComment(entry *InputRequiredEntry, comment *domain.Comment) *InputRequiredEntry {
	if entry == nil || comment == nil {
		return entry
	}
	cp := *entry
	cp.QuestionCommentID = comment.ID
	cp.QuestionAuthorID = comment.AuthorID
	cp.QuestionAuthorName = comment.AuthorName
	return &cp
}

func withRecordedPendingQuestionComment(entry *PendingInputResumeEntry, comment *domain.Comment) *PendingInputResumeEntry {
	if entry == nil || comment == nil {
		return entry
	}
	cp := *entry
	cp.QuestionCommentID = comment.ID
	cp.QuestionAuthorID = comment.AuthorID
	cp.QuestionAuthorName = comment.AuthorName
	return &cp
}

func inputRequiredEntryFromPending(entry *PendingInputResumeEntry) *InputRequiredEntry {
	if entry == nil {
		return nil
	}
	return &InputRequiredEntry{
		IssueID:            entry.IssueID,
		Identifier:         entry.Identifier,
		SessionID:          entry.SessionID,
		Context:            entry.Context,
		BranchName:         entry.BranchName,
		Backend:            entry.Backend,
		Command:            entry.Command,
		WorkerHost:         entry.WorkerHost,
		ProfileName:        entry.ProfileName,
		QuestionCommentID:  entry.QuestionCommentID,
		QuestionAuthorID:   entry.QuestionAuthorID,
		QuestionAuthorName: entry.QuestionAuthorName,
		QueuedAt:           entry.QueuedAt,
	}
}

func findLatestItervoxQuestionComment(comments []domain.Comment) (int, domain.Comment, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		if strings.HasPrefix(comments[i].Body, itervoxCommentPrefix) {
			return i, comments[i], true
		}
	}
	return -1, domain.Comment{}, false
}

func findTrackedQuestionComment(comments []domain.Comment, entry *InputRequiredEntry) (int, domain.Comment, bool) {
	if entry != nil && entry.QuestionCommentID != "" {
		for i, comment := range comments {
			if comment.ID == entry.QuestionCommentID {
				return i, comment, true
			}
		}
	}
	return findLatestItervoxQuestionComment(comments)
}

func sameCommentAuthor(a, b domain.Comment) bool {
	if a.AuthorID != "" && b.AuthorID != "" {
		return a.AuthorID == b.AuthorID
	}
	if a.AuthorName != "" && b.AuthorName != "" {
		return strings.EqualFold(a.AuthorName, b.AuthorName)
	}
	return false
}

func findReplyAfterQuestion(comments []domain.Comment, questionIdx int, question domain.Comment) (domain.Comment, bool) {
	for i := questionIdx + 1; i < len(comments); i++ {
		comment := comments[i]
		if strings.HasPrefix(comment.Body, itervoxCommentPrefix) {
			continue
		}
		if sameCommentAuthor(comment, question) {
			continue
		}
		return comment, true
	}
	return domain.Comment{}, false
}

// recoverInputRequired fetches the full issue detail (with comments) and checks
// if the latest comment is an unresolved Itervox input-required question.
// If so, returns an InputRequiredEntry reconstructed from the comment,
// preventing a wasteful fresh dispatch. This path is intentionally best-effort:
// session/backend/host continuity comes from the locally persisted
// InputRequiredIssues file, not tracker comment metadata.
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
	// Walk comments in reverse to find the last Itervox question.
	lastItervoxIdx, questionComment, ok := findLatestItervoxQuestionComment(detailed.Comments)
	if !ok {
		return nil // no Itervox question comment found
	}
	// Check if there's a non-Itervox comment after it (= user replied).
	if _, replied := findReplyAfterQuestion(detailed.Comments, lastItervoxIdx, questionComment); replied {
		return nil // user already replied — safe to dispatch fresh
	}
	// Extract the question context from the comment body.
	body := questionComment.Body
	questionCtx := strings.TrimPrefix(body, itervoxCommentPrefix)
	questionCtx = strings.TrimSpace(questionCtx)
	// Strip the trailing instruction line.
	if idx := strings.LastIndex(questionCtx, "\n---\n"); idx >= 0 {
		questionCtx = strings.TrimSpace(questionCtx[:idx])
	}
	// Rehydration path: local input_required.json state was lost (daemon
	// restart + missing file, stale cache, or new installation), but the
	// tracker still carries the Itervox question comment. We can reconstruct
	// identity and context from the tracker, but NOT the agent session ID,
	// backend, command, profile, or worker host — those were only ever in
	// worker-local state. On resume, hasResumeSession will be false, so the
	// worker will take the fresh-dispatch-with-context branch rather than
	// `claude --resume <sid>`. Users should know this has happened because
	// their "resume the exact paused session" expectation is being
	// downgraded to "start fresh with the question + your reply as context".
	slog.Warn("orchestrator: rehydrating input-required entry from tracker — session context lost, resume will start a fresh agent session",
		"identifier", issue.Identifier,
		"question_comment_id", questionComment.ID,
	)
	entry := &InputRequiredEntry{
		IssueID:            issue.ID,
		Identifier:         issue.Identifier,
		Context:            questionCtx,
		BranchName:         branchNameValue(issue.BranchName),
		QuestionCommentID:  questionComment.ID,
		QuestionAuthorID:   questionComment.AuthorID,
		QuestionAuthorName: questionComment.AuthorName,
		QueuedAt:           time.Now(),
	}
	return entry
}

func branchNameValue(branchName *string) string {
	if branchName == nil {
		return ""
	}
	return *branchName
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
		if detailed != nil {
			o.auditFetchedIssueDependenciesAndDispatch(ctx, &state, *detailed, time.Now())
		}
		questionIdx, questionComment, ok := findTrackedQuestionComment(detailed.Comments, entry)
		if !ok {
			continue // no question comment found — wait
		}
		replyComment, replied := findReplyAfterQuestion(detailed.Comments, questionIdx, questionComment)
		if !replied || strings.TrimSpace(replyComment.Body) == "" {
			continue // no reply yet
		}
		slog.Info("orchestrator: tracker comment reply detected, queuing pending resume",
			"identifier", identifier, "reply_length", len(replyComment.Body))
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLine("INFO",
				"worker: user replied via tracker comment — awaiting resumed worker"))
		}
		delete(state.InputRequiredIssues, identifier)
		state.PendingInputResumes[identifier] = buildPendingInputResumeEntry(entry, replyComment.Body)
	}
	return state
}

func (o *Orchestrator) processPendingInputResumes(ctx context.Context, state State, now time.Time) State {
	if len(state.PendingInputResumes) == 0 || AvailableSlots(state) <= 0 {
		return state
	}

	identifiers := make([]string, 0, len(state.PendingInputResumes))
	for identifier := range state.PendingInputResumes {
		identifiers = append(identifiers, identifier)
	}
	slices.Sort(identifiers)

	for _, identifier := range identifiers {
		if AvailableSlots(state) <= 0 {
			break
		}
		entry := state.PendingInputResumes[identifier]
		if entry == nil {
			delete(state.PendingInputResumes, identifier)
			continue
		}
		if _, running := state.Running[entry.IssueID]; running {
			continue
		}
		if _, claimed := state.Claimed[entry.IssueID]; claimed {
			continue
		}
		if _, paused := state.PausedIdentifiers[identifier]; paused {
			delete(state.PendingInputResumes, identifier)
			slog.Info("orchestrator: dropping pending input resume for paused issue",
				"identifier", identifier)
			continue
		}
		if _, discarding := state.DiscardingIdentifiers[identifier]; discarding {
			delete(state.PendingInputResumes, identifier)
			slog.Info("orchestrator: dropping pending input resume for discarding issue",
				"identifier", identifier)
			continue
		}

		detailed, err := o.tracker.FetchIssueDetail(ctx, entry.IssueID)
		if err != nil {
			slog.Warn("orchestrator: pending input resume detail fetch failed",
				"identifier", identifier, "error", err)
			continue
		}
		if detailed == nil {
			continue
		}
		o.auditFetchedIssueDependenciesAndDispatch(ctx, &state, *detailed, now)
		if isTerminalState(detailed.State, state) {
			delete(state.PendingInputResumes, identifier)
			slog.Info("orchestrator: dropping pending input resume for terminal issue",
				"identifier", identifier, "state", detailed.State)
			continue
		}
		if !isActiveState(detailed.State, state) {
			delete(state.PendingInputResumes, identifier)
			slog.Info("orchestrator: dropping pending input resume for non-active issue",
				"identifier", identifier, "state", detailed.State)
			continue
		}

		resumeIssue := *detailed
		if resumeIssue.BranchName == nil && entry.BranchName != "" {
			branchName := entry.BranchName
			resumeIssue.BranchName = &branchName
		}
		workerCtx, workerCancel := context.WithCancel(ctx)
		state.Claimed[entry.IssueID] = struct{}{}
		state.Running[entry.IssueID] = &RunEntry{
			Issue:              resumeIssue,
			AgentSessionID:     entry.SessionID,
			WorkerHost:         entry.WorkerHost,
			Backend:            entry.Backend,
			ProfileName:        entry.ProfileName,
			PendingInputResume: true,
			BranchName:         branchNameValue(resumeIssue.BranchName),
			StartedAt:          now,
		}
		o.workerCancelsMu.Lock()
		o.workerCancels[identifier] = workerCancel
		o.workerCancelsMu.Unlock()
		runnerCommand := resolveResumeCommand(inputRequiredEntryFromPending(entry), o.cfg, &o.cfgMu)
		go o.runWorker(workerCtx, resumeIssue, 0, entry.WorkerHost, runnerCommand, entry.Backend, entry.ProfileName, false, &ResumeContext{
			SessionID:    entry.SessionID,
			UserMessage:  entry.UserMessage,
			InputContext: entry.Context,
		}, nil)
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

	workerHost := o.selectWorkerHost(hosts, dispatchStrategy, state)

	// Resolve the issue's profile (clearing it if not found / disabled), then
	// compute the effective (cmd, runnerCmd, backend) via the shared helper.
	// The same logic powers reviewer dispatch — see resolveBackendForIssue.
	profileName := o.issueProfileForDispatch(state, issue.Identifier)
	var profilePtr *config.AgentProfile
	if profileName != "" {
		o.cfgMu.RLock()
		profile, ok := o.cfg.Agent.Profiles[profileName]
		o.cfgMu.RUnlock()
		switch {
		case !ok:
			slog.Warn("orchestrator: profile not found, using default",
				"identifier", issue.Identifier, "profile", profileName)
			profileName = "" // worker will not reference a missing profile
		case !config.ProfileEnabled(profile):
			slog.Warn("orchestrator: profile disabled, using default",
				"identifier", issue.Identifier, "profile", profileName)
			profileName = ""
		default:
			profilePtr = &profile
		}
	}
	issueBackend := o.issueBackendForDispatch(state, issue.Identifier)

	agentCommand, runnerCommand, backend := resolveBackendForIssue(
		agentCommand, defaultBackend, profilePtr, issueBackend,
	)
	if profilePtr != nil {
		slog.Info("orchestrator: using profile",
			"identifier", issue.Identifier, "profile", profileName, "command", agentCommand, "backend", backend)
	}
	if issueBackend != "" {
		slog.Info("orchestrator: using per-issue backend override",
			"identifier", issue.Identifier, "backend", issueBackend)
	}

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
		ProfileName:  profileName,
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
	var resumeCtx *ResumeContext
	if entry, ok := state.PausedSessions[issue.Identifier]; ok && entry != nil {
		resumeCtx = &ResumeContext{SessionID: entry.SessionID}
		// Consume the entry — once dispatched, the session info is no longer
		// needed (the worker now owns the session via its RunEntry).
		delete(state.PausedSessions, issue.Identifier)
	}
	go o.runWorker(workerCtx, issue, attempt, workerHost, runnerCommand, backend, profileName, skipPRCheck, resumeCtx, nil)
	return state
}

func (o *Orchestrator) issueProfileForDispatch(state State, identifier string) string {
	profileName := state.IssueProfiles[identifier]
	o.issueProfilesMu.RLock()
	if override, ok := o.issueProfiles[identifier]; ok {
		profileName = override
	}
	o.issueProfilesMu.RUnlock()
	return profileName
}

func (o *Orchestrator) issueBackendForDispatch(state State, identifier string) string {
	backend := state.IssueBackends[identifier]
	o.issueBackendsMu.RLock()
	if override, ok := o.issueBackends[identifier]; ok {
		backend = override
	}
	o.issueBackendsMu.RUnlock()
	return backend
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
	hosts := append([]string{}, o.cfg.Agent.SSHHosts...)
	dispatchStrategy := o.cfg.Agent.DispatchStrategy
	o.cfgMu.RUnlock()

	if !ok {
		slog.Warn("orchestrator: reviewer profile not found, skipping auto-review",
			"issue_identifier", issue.Identifier, "profile", profileName)
		return
	}
	if !config.ProfileEnabled(profile) {
		slog.Warn("orchestrator: reviewer profile disabled, skipping auto-review",
			"issue_identifier", issue.Identifier, "profile", profileName)
		return
	}

	o.issueBackendsMu.RLock()
	issueBackend := o.issueBackends[issue.Identifier]
	o.issueBackendsMu.RUnlock()

	_, runnerCommand, backend := resolveBackendForIssue(
		defaultCommand, defaultBackend, &profile, issueBackend,
	)
	if issueBackend != "" {
		slog.Info("orchestrator: using per-issue backend override for reviewer",
			"identifier", issue.Identifier, "backend", issueBackend)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	workerHost := o.selectWorkerHost(hosts, dispatchStrategy, *state)

	if o.DryRun {
		workerCancel()
		slog.Info("orchestrator: [DRY-RUN] would dispatch reviewer",
			"identifier", issue.Identifier, "profile", profileName, "worker_host", workerHost)
		return
	}

	state.Claimed[issue.ID] = struct{}{}
	attempt := 0
	state.Running[issue.ID] = &RunEntry{
		Issue:        issue,
		Kind:         "reviewer",
		WorkerHost:   workerHost,
		Backend:      backend,
		ProfileName:  profileName,
		StartedAt:    now,
		RetryAttempt: &attempt,
		WorkerCancel: workerCancel,
	}

	o.workerCancelsMu.Lock()
	o.workerCancels[issue.Identifier] = workerCancel
	o.workerCancelsMu.Unlock()

	slog.Info("orchestrator: dispatching reviewer",
		"issue_identifier", issue.Identifier, "profile", profileName, "backend", backend, "worker_host", workerHost)

	// Set the issue's profile to the reviewer profile so runWorker uses the
	// reviewer's prompt (appended via the profile system). Mark this entry
	// as reviewer-injected so TerminalSucceeded knows to clear it without
	// touching user-set overrides (T-21).
	o.issueProfilesMu.Lock()
	o.issueProfiles[issue.Identifier] = profileName
	o.reviewerInjectedProfiles[issue.Identifier] = struct{}{}
	o.issueProfilesMu.Unlock()

	go o.runWorker(workerCtx, issue, attempt, workerHost, runnerCommand, backend, profileName, false, nil, nil)
}

func (o *Orchestrator) selectWorkerHost(hosts []string, dispatchStrategy string, state State) string {
	if len(hosts) == 0 {
		return ""
	}
	if dispatchStrategy == "least-loaded" {
		return selectLeastLoadedHost(hosts, state.Running)
	}
	// Default: round-robin.
	workerHost := hosts[o.sshHostIdx%len(hosts)]
	o.sshHostIdx++
	return workerHost
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
	o.RecordIssueStatusChange(IssueStatusChange{
		Identifier: issue.Identifier,
		FromState:  issue.State,
		ToState:    target,
		Source:     StatusSourceWorkerLifecycle,
	})
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLine("INFO", fmt.Sprintf("worker: → %s", target)))
	}
}

func (o *Orchestrator) handleEvent(ctx context.Context, state State, ev OrchestratorEvent) (ret State) {
	drainAfter := shouldDrainAutomationQueueAfterEvent(ev.Type)
	defer func() {
		if drainAfter {
			o.drainAutomationQueue(ctx, &ret, time.Now())
		}
	}()
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
			if entry.PendingInputResume && (ev.RunEntry.TurnCount > 0 || ev.RunEntry.TotalTokens > 0 || ev.RunEntry.LastMessage != "") {
				delete(state.PendingInputResumes, entry.Issue.Identifier)
				entry.PendingInputResume = false
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
			o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
			slog.Info("orchestrator: issue un-paused for forced re-analysis",
				"identifier", ev.Identifier)
			if o.OnStateChange != nil {
				o.OnStateChange()
			}
		}

	case EventResumeIssue:
		if _, isPaused := state.PausedIdentifiers[ev.Identifier]; isPaused {
			delete(state.PausedIdentifiers, ev.Identifier)
			o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
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
			o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
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
		o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
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
		state.PendingInputResumes[ev.Identifier] = buildPendingInputResumeEntry(entry, ev.Message)
		slog.Info("orchestrator: user provided input, queued pending resume",
			"identifier", ev.Identifier, "session_id", entry.SessionID)
		// Post the user's reply as a tracker comment so the conversation
		// is visible in Linear/GitHub alongside the agent's question.
		// T-44 (02.G-01): tracked via commentWg so Run waits for the post
		// to finish on shutdown — otherwise the comment can be dropped if
		// the daemon exits before the (postRunTimeout-bounded) tracker
		// API call returns.
		o.commentWg.Add(1)
		go func(issueID, ident, msg string) {
			defer o.commentWg.Done()
			postCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
			defer cancel()
			if _, err := o.tracker.CreateComment(postCtx, issueID, tracker.MarkManagedComment(msg)); err != nil {
				slog.Warn("orchestrator: failed to post user input as tracker comment",
					"identifier", ident, "error", err)
			}
		}(entry.IssueID, ev.Identifier, ev.Message)
		state = o.processPendingInputResumes(ctx, state, time.Now())
		if o.OnStateChange != nil {
			o.OnStateChange()
		}

	case EventInputRequiredCommentRecorded:
		if ev.Comment == nil || ev.Identifier == "" {
			return state
		}
		if entry, ok := state.InputRequiredIssues[ev.Identifier]; ok {
			state.InputRequiredIssues[ev.Identifier] = withRecordedQuestionComment(entry, ev.Comment)
		}
		if pending, ok := state.PendingInputResumes[ev.Identifier]; ok {
			state.PendingInputResumes[ev.Identifier] = withRecordedPendingQuestionComment(pending, ev.Comment)
		}

	case EventDismissInput:
		entry, ok := state.InputRequiredIssues[ev.Identifier]
		if !ok {
			slog.Warn("orchestrator: dismiss-input for unknown identifier", "identifier", ev.Identifier)
			return state
		}
		delete(state.InputRequiredIssues, ev.Identifier)
		state.PausedIdentifiers[ev.Identifier] = entry.IssueID
		o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
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
		for i := range issues {
			o.auditFetchedIssueDependenciesAndDispatch(ctx, &state, issues[i], time.Now())
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

	case EventDispatchAutomation:
		if ev.Issue == nil || ev.Automation == nil {
			return state
		}
		o.dispatchOrQueueAutomation(ctx, &state, *ev.Issue, *ev.Automation, time.Now())

	case EventIssueStatusChanged:
		if ev.StatusChange != nil {
			appendIssueStatusChange(&state, *ev.StatusChange)
		}

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
		// has exited — CancelIssue will no longer find a cancel to invoke. Use
		// the same helper reconcile uses so cleanup is single-source-of-truth
		// (T-09). The cancel func itself was already invoked by the worker's
		// own exit path; calling cancelAndCleanupWorker here is redundant on
		// the cancel side but correct on the map-cleanup side.
		o.cancelAndCleanupWorker(ev.RunEntry.Issue.Identifier)

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
				// Resolve command for resume. The live RunEntry carries the
				// resolved profile name; command still comes from cfg so a resume
				// uses the same profile-specific runner where possible.
				o.cfgMu.RLock()
				resumeCommand := o.cfg.Agent.Command
				profiles := o.cfg.Agent.Profiles
				o.cfgMu.RUnlock()
				profileName := liveEntry.ProfileName
				if profileName == "" {
					profileName = state.IssueProfiles[issue.Identifier]
				}
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
			if liveEntry != nil && liveEntry.PendingInputResume {
				delete(state.PendingInputResumes, issue.Identifier)
				liveEntry.PendingInputResume = false
			}
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
			if liveEntry != nil && liveEntry.PendingInputResume {
				delete(state.PendingInputResumes, issue.Identifier)
				liveEntry.PendingInputResume = false
			}
			// Release the claim — the issue completed successfully.
			// Do NOT schedule a retry; successful completions must not appear in
			// the retry queue and must not cause infinite re-dispatch loops.
			delete(state.Claimed, ev.IssueID)
			// T-21: clear reviewer-injected profile overrides only. A user-set
			// override (via SetIssueProfile HTTP) is left intact even on a
			// reviewer-Kind completion, since the user never asked us to forget it.
			if liveEntry != nil && liveEntry.Kind == "reviewer" {
				o.issueProfilesMu.Lock()
				if _, injected := o.reviewerInjectedProfiles[issue.Identifier]; injected {
					delete(o.issueProfiles, issue.Identifier)
					delete(o.reviewerInjectedProfiles, issue.Identifier)
				}
				o.issueProfilesMu.Unlock()
			}
			// G-07 (gaps_280426_2): clear `issueBackends[identifier]` on terminal
			// completion to bound map growth across the daemon's lifetime. Unlike
			// `issueProfiles` (which has the reviewer-injected vs user-set
			// distinction handled above), every `issueBackends` entry is user-set
			// via the SetIssueBackend HTTP path. Discarding on terminal matches
			// what TerminateIssue (issue_control.go:95) already does on the
			// explicit-cancel path; without this, naturally-resolved issues
			// leave entries behind for the daemon's lifetime.
			o.issueBackendsMu.Lock()
			delete(o.issueBackends, issue.Identifier)
			o.issueBackendsMu.Unlock()
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
			// Gap §1.3 — clear the rate_limited auto-switch override on
			// successful exit so the next dispatch reverts to the
			// natural profile. Operator-set overrides (not marked in
			// AutoSwitchedIdentifiers) are preserved.
			if _, autoSwitched := state.AutoSwitchedIdentifiers[issue.Identifier]; autoSwitched {
				delete(state.IssueProfiles, issue.Identifier)
				delete(state.IssueBackends, issue.Identifier)
				delete(state.AutoSwitchedIdentifiers, issue.Identifier)
				delete(state.AutoSwitchedAt, issue.Identifier) // §6.2 keep maps in sync
				// Gap §5.3 — persist the cleared state so a restart
				// after the successful exit doesn't reload the stale
				// override.
				autoSwitchedCopy := maps.Clone(state.AutoSwitchedIdentifiers)
				profilesCopy := maps.Clone(state.IssueProfiles)
				backendsCopy := maps.Clone(state.IssueBackends)
				switchedAtCopy := maps.Clone(state.AutoSwitchedAt)
				go o.saveAutoSwitchedToDisk(autoSwitchedCopy, profilesCopy, backendsCopy, switchedAtCopy)
			}
			o.recordHistory(liveEntry, issue, now, "succeeded")
			// Auto-clear workspace if configured.
			//
			// New semantics (v0.2.0): clear only when the issue reaches a
			// terminal tracker state. For the success path that means we clear
			// when there is no further work pending on this workspace —
			// specifically: no reviewer will run after this worker. When an
			// auto-review is queued, defer the clear to the reviewer's own
			// TerminalSucceeded handler so the reviewer has the workspace
			// available. The TerminalFailed branch below clears on the
			// max-retries-exhausted-to-FailedState transition.
			o.cfgMu.RLock()
			autoClear := o.cfg.Workspace.AutoClearWorkspace
			reviewerProfile := o.cfg.Agent.ReviewerProfile
			autoReview := o.cfg.Agent.AutoReview
			o.cfgMu.RUnlock()
			reviewerWillRun := autoReview && reviewerProfile != "" && runEligibleForAutoReview(liveEntry)
			if autoClear && !reviewerWillRun {
				// Use the actual worktree branch propagated via sendExitWithBranch.
				// PR-continuation runs use prCtx.Branch, which differs from
				// issue.BranchName; re-deriving the branch here would delete the
				// wrong branch and leak the PR branch permanently (GO-H2).
				bn := ev.RunEntry.BranchName
				if bn == "" {
					bn = workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
				}
				o.scheduleWorkspaceClear(issue.Identifier, bn)
			}

			// Auto-review: if configured, dispatch a reviewer worker for this issue.
			// Only trigger when the completed worker was NOT itself a reviewer
			// (prevents infinite review loops).
			if reviewerWillRun {
				o.dispatchReviewerForIssue(ctx, &state, issue, reviewerProfile, now)
			}

		case TerminalStalled:
			// ReconcileStalls already handled claim deletion and retry scheduling
			// inline. All we need to do here is record history so stall kills appear
			// in the run-history ring buffer. Use ev.RunEntry (not liveEntry, which
			// is nil because ReconcileStalls already deleted it from state.Running).
			o.recordHistory(ev.RunEntry, issue, now, "stalled")
			// Mark any in-flight handoff file as .partial.md so subsequent
			// agents can see the stalled worker's partial deliverable without
			// confusing it for a clean handoff. Filesystem-driven (no need
			// to thread the run timestamp through the stall path, which the
			// orchestrator synthesizes itself).
			o.markStalledHandoffPartial(ev.RunEntry, issue)

		case TerminalInputRequired:
			delete(state.Claimed, ev.IssueID)
			if liveEntry != nil && liveEntry.PendingInputResume {
				delete(state.PendingInputResumes, issue.Identifier)
				liveEntry.PendingInputResume = false
			}
			entry := ev.InputRequiredEntry
			if entry == nil {
				break
			}
			if liveEntry != nil && ev.RunEntry != nil {
				if ev.RunEntry.TurnCount > 0 {
					liveEntry.TurnCount = ev.RunEntry.TurnCount
				}
				if ev.RunEntry.TotalTokens > 0 {
					liveEntry.TotalTokens = ev.RunEntry.TotalTokens
					liveEntry.InputTokens = ev.RunEntry.InputTokens
					liveEntry.OutputTokens = ev.RunEntry.OutputTokens
				}
				if ev.RunEntry.SessionID != "" {
					liveEntry.SessionID = ev.RunEntry.SessionID
				}
			}
			// Post the agent's question as a tracker comment so it's visible
			// in Linear/GitHub. The dashboard shows a reply UI; user replies
			// are also posted as tracker comments before resuming the agent.
			// T-44 (02.G-01): tracked via commentWg so Run waits for the post
			// AND the recorded-comment event to finish on shutdown.
			commentText := tracker.MarkManagedComment(buildInputRequiredComment(entry))
			o.commentWg.Add(1)
			go func(issueID, ident string) {
				defer o.commentWg.Done()
				postCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
				defer cancel()
				comment, err := o.tracker.CreateComment(postCtx, issueID, commentText)
				if err != nil {
					slog.Warn("orchestrator: failed to post input-required comment", "identifier", ident, "error", err)
					return
				}
				if comment == nil {
					return
				}
				sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer sendCancel()
				select {
				case o.events <- OrchestratorEvent{
					Type:       EventInputRequiredCommentRecorded,
					Identifier: ident,
					Comment:    comment,
				}:
				case <-sendCtx.Done():
					slog.Warn("orchestrator: input-required comment event lost", "identifier", ident)
				}
			}(entry.IssueID, issue.Identifier)
			state.InputRequiredIssues[issue.Identifier] = entry
			// Pass liveEntry so the B1 self-reentry guard can suppress dispatch
			// when the exiting worker was itself an input_required automation.
			o.dispatchMatchingInputRequiredAutomations(ctx, &state, issue, entry, now, liveEntry)
			slog.Info("orchestrator: issue queued for human input",
				"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
			o.recordHistory(liveEntry, issue, now, "input_required")

		default: // TerminalFailed (and any other unhandled terminal reasons)
			// context.Canceled means the worker was stopped by the orchestrator
			// (stall timeout, reload, shutdown) — not a real failure. Release the
			// claim so the issue can be dispatched fresh on the next poll cycle.
			if ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
				// ORCH-2: a reconcile kill path (e.g. ReconcileStalls) may have
				// already re-claimed this issue via ScheduleRetry before this
				// late exit arrived. Only release the claim when no pending
				// retry owns it — otherwise we'd orphan the retry's claim and
				// let the next poll cycle dispatch the issue fresh at attempt 0
				// while the retry later fires and overwrites state.Running.
				if _, retryPending := state.RetryAttempts[ev.IssueID]; !retryPending {
					delete(state.Claimed, ev.IssueID)
					slog.Info("orchestrator: worker context cancelled, claim released for re-dispatch",
						"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
				} else {
					slog.Info("orchestrator: worker context cancelled, claim retained for pending retry",
						"issue_id", ev.IssueID, "issue_identifier", issue.Identifier)
				}
				// Not recorded — the issue will be re-dispatched.
			} else {
				// Mark any in-flight handoff file as .partial.md so subsequent
				// agents (this attempt's retry, the reviewer, or a downstream
				// pipeline profile) see the failed worker's partial deliverable
				// distinctly from a clean handoff. No-op if the agent never
				// wrote a handoff this turn.
				o.markFailedHandoffPartial(ev.RunEntry, liveEntry, issue)
				errMsg := ""
				if ev.Error != nil {
					errMsg = ev.Error.Error()
				}
				nextAttempt := attempt + 1
				// Read through the cfgMu-guarded getters: G surfaces these fields
				// to a runtime PUT /api/v1/settings handler that writes under
				// cfgMu.Lock. Direct reads here would be a data race once the UI
				// is in use even if no -race test exercises the interleave today.
				maxRetries := o.MaxRetriesCfg()
				if maxRetries > 0 && nextAttempt > maxRetries {
					// Max retries exhausted — recover via rate_limited auto-switch
					// when possible, otherwise move to failed state or pause.
					slog.Warn("worker: max retries exhausted",
						"issue_id", issue.ID, "issue_identifier", issue.Identifier,
						"attempts", attempt, "max_retries", maxRetries)
					if o.logBuf != nil {
						o.logBuf.Add(issue.Identifier, makeBufLine("ERROR",
							fmt.Sprintf("worker: max retries exhausted (%d/%d)", attempt, maxRetries)))
					}
					o.commentMaxRetriesExhausted(issue, attempt, errMsg)
					delete(state.Claimed, ev.IssueID)

					rateLimitedQueued := 0
					// Gap §5.1 — use the operator-configurable patterns list
					// when present; otherwise fall back to defaults.
					o.cfgMu.RLock()
					rlPatterns := append([]string(nil), o.cfg.Agent.RateLimitErrorPatterns...)
					transportPatterns := append([]string(nil), o.cfg.Agent.TransportErrorPatterns...)
					o.cfgMu.RUnlock()
					// todolist4 A.4 — classify the exhausted-retry exit as a
					// transport failure so the dashboard can surface a
					// distinct paused_transport tile instead of conflating
					// it with the generic failure path.
					if IsTransportFailure(errMsg, transportPatterns) {
						state.TransportFailureCount++
					}
					if IsRateLimitFailureWithPatterns(errMsg, rlPatterns) {
						failedProfile := ""
						failedBackend := ""
						inputTokens := 0
						outputTokens := 0
						if liveEntry != nil {
							failedProfile = liveEntry.ProfileName
							failedBackend = liveEntry.Backend
							inputTokens = liveEntry.InputTokens
							outputTokens = liveEntry.OutputTokens
						}
						if failedProfile == "" {
							failedProfile = o.issueProfileForDispatch(state, issue.Identifier)
						}
						rateLimitedQueued = o.dispatchMatchingRateLimitedAutomations(
							ctx, &state, issue, now,
							failedProfile, failedBackend, errMsg, nextAttempt,
							inputTokens, outputTokens,
						)
					}

					failedState := o.FailedStateCfg()
					// `run_failed` automations are operator
					// safety nets and must fire on every retry-exhaustion, even when
					// a `rate_limited` recovery was queued. The failed-state
					// transition decision is independent: when a recovery is queued
					// we defer the tracker move so the recovery worker can finish
					// the issue normally.
					o.dispatchMatchingRunFailedAutomations(ctx, &state, issue, now, errMsg, nextAttempt)
					if rateLimitedQueued > 0 {
						slog.Info("orchestrator: rate_limited recovery queued, deferring failed-state transition",
							"issue_id", issue.ID, "issue_identifier", issue.Identifier,
							"queued", rateLimitedQueued)
					} else if failedState != "" {
						state = o.asyncDiscardAndTransitionTo(state, ev.IssueID, issue.Identifier, failedState)
						// New semantics (v0.2.0): clear workspace when the
						// issue reaches a terminal tracker state. FailedState
						// is terminal — no retries remain and no rate-limited
						// recovery is queued — so the workspace will not be
						// reused. The success path clears in the
						// TerminalSucceeded branch above.
						o.cfgMu.RLock()
						autoClearOnFailure := o.cfg.Workspace.AutoClearWorkspace
						o.cfgMu.RUnlock()
						if autoClearOnFailure {
							bn := ev.RunEntry.BranchName
							if bn == "" {
								bn = workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier)
							}
							o.scheduleWorkspaceClear(issue.Identifier, bn)
						}
					} else {
						state.PausedIdentifiers[issue.Identifier] = issue.ID
						o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
					}
					o.recordHistory(liveEntry, issue, now, "failed")
				} else {
					backoff := BackoffMs(nextAttempt, o.cfg.Agent.MaxRetryBackoffMs)
					state = ScheduleRetry(state, ev.IssueID, nextAttempt, issue.Identifier, errMsg, now, backoff)
					// liveEntry may be nil when a reconcile kill path already
					// removed the entry and the worker's exit event arrived
					// late (ORCH-1) — never dereference it unguarded.
					turnCount, inTok, outTok := 0, 0, 0
					if liveEntry != nil {
						turnCount, inTok, outTok = liveEntry.TurnCount, liveEntry.InputTokens, liveEntry.OutputTokens
					}
					slog.Info("orchestrator: worker failed, retry scheduled",
						"issue_id", ev.IssueID, "issue_identifier", issue.Identifier,
						"attempt", nextAttempt, "backoff_ms", backoff,
						"turns", turnCount, "input_tokens", inTok, "output_tokens", outTok)
					o.recordHistory(liveEntry, issue, now, "failed")
				}
			}
		}
	}
	return state
}

func shouldDrainAutomationQueueAfterEvent(eventType EventType) bool {
	switch eventType {
	case EventWorkerExited, EventResumeIssue, EventProvideInput, EventDismissInput, EventDiscardComplete:
		return true
	default:
		return false
	}
}

// commentMaxRetriesExhausted posts a comment on the issue explaining that
// the maximum number of retries has been exhausted.
// Uses context.Background() intentionally: this notification must be delivered
// even during graceful shutdown so the issue owner knows why retries stopped.
func (o *Orchestrator) commentMaxRetriesExhausted(issue domain.Issue, attempts int, lastErr string) {
	comment := fmt.Sprintf(
		"Itervox: maximum retries exhausted (%d attempts). Last error:\n\n%s\n\nRetries have stopped for this run. Itervox may move the issue to failed state, pause it, or let a matching automation recover it.",
		attempts, lastErr)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := o.tracker.CreateComment(ctx, issue.ID, tracker.MarkManagedComment(comment)); err != nil {
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
			o.RecordIssueStatusChange(IssueStatusChange{
				Identifier: identifier,
				ToState:    targetState,
				Source:     StatusSourceWorkerLifecycle,
			})
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
			o.RecordIssueStatusChange(IssueStatusChange{
				Identifier: identifier,
				ToState:    targetState,
				Source:     StatusSourceSystem,
			})
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
		run.ProfileName = liveEntry.ProfileName
		run.Kind = liveEntry.Kind
		run.SessionID = liveEntry.SessionID
		run.AutomationID = liveEntry.AutomationID
		run.TriggerType = liveEntry.TriggerType
		// CommentCount on liveEntry is rarely populated directly — the live
		// counter lives on the orchestrator (BumpCommentCount writes to the
		// concurrent map). Prefer the orchestrator counter; fall back to the
		// liveEntry value so unit tests that pass a CommentCount directly
		// still propagate.
		live := o.CommentCountFor(issue.Identifier)
		if live > liveEntry.CommentCount {
			run.CommentCount = live
		} else {
			run.CommentCount = liveEntry.CommentCount
		}
	} else {
		run.StartedAt = finishedAt
	}
	o.addCompletedRun(run)
	// Reset the per-identifier counter so the next run starts fresh — without
	// this a long-lived issue accumulates comment counts across multiple runs.
	o.ResetCommentCount(issue.Identifier)
}

func runEligibleForAutoReview(liveEntry *RunEntry) bool {
	if liveEntry == nil {
		return true
	}
	return liveEntry.Kind == "" || liveEntry.Kind == "worker"
}

// markStalledHandoffPartial renames the in-flight handoff file authored by
// the stalled worker's profile to `<basename>.partial.md`. Filesystem-driven
// (uses the profile name from RunEntry rather than threading the run
// timestamp through the stall path, which the orchestrator synthesizes
// itself). Safe to call when the worker never wrote a handoff or when no
// workspace exists yet — both paths no-op.
//
// runEntry.StartedAt is passed as the rename gate so files that predate
// the stalled worker (predecessor handoffs from successful prior runs)
// are not eligible for renaming. If the worker never wrote a handoff this
// run, no eligible file exists and the call is a no-op.
func (o *Orchestrator) markStalledHandoffPartial(runEntry *RunEntry, issue domain.Issue) {
	if o.workspace == nil || runEntry == nil {
		return
	}
	wsPath := o.workspace.ResolvePath(issue.Identifier)
	if wsPath == "" {
		return
	}
	if err := markLatestHandoffPartial(wsPath, runEntry.ProfileName, runEntry.StartedAt); err != nil {
		slog.Warn("orchestrator: failed to mark stalled handoff partial",
			"identifier", issue.Identifier, "error", err)
	}
}

// markFailedHandoffPartial renames the in-flight handoff file authored by
// the failed worker's profile to `<basename>.partial.md`. Called when a
// worker exits with TerminalFailed (process error, render error, agent
// failure) so subsequent agents can distinguish "tried and failed" from
// "completed cleanly." Safe no-op when no handoff was written.
//
// WORK-2: the exit's bare runEntry (sendExit's RunEntry, worker.go) carries
// neither ProfileName nor StartedAt — using it directly means named-profile
// failures never match their `_<profile>.md` suffix (handoff silently stays
// "clean"), and a zero StartedAt disables the notBefore gate below, letting
// a default-profile failure rename a PREDECESSOR's complete handoff. The
// live Running entry (liveEntry, populated at dispatch — event_loop.go
// ~748-749) is authoritative for both fields; runEntry is only a fallback
// for when liveEntry is nil (a reconcile kill path already removed it) or
// leaves a field unset. See markStalledHandoffPartial for the same gate.
func (o *Orchestrator) markFailedHandoffPartial(runEntry, liveEntry *RunEntry, issue domain.Issue) {
	if o.workspace == nil || runEntry == nil {
		return
	}
	profileName, startedAt := runEntry.ProfileName, runEntry.StartedAt
	if liveEntry != nil {
		if liveEntry.ProfileName != "" {
			profileName = liveEntry.ProfileName
		}
		if !liveEntry.StartedAt.IsZero() {
			startedAt = liveEntry.StartedAt
		}
	}
	wsPath := o.workspace.ResolvePath(issue.Identifier)
	if wsPath == "" {
		return
	}
	if err := markLatestHandoffPartial(wsPath, profileName, startedAt); err != nil {
		slog.Warn("orchestrator: failed to mark failed handoff partial",
			"identifier", issue.Identifier, "error", err)
	}
}

// scheduleWorkspaceClear runs `workspace.Manager.RemoveWorkspace` in a
// background goroutine so a slow os.RemoveAll (large workspaces) cannot
// stall the orchestrator event loop and starve other workers. The
// `autoClearWg` tracks in-flight clears so graceful shutdown can wait
// for them.
//
// New semantics (v0.2.0): the caller is responsible for deciding
// whether the issue has reached a terminal tracker state. This helper
// only enqueues the goroutine.
func (o *Orchestrator) scheduleWorkspaceClear(identifier, branchName string) {
	if o.workspace == nil {
		return
	}
	wm := o.workspace
	id := identifier
	bn := branchName
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

// buildSubAgentContext generates a "## Available Sub-Agents" section that is
// appended to the rendered prompt when agent teams mode is active.
// activeProfile is excluded from the list so the agent doesn't try to spawn itself.
// Returns an empty string when there are no other profiles to list.
// resolveResumeCommand builds the runner command for a resumed input-required
// worker. It resolves an empty entry.Command by checking the profile first,
// then falling back to cfg.Agent.Command. The backend hint is applied so that
// MultiRunner routes to the correct runner (CodexRunner vs ClaudeRunner).
//
// Without this, an entry persisted with an empty Command and Backend="codex"
// would fall back to cfg.Agent.Command (the claude binary) and the CodexRunner
// would receive the wrong binary on resume.
func resolveResumeCommand(entry *InputRequiredEntry, cfg *config.Config, cfgMu *sync.RWMutex) string {
	cmd := entry.Command
	if cmd == "" {
		// Resolution order:
		// 1. Profile's explicit command (most specific)
		// 2. Backend name as the binary (e.g. "codex" → the codex binary)
		// 3. cfg.Agent.Command (global default, always claude)
		cfgMu.RLock()
		if entry.ProfileName != "" {
			if profile, ok := cfg.Agent.Profiles[entry.ProfileName]; ok && profile.Command != "" {
				cmd = profile.Command
			}
		}
		if cmd == "" && entry.Backend != "" && entry.Backend != "claude" {
			// The backend name IS the binary name (codex → "codex").
			// Without this, a backend-only profile would fall through to
			// cfg.Agent.Command (claude) and CodexRunner would receive
			// the wrong binary.
			cmd = entry.Backend
		}
		if cmd == "" {
			cmd = cfg.Agent.Command
		}
		cfgMu.RUnlock()
		slog.Info("orchestrator: resume entry had empty command, resolved from config",
			"identifier", entry.Identifier, "resolved_command", cmd,
			"backend", entry.Backend, "profile", entry.ProfileName)
	}
	if entry.Backend != "" {
		cmd = agent.CommandWithBackendHint(cmd, entry.Backend)
	}
	return cmd
}

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
	listed := 0
	for name, p := range profiles {
		if name == activeProfile {
			continue
		}
		listed++
		if summary := profileRosterSummary(p); summary != "" {
			b.WriteString("- **" + name + "**: " + summary + "\n")
		} else {
			b.WriteString("- **" + name + "**\n")
		}
	}
	if listed == 0 {
		return ""
	}
	b.WriteString("\nUse the ")
	b.WriteString(toolName)
	b.WriteString(" tool with the sub-agent description when you need specialised help.")
	return b.String()
}

func profileRosterSummary(profile config.AgentProfile) string {
	if profile.Soul != "" {
		return firstProfileSummaryLine(profile.Soul)
	}
	return firstProfileSummaryLine(profile.Prompt)
}

func firstProfileSummaryLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(trimmed) > 180 {
			return trimmed[:180] + "..."
		}
		return trimmed
	}
	return ""
}

// StartupTerminalCleanup fetches terminal issues and removes their workspaces.
// Runs in the background with a 15-second timeout so it never blocks startup.
//
// Returns a `wait` closure that blocks until the cleanup goroutine has fully
// exited. Callers that need to ensure cleanup is complete before proceeding
// (e.g. shutdown teardown) MUST invoke `wait()`. Discarding the return value
// is safe — the goroutine exits on its own within the 15s timeout. T-49.
func StartupTerminalCleanup(ctx context.Context, tr tracker.Tracker, terminalStates []string, removeWorkspace func(string) error) (wait func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
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
	return func() { <-done }
}
