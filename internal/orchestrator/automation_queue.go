package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// AutomationQueueStatus is the runtime status of an automation queue entry.
type AutomationQueueStatus string

const (
	// AutomationQueueQueued means the entry is waiting for a retryable runtime condition.
	AutomationQueueQueued AutomationQueueStatus = "queued"
	// AutomationQueueBlocked means dependency blockers still prevent dispatch.
	AutomationQueueBlocked AutomationQueueStatus = "blocked"
	// AutomationQueueDispatching means the entry is currently being converted to a run.
	AutomationQueueDispatching AutomationQueueStatus = "dispatching"
)

// AutomationQueueReason classifies why an automation entry is queued.
type AutomationQueueReason string

const (
	AutomationQueueReasonReady              AutomationQueueReason = "ready"
	AutomationQueueReasonNoSlots            AutomationQueueReason = "no_slots"
	AutomationQueueReasonPerStateLimit      AutomationQueueReason = "per_state_limit"
	AutomationQueueReasonAlreadyRunning     AutomationQueueReason = "already_running"
	AutomationQueueReasonClaimed            AutomationQueueReason = "claimed"
	AutomationQueueReasonInputRequired      AutomationQueueReason = "input_required"
	AutomationQueueReasonPendingInputResume AutomationQueueReason = "pending_input_resume"
	AutomationQueueReasonBlockedBy          AutomationQueueReason = "blocked_by"
	// AutomationQueueReasonInferredBlockedBy mirrors AutomationQueueReasonBlockedBy
	// for the soft (LLM-inferred) dependency gate added by the
	// unified-dependency-graph work (dispatch.go's "inferred_blocked_by:<source>"
	// guard). The soft gate must never be harsher than the hard tracker-blocker
	// gate, so it gets the same queueable/Blocked-status treatment — only the
	// reason tag differs, so the dashboard and logs can still tell inferred
	// blockers apart from tracker blockers.
	AutomationQueueReasonInferredBlockedBy AutomationQueueReason = "inferred_blocked_by"
	AutomationQueueReasonPausedByState     AutomationQueueReason = "paused_by_state"
)

// AutomationQueueEntry preserves one automation trigger attempt until it can dispatch.
type AutomationQueueEntry struct {
	ID                string
	AutomationID      string
	TriggerType       string
	Issue             domain.Issue
	ProfileName       string
	Instructions      string
	Trigger           AutomationTriggerContext
	AutoResume        bool
	MoveToState       string
	UseIssueLifecycle bool
	Status            AutomationQueueStatus
	Reason            AutomationQueueReason
	ReasonDetail      string
	QueuedAt          time.Time
	FiredAt           time.Time
	LastFiredAt       time.Time
	LastAttemptAt     time.Time
	AttemptCount      int
	LastError         string
}

// AutomationQueueBackpressure records queue-cap saturation and rejected triggers.
type AutomationQueueBackpressure struct {
	Length             int
	MaxLength          int
	Saturated          bool
	PausedProducers    bool
	RejectedSinceBoot  int
	LastRejectedAt     time.Time
	LastRejectedReason string
	// todolist4 P2-2 — structured fields parallel to LastRejectedReason.
	// LastRejectedReason continues to carry the colon-joined `reason:auto:
	// trigger:ident` legacy string for backwards-compat with the dashboard;
	// these structured fields are the operator-friendly split.
	LastRejectedAutomationID string
	LastRejectedTrigger      string
	LastRejectedIdentifier   string
}

// DependencyAuditStatus is the normalized dependency state for one issue.
type DependencyAuditStatus string

const (
	DependencyAuditUnknown   DependencyAuditStatus = "unknown"
	DependencyAuditBlocked   DependencyAuditStatus = "blocked"
	DependencyAuditUnblocked DependencyAuditStatus = "unblocked"
)

// DependencyAuditSource identifies where a blocker relation came from.
type DependencyAuditSource string

const (
	DependencySourceTrackerRelation DependencyAuditSource = "tracker_relation"
	DependencySourceIssueText       DependencyAuditSource = "issue_text"
	DependencySourceIssueComment    DependencyAuditSource = "issue_comment"
	// DependencySourceSubIssue marks a blocker derived from a Linear parent's
	// child (sub-)issue, as opposed to an explicit "blocks" tracker relation.
	// Distinguished at internal/tracker/linear/normalize.go child-append time
	// via domain.BlockerRef.Origin == "sub_issue"; dependencySourceForBlocker
	// in dependency_audit.go maps that marker to this source.
	DependencySourceSubIssue DependencyAuditSource = DependencyAuditSource(domain.BlockerOriginSubIssue)
)

// DependencyAuditEntry is the event-loop-owned dependency state for one issue.
type DependencyAuditEntry struct {
	IssueID               string
	Identifier            string
	IssueState            string
	Status                DependencyAuditStatus
	Sources               []DependencyAuditSource
	BlockedBy             []domain.BlockerRef
	UnresolvedBlockers    []domain.BlockerRef
	ResolvedBlockers      []domain.BlockerRef
	WasBlocked            bool
	FirstBlockedAt        time.Time
	UnblockedAt           time.Time
	LastAuditedAt         time.Time
	LastTransitionVersion int64
	LastTransitionReason  string
	// InFlight is true while an off-loop refresh batch holds this row. It
	// excludes the row from re-selection until the result lands or the
	// watchdog fires. NOT durable — cleared unconditionally on envelope
	// restore, since a crash mid-refresh would otherwise wedge the row.
	InFlight bool
	// ConsecutiveFailures counts back-to-back transient refresh failures.
	// Reset to 0 on any successful refresh. Drives the degraded marker.
	ConsecutiveFailures int
	// LastRefreshAttemptAt is when a refresh was last *attempted* for this
	// row. Distinct from LastAuditedAt, which means "recomputed" and ticks
	// for every candidate issue every tick regardless of whether a fetch
	// occurred — making it the wrong signal for a refresh interval.
	LastRefreshAttemptAt time.Time
}

func automationQueueKey(issue domain.Issue, dispatch AutomationDispatch) string {
	issueKey := issue.ID
	if issueKey == "" {
		issueKey = issue.Identifier
	}
	parts := []string{"automation", dispatch.AutomationID, dispatch.Trigger.Type, issueKey}

	switch dispatch.Trigger.Type {
	case config.AutomationTriggerPROpened:
		parts = append(parts, dispatch.Trigger.PRURL)
	case config.AutomationTriggerRateLimited:
		parts = append(parts, dispatch.Trigger.FailedProfile, dispatch.Trigger.FailedBackend)
	case config.AutomationTriggerInputRequired:
		questionKey := dispatch.Trigger.CommentID
		if questionKey == "" {
			questionKey = dispatch.Trigger.InputContext
		}
		parts = append(parts, questionKey)
	case config.AutomationTriggerRunFailed:
		parts = append(parts, strconv.Itoa(dispatch.Trigger.RetryAttempt))
	case config.AutomationTriggerBlockersResolved:
		parts = append(parts, strconv.FormatInt(dispatch.Trigger.DependencyAuditVersion, 10))
	case config.AutomationTriggerTrackerComment:
		commentKey := dispatch.Trigger.CommentID
		if commentKey == "" {
			commentKey = dispatch.Trigger.CommentCreatedAt
		}
		if commentKey == "" {
			commentKey = strconv.FormatInt(dispatch.Trigger.FiredAt.UnixNano(), 10)
		}
		parts = append(parts, commentKey)
	case config.AutomationTriggerIssueEnteredState, config.AutomationTriggerIssueMovedBacklog:
		parts = append(parts, dispatch.Trigger.PreviousState, dispatch.Trigger.TriggerState)
		if !dispatch.Trigger.FiredAt.IsZero() {
			parts = append(parts, strconv.FormatInt(dispatch.Trigger.FiredAt.UnixNano(), 10))
		}
	case TestAutomationTriggerType:
		parts = append(parts, strconv.FormatInt(dispatch.Trigger.FiredAt.UnixNano(), 10))
	}

	return strings.Join(parts, ":")
}

func enqueueAutomation(state *State, issue domain.Issue, dispatch AutomationDispatch, reason string, now time.Time) bool {
	ensureAutomationQueueState(state)
	id := automationQueueKey(issue, dispatch)
	queueReason, detail := automationQueueReasonFromString(reason)
	firedAt := dispatch.Trigger.FiredAt
	if firedAt.IsZero() {
		firedAt = now
	}

	if existing := state.AutomationQueue[id]; existing != nil {
		existing.Issue = issue
		existing.ProfileName = dispatch.ProfileName
		existing.Instructions = dispatch.Instructions
		existing.Trigger = dispatch.Trigger
		existing.AutoResume = dispatch.AutoResume
		existing.MoveToState = dispatch.MoveToState
		existing.UseIssueLifecycle = dispatch.UseIssueLifecycle
		existing.Status = automationQueueStatusForReason(queueReason)
		existing.Reason = queueReason
		existing.ReasonDetail = detail
		existing.LastFiredAt = firedAt
		existing.LastAttemptAt = now
		existing.AttemptCount++
		refreshAutomationQueueBackpressure(state)
		return true
	}

	if !automationQueueCanAccept(*state, id) {
		recordAutomationQueueRejected(state, dispatch, issue, "queue_full", now)
		return false
	}

	state.AutomationQueue[id] = &AutomationQueueEntry{
		ID:                id,
		AutomationID:      dispatch.AutomationID,
		TriggerType:       dispatch.Trigger.Type,
		Issue:             issue,
		ProfileName:       dispatch.ProfileName,
		Instructions:      dispatch.Instructions,
		Trigger:           dispatch.Trigger,
		AutoResume:        dispatch.AutoResume,
		MoveToState:       dispatch.MoveToState,
		UseIssueLifecycle: dispatch.UseIssueLifecycle,
		Status:            automationQueueStatusForReason(queueReason),
		Reason:            queueReason,
		ReasonDetail:      detail,
		QueuedAt:          now,
		FiredAt:           firedAt,
		LastFiredAt:       firedAt,
		LastAttemptAt:     now,
		AttemptCount:      1,
	}
	state.AutomationQueueOrder = append(state.AutomationQueueOrder, id)
	refreshAutomationQueueBackpressure(state)
	return true
}

func automationDispatchFromQueueEntry(entry *AutomationQueueEntry) AutomationDispatch {
	if entry == nil {
		return AutomationDispatch{}
	}
	return AutomationDispatch{
		AutomationID:      entry.AutomationID,
		ProfileName:       entry.ProfileName,
		Instructions:      entry.Instructions,
		Trigger:           entry.Trigger,
		AutoResume:        entry.AutoResume,
		MoveToState:       entry.MoveToState,
		UseIssueLifecycle: entry.UseIssueLifecycle,
	}
}

func sortedAutomationQueue(state State) []*AutomationQueueEntry {
	entries := make([]*AutomationQueueEntry, 0, len(state.AutomationQueueOrder))
	for _, id := range state.AutomationQueueOrder {
		entry := state.AutomationQueue[id]
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

func removeAutomationQueueEntry(state *State, id string) {
	ensureAutomationQueueState(state)
	delete(state.AutomationQueue, id)
	for i, existing := range state.AutomationQueueOrder {
		if existing == id {
			state.AutomationQueueOrder = append(state.AutomationQueueOrder[:i], state.AutomationQueueOrder[i+1:]...)
			refreshAutomationQueueBackpressure(state)
			return
		}
	}
	refreshAutomationQueueBackpressure(state)
}

func ensureAutomationQueueState(state *State) {
	if state.AutomationQueue == nil {
		state.AutomationQueue = make(map[string]*AutomationQueueEntry)
	}
	if state.AutomationQueueOrder == nil {
		state.AutomationQueueOrder = []string{}
	}
	if state.DependencyAudit == nil {
		state.DependencyAudit = make(map[string]*DependencyAuditEntry)
	}
	refreshAutomationQueueBackpressure(state)
}

func automationQueueCanAccept(state State, key string) bool {
	if _, exists := state.AutomationQueue[key]; exists {
		return true
	}
	return len(state.AutomationQueue) < automationQueueMaxLength(state)
}

func recordAutomationQueueRejected(state *State, dispatch AutomationDispatch, issue domain.Issue, reason string, now time.Time) {
	maxLength := automationQueueMaxLength(*state)
	state.AutomationQueueBackpressure.Length = len(state.AutomationQueue)
	state.AutomationQueueBackpressure.MaxLength = maxLength
	state.AutomationQueueBackpressure.Saturated = true
	state.AutomationQueueBackpressure.PausedProducers = true
	state.AutomationQueueBackpressure.RejectedSinceBoot++
	state.AutomationQueueBackpressure.LastRejectedAt = now
	state.AutomationQueueBackpressure.LastRejectedReason = strings.Join([]string{
		reason,
		dispatch.AutomationID,
		dispatch.Trigger.Type,
		issue.Identifier,
	}, ":")
	state.AutomationQueueBackpressure.LastRejectedAutomationID = dispatch.AutomationID
	state.AutomationQueueBackpressure.LastRejectedTrigger = dispatch.Trigger.Type
	state.AutomationQueueBackpressure.LastRejectedIdentifier = issue.Identifier
	slog.Warn("orchestrator: automation queue full, rejecting dispatch",
		"identifier", issue.Identifier,
		"automation", dispatch.AutomationID,
		"trigger", dispatch.Trigger.Type,
		"length", state.AutomationQueueBackpressure.Length,
		"max_length", maxLength)
}

func refreshAutomationQueueBackpressure(state *State) {
	maxLength := automationQueueMaxLength(*state)
	length := len(state.AutomationQueue)
	wasPaused := state.AutomationQueueBackpressure.PausedProducers
	state.AutomationQueueBackpressure.Length = length
	state.AutomationQueueBackpressure.MaxLength = maxLength
	if length >= maxLength {
		state.AutomationQueueBackpressure.Saturated = true
		state.AutomationQueueBackpressure.PausedProducers = true
		// v0.2.0 audit P2-13 — log the false→true transition so operators
		// tailing the log can see "queue full" without having to also
		// monitor the dashboard snapshot. recordAutomationQueueRejected
		// fires on each rejection; this fires once per pause cycle.
		if !wasPaused {
			slog.Warn("orchestrator: automation producers paused", "length", length, "max_length", maxLength)
		}
		return
	}
	state.AutomationQueueBackpressure.Saturated = false
	if state.AutomationQueueBackpressure.PausedProducers && length < automationQueueLowWater(maxLength) {
		state.AutomationQueueBackpressure.PausedProducers = false
		// v0.2.0 audit P2-13 — pair the resume log with the paused log
		// above so the cycle is observable end-to-end.
		slog.Info("orchestrator: automation producers resumed", "length", length, "max_length", maxLength)
	}
}

func automationQueueMaxLength(state State) int {
	if state.AutomationQueueBackpressure.MaxLength > 0 {
		return state.AutomationQueueBackpressure.MaxLength
	}
	return 100
}

func automationQueueLowWater(maxLength int) int {
	if maxLength <= 1 {
		return 1
	}
	low := maxLength * 8 / 10
	if low < 1 {
		return 1
	}
	return low
}

func automationQueueReasonFromString(reason string) (AutomationQueueReason, string) {
	before, after, ok := strings.Cut(reason, ":")
	if !ok {
		return AutomationQueueReason(reason), ""
	}
	switch AutomationQueueReason(before) {
	case AutomationQueueReasonBlockedBy, AutomationQueueReasonInferredBlockedBy:
		return AutomationQueueReason(before), after
	default:
		return AutomationQueueReason(reason), ""
	}
}

func automationQueueableReason(reason string) (bool, AutomationQueueReason, string) {
	if state, ok := strings.CutPrefix(reason, "paused_by_state:"); ok {
		return true, AutomationQueueReasonPausedByState, state
	}
	queueReason, detail := automationQueueReasonFromString(reason)
	switch queueReason {
	case AutomationQueueReasonNoSlots,
		AutomationQueueReasonPerStateLimit,
		AutomationQueueReasonAlreadyRunning,
		AutomationQueueReasonClaimed,
		AutomationQueueReasonInputRequired,
		AutomationQueueReasonPendingInputResume,
		AutomationQueueReasonBlockedBy,
		AutomationQueueReasonInferredBlockedBy:
		return true, queueReason, detail
	default:
		return false, "", ""
	}
}

// IsQueueableAutomationReason reports whether an automation ineligibility
// reason should be preserved in the durable automation queue instead of
// being dropped by producer-side prefilters.
func IsQueueableAutomationReason(reason string) bool {
	queueable, _, _ := automationQueueableReason(reason)
	return queueable
}

func automationQueueStatusForReason(reason AutomationQueueReason) AutomationQueueStatus {
	if reason == AutomationQueueReasonBlockedBy || reason == AutomationQueueReasonInferredBlockedBy {
		return AutomationQueueBlocked
	}
	return AutomationQueueQueued
}

func updateAutomationQueueEntryReason(entry *AutomationQueueEntry, reason string, now time.Time) bool {
	if entry == nil {
		return false
	}
	queueable, queueReason, detail := automationQueueableReason(reason)
	if !queueable {
		return false
	}
	entry.Status = automationQueueStatusForReason(queueReason)
	entry.Reason = queueReason
	entry.ReasonDetail = detail
	entry.LastAttemptAt = now
	entry.AttemptCount++
	return true
}

func (o *Orchestrator) automationProfileUnavailableReason(dispatch AutomationDispatch) string {
	if dispatch.ProfileName == "" {
		return "missing_profile"
	}
	o.cfgMu.RLock()
	profile, ok := o.cfg.Agent.Profiles[dispatch.ProfileName]
	o.cfgMu.RUnlock()
	if !ok {
		return "missing_profile"
	}
	if !config.ProfileEnabled(profile) {
		return "disabled_profile"
	}
	return ""
}

// claimPROpenedDedup mark-and-attempts the pr_opened dispatch dedup ledger:
// returns true when the (identifier, prURL, automationID) triple is fresh and
// records the key for future calls; returns false when an earlier dispatch
// already claimed the same triple. No-op for non-pr_opened triggers.
func claimPROpenedDedup(state *State, issue domain.Issue, dispatch AutomationDispatch) bool {
	if dispatch.Trigger.Type != config.AutomationTriggerPROpened {
		return true
	}
	if dispatch.Trigger.PRURL == "" {
		return true
	}
	key := issue.Identifier + "|" + dispatch.Trigger.PRURL + "|" + dispatch.AutomationID
	if state.PROpenedDispatched == nil {
		state.PROpenedDispatched = make(map[string]struct{})
	}
	if _, fired := state.PROpenedDispatched[key]; fired {
		state.AutomationDroppedPROpenedDedupTotal++
		slog.Info("orchestrator: pr_opened automation skipped (dedup)",
			"identifier", issue.Identifier,
			"automation", dispatch.AutomationID,
			"pr_url", dispatch.Trigger.PRURL,
			"automation_dropped_pr_opened_dedup_total", state.AutomationDroppedPROpenedDedupTotal)
		return false
	}
	state.PROpenedDispatched[key] = struct{}{}
	state.AutomationDispatchesPROpenedTotal++
	return true
}

func (o *Orchestrator) dispatchOrQueueAutomation(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	dispatch AutomationDispatch,
	now time.Time,
) bool {
	// pr_opened dedup: resumed workers, retries, and
	// secondary runs on the same issue all re-detect the same PR URL.
	if !claimPROpenedDedup(state, issue, dispatch) {
		return false
	}
	if reason := o.automationProfileUnavailableReason(dispatch); reason != "" {
		slog.Warn("orchestrator: dropping automation dispatch with invalid profile",
			"identifier", issue.Identifier,
			"automation", dispatch.AutomationID,
			"profile", dispatch.ProfileName,
			"reason", reason)
		return false
	}
	if _, waiting := state.InputRequiredIssues[issue.Identifier]; waiting &&
		dispatch.Trigger.Type != config.AutomationTriggerInputRequired {
		accepted := enqueueAutomation(state, issue, dispatch, string(AutomationQueueReasonInputRequired), now)
		slog.Debug("orchestrator: queued automation dispatch (input_required arrived before dispatch)",
			"identifier", issue.Identifier,
			"automation", dispatch.AutomationID,
			"reason", "input_required")
		return accepted
	}
	if dispatch.Trigger.Type == config.AutomationTriggerRateLimited && dispatch.AutoResume {
		if _, autoSwitched := state.AutoSwitchedIdentifiers[issue.Identifier]; autoSwitched {
			delete(state.PausedIdentifiers, issue.Identifier)
			delete(state.PausedSessions, issue.Identifier)
			o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
		}
	}
	if reason := ineligibleReasonForAutomation(issue, *state, o.cfg); reason != "" {
		if queueable, _, _ := automationQueueableReason(reason); queueable {
			accepted := enqueueAutomation(state, issue, dispatch, reason, now)
			slog.Debug("orchestrator: queued automation dispatch",
				"identifier", issue.Identifier,
				"automation", dispatch.AutomationID,
				"reason", reason)
			return accepted
		}
		slog.Debug("orchestrator: dropping automation dispatch",
			"identifier", issue.Identifier,
			"automation", dispatch.AutomationID,
			"reason", reason)
		return false
	}
	return o.startAutomationRun(ctx, state, issue, now, dispatch)
}

// drainAutomationQueueFetchBudget caps per-drain tracker fetches as a
// belt-and-braces defence against pathological queue sizes. Identifiers
// already present in the per-tick candidate set never count against this
// budget — they reuse the candidate fetch result and incur zero new tracker
// calls. v0.2.0 audit P1-1.
const drainAutomationQueueFetchBudget = 10

func (o *Orchestrator) drainAutomationQueue(ctx context.Context, state *State, now time.Time) {
	o.drainAutomationQueueWithCandidates(ctx, state, now, nil)
}

// drainAutomationQueueWithCandidates runs the queue-drain pass, optionally
// reusing `candidates` (keyed by both issue.ID and issue.Identifier) so the
// per-entry tracker fetch can be skipped when the issue was already fetched
// by the tick's candidate poll. Pass nil when no candidate hint exists
// (e.g. the deferred drain in handleEvent). v0.2.0 audit P1-1.
func (o *Orchestrator) drainAutomationQueueWithCandidates(
	ctx context.Context,
	state *State,
	now time.Time,
	candidates map[string]domain.Issue,
) {
	ensureAutomationQueueState(state)
	pollInterval := time.Duration(state.PollIntervalMs) * time.Millisecond
	fetchBudgetRemaining := drainAutomationQueueFetchBudget

	for _, entry := range sortedAutomationQueue(*state) {
		if AvailableSlots(*state) <= 0 {
			return
		}
		if entry == nil {
			continue
		}
		dispatch := automationDispatchFromQueueEntry(entry)
		if reason := o.automationProfileUnavailableReason(dispatch); reason != "" {
			slog.Warn("orchestrator: dropping queued automation with invalid profile",
				"identifier", entry.Issue.Identifier,
				"automation", entry.AutomationID,
				"profile", entry.ProfileName,
				"reason", reason)
			removeAutomationQueueEntry(state, entry.ID)
			continue
		}

		issue, refreshed, ok := o.resolveQueuedAutomationIssue(ctx, state, entry, now, candidates, pollInterval, &fetchBudgetRemaining)
		if !ok {
			continue
		}
		if refreshed {
			auditFetchedIssueDependencies(state, issue, now)
		}

		reason := ineligibleReasonForAutomation(issue, *state, o.cfg)
		if reason != "" {
			if !updateAutomationQueueEntryReason(entry, reason, now) {
				slog.Debug("orchestrator: dropping queued automation dispatch",
					"identifier", issue.Identifier,
					"automation", entry.AutomationID,
					"reason", reason)
				removeAutomationQueueEntry(state, entry.ID)
				continue
			}
			if entry.Reason == AutomationQueueReasonNoSlots {
				return
			}
			continue
		}

		entry.Status = AutomationQueueDispatching
		entry.LastAttemptAt = now
		entry.AttemptCount++
		if o.startAutomationRun(ctx, state, issue, now, dispatch) {
			removeAutomationQueueEntry(state, entry.ID)
			continue
		}
		if AvailableSlots(*state) <= 0 {
			_ = updateAutomationQueueEntryReason(entry, string(AutomationQueueReasonNoSlots), now)
			return
		}
		slog.Debug("orchestrator: dropping queued automation after dispatch failed",
			"identifier", issue.Identifier,
			"automation", entry.AutomationID)
		removeAutomationQueueEntry(state, entry.ID)
	}
}

// resolveQueuedAutomationIssue returns the freshest issue snapshot for a
// queued entry, preferring (in order): the supplied candidate map, the
// entry's existing snapshot when LastAttemptAt is within the poll interval,
// and a synchronous tracker fetch as the last resort. The fetch budget is
// shared across one drain pass; once it hits zero the function falls back to
// the stale entry.Issue rather than blocking the event loop further.
// v0.2.0 audit P1-1.
//
// The bool return triple is (issue, freshlyFetched, ok). freshlyFetched is
// true only when the tracker was actually consulted, which the caller uses
// to gate the auditFetchedIssueDependencies follow-up.
func (o *Orchestrator) resolveQueuedAutomationIssue(
	ctx context.Context,
	state *State,
	entry *AutomationQueueEntry,
	now time.Time,
	candidates map[string]domain.Issue,
	pollInterval time.Duration,
	fetchBudget *int,
) (domain.Issue, bool, bool) {
	if entry == nil {
		return domain.Issue{}, false, false
	}
	if hint, ok := lookupCandidate(candidates, entry.Issue); ok {
		entry.Issue = hint
		entry.LastError = ""
		return hint, false, true
	}
	if pollInterval > 0 && !entry.LastAttemptAt.IsZero() && now.Sub(entry.LastAttemptAt) < pollInterval {
		return entry.Issue, false, true
	}
	if fetchBudget != nil && *fetchBudget <= 0 {
		return entry.Issue, false, true
	}
	issue, refreshed := o.refreshQueuedAutomationIssue(ctx, state, entry, now)
	if !refreshed {
		return domain.Issue{}, false, false
	}
	if fetchBudget != nil {
		*fetchBudget--
	}
	return issue, true, true
}

// lookupCandidate finds an issue in the candidate map by ID or Identifier.
// Both lookup paths are necessary because automation queue entries reference
// issues sometimes by ID (Linear-shaped) and sometimes by Identifier
// (GitHub-shaped). v0.2.0 audit P1-1.
func lookupCandidate(candidates map[string]domain.Issue, target domain.Issue) (domain.Issue, bool) {
	if len(candidates) == 0 {
		return domain.Issue{}, false
	}
	if target.ID != "" {
		if hit, ok := candidates[target.ID]; ok {
			return hit, true
		}
	}
	if target.Identifier != "" {
		if hit, ok := candidates[target.Identifier]; ok {
			return hit, true
		}
	}
	return domain.Issue{}, false
}

func (o *Orchestrator) refreshQueuedAutomationIssue(
	ctx context.Context,
	state *State,
	entry *AutomationQueueEntry,
	now time.Time,
) (domain.Issue, bool) {
	if entry == nil {
		return domain.Issue{}, false
	}
	if o.tracker == nil {
		return entry.Issue, true
	}
	var (
		issue *domain.Issue
		err   error
	)
	switch {
	case entry.Issue.ID != "":
		issue, err = o.tracker.FetchIssueDetail(ctx, entry.Issue.ID)
	case entry.Issue.Identifier != "":
		issue, err = o.tracker.FetchIssueByIdentifier(ctx, entry.Issue.Identifier)
	default:
		return entry.Issue, true
	}
	if err != nil {
		entry.LastAttemptAt = now
		entry.LastError = err.Error()
		if errors.Is(err, tracker.ErrNotFound) {
			slog.Debug("orchestrator: dropping queued automation for missing issue",
				"identifier", entry.Issue.Identifier,
				"automation", entry.AutomationID,
				"error", err)
			removeAutomationQueueEntry(state, entry.ID)
			return domain.Issue{}, false
		}
		slog.Warn("orchestrator: queued automation issue refresh failed",
			"identifier", entry.Issue.Identifier,
			"automation", entry.AutomationID,
			"error", err)
		return domain.Issue{}, false
	}
	if issue == nil {
		entry.LastAttemptAt = now
		entry.LastError = "tracker returned nil issue"
		return domain.Issue{}, false
	}
	entry.Issue = *issue
	entry.LastError = ""
	return entry.Issue, true
}
