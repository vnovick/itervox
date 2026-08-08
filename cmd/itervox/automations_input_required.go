package main

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// inputRequiredDetailTTL bounds how long a fetched issue detail may be reused
// across replay ticks.
//
// The replay runs every automation tick, and each blocked issue with a pending
// rule cost one FetchIssueDetail per tick — one of the largest contributors to
// Linear rate-limit pressure, for data that rarely changes while an issue sits
// blocked. Reuse is already invalidated precisely when the blocked context
// moves (see blockedKey), so this TTL is only a backstop for issue edits that
// leave the key untouched: a label or state change made while the issue is
// blocked takes effect at most this late.
const inputRequiredDetailTTL = 60 * time.Second

type inputRequiredReplayState struct {
	initialized bool
	issues      map[string]inputRequiredReplayIssueState
	// details carries fetched issue detail across ticks. Entries are only
	// reused for the same blockedKey and within inputRequiredDetailTTL, and
	// only entries touched this tick survive into the next state — so an
	// issue leaving input_required drops out without explicit pruning.
	details map[string]inputRequiredDetailCacheEntry
}

type inputRequiredDetailCacheEntry struct {
	// issue is nil when the fetch failed. A nil entry is honoured for the
	// remainder of the tick that produced it, but never reused across ticks:
	// a transient tracker error must not suppress replay for a whole TTL.
	issue      *domain.Issue
	blockedKey string
	fetchedAt  time.Time
}

type inputRequiredReplayIssueState struct {
	blockedKey         string
	firedAutomationIDs map[string]struct{}
}

func replayInputRequiredAutomations(
	ctx context.Context,
	tr tracker.Tracker,
	orch *orchestrator.Orchestrator,
	automations []orchestrator.InputRequiredAutomation,
	prev inputRequiredReplayState,
	now time.Time,
) inputRequiredReplayState {
	snap := orch.Snapshot()
	if automationProducersPaused(snap) {
		slog.Warn("automation: input-required replay paused by automation queue backpressure")
		return prev
	}
	next := inputRequiredReplayState{
		initialized: true,
		issues:      make(map[string]inputRequiredReplayIssueState, len(snap.InputRequiredIssues)),
		details:     make(map[string]inputRequiredDetailCacheEntry, len(snap.InputRequiredIssues)),
	}
	if len(snap.InputRequiredIssues) == 0 {
		return next
	}

	activeAutomationIDs := make(map[string]struct{}, len(automations))
	for _, automation := range automations {
		activeAutomationIDs[automation.ID] = struct{}{}
	}

	identifiers := make([]string, 0, len(snap.InputRequiredIssues))
	for identifier := range snap.InputRequiredIssues {
		identifiers = append(identifiers, identifier)
	}
	slices.Sort(identifiers)

	dispatched := 0
	for _, identifier := range identifiers {
		entry := snap.InputRequiredIssues[identifier]
		if entry == nil {
			continue
		}

		issueState := inputRequiredReplayIssueState{
			blockedKey:         inputRequiredReplayKey(entry),
			firedAutomationIDs: make(map[string]struct{}),
		}
		if prevIssue, ok := prev.issues[identifier]; ok && prevIssue.blockedKey == issueState.blockedKey {
			maps.Copy(issueState.firedAutomationIDs, filterReplayAutomationIDs(prevIssue.firedAutomationIDs, activeAutomationIDs))
		} else if prev.initialized {
			// New blocked issues observed after startup were already handled by
			// the event-loop input_required / recovery path. Seed the current
			// automations as fired so only newly-added rules replay later.
			maps.Copy(issueState.firedAutomationIDs, activeAutomationIDs)
			next.issues[identifier] = issueState
			continue
		}

		if len(automations) == 0 {
			next.issues[identifier] = issueState
			continue
		}

		// The detail below feeds only the automation loop, so once every
		// active rule has fired for this blocked context the fetch cannot
		// change the outcome. Skipping it is what removes the steady-state
		// cost: an issue can sit in input_required for hours with its
		// automations long since fired, and re-fetching it every tick buys
		// nothing.
		if !hasPendingReplayAutomation(automations, issueState.firedAutomationIDs) {
			next.issues[identifier] = issueState
			continue
		}

		issue := replayInputRequiredIssueDetail(ctx, tr, prev.details, next.details, entry, issueState.blockedKey, now)
		if issue == nil {
			next.issues[identifier] = issueState
			continue
		}

		for _, automation := range automations {
			if _, alreadyFired := issueState.firedAutomationIDs[automation.ID]; alreadyFired {
				continue
			}
			if !matchesReplayInputRequiredAutomation(*issue, automation, entry.Context) {
				continue
			}
			if orch.DispatchAutomation(ctx, *issue, replayInputRequiredDispatch(entry, automation, now)) {
				issueState.firedAutomationIDs[automation.ID] = struct{}{}
				dispatched++
			}
		}
		next.issues[identifier] = issueState
	}

	if dispatched > 0 {
		slog.Info("automation: accepted input-required dispatch events", "count", dispatched)
	}
	return next
}

func filterReplayAutomationIDs(ids map[string]struct{}, present map[string]struct{}) map[string]struct{} {
	filtered := make(map[string]struct{}, len(ids))
	for id := range ids {
		if _, ok := present[id]; ok {
			filtered[id] = struct{}{}
		}
	}
	return filtered
}

func inputRequiredReplayKey(entry *orchestrator.InputRequiredEntry) string {
	if entry == nil {
		return ""
	}
	if entry.QuestionCommentID != "" {
		return "comment:" + entry.QuestionCommentID
	}
	if !entry.QueuedAt.IsZero() {
		return "queued:" + entry.QueuedAt.UTC().Format(time.RFC3339Nano)
	}
	return "context:" + entry.IssueID + ":" + entry.Context
}

// hasPendingReplayAutomation reports whether any active rule has yet to fire
// for this blocked context.
func hasPendingReplayAutomation(automations []orchestrator.InputRequiredAutomation, fired map[string]struct{}) bool {
	for _, automation := range automations {
		if _, ok := fired[automation.ID]; !ok {
			return true
		}
	}
	return false
}

// replayInputRequiredIssueDetail resolves the issue detail for a blocked entry,
// reusing prev's fetch where that is still sound and recording whatever it
// resolves into next so the following tick can reuse it in turn.
func replayInputRequiredIssueDetail(
	ctx context.Context,
	tr tracker.Tracker,
	prev, next map[string]inputRequiredDetailCacheEntry,
	entry *orchestrator.InputRequiredEntry,
	blockedKey string,
	now time.Time,
) *domain.Issue {
	if entry == nil {
		return nil
	}
	if entry.IssueID != "" {
		// Same tick: honour whatever was already resolved, including a
		// failure, so one bad issue costs at most one request per tick.
		if cached, ok := next[entry.IssueID]; ok {
			return cached.issue
		}
		// Earlier tick: reuse only a successful fetch, taken under the same
		// blocked context, still inside the TTL. A new question comment moves
		// blockedKey and so forces a refetch.
		if cached, ok := prev[entry.IssueID]; ok &&
			cached.issue != nil &&
			cached.blockedKey == blockedKey &&
			now.Sub(cached.fetchedAt) < inputRequiredDetailTTL {
			next[entry.IssueID] = cached
			return cached.issue
		}
		issue, err := tr.FetchIssueDetail(ctx, entry.IssueID)
		if err != nil {
			slog.Warn("automation: input-required replay detail fetch failed",
				"identifier", entry.Identifier,
				"issue_id", entry.IssueID,
				"error", err)
			next[entry.IssueID] = inputRequiredDetailCacheEntry{blockedKey: blockedKey, fetchedAt: now}
			return nil
		}
		next[entry.IssueID] = inputRequiredDetailCacheEntry{issue: issue, blockedKey: blockedKey, fetchedAt: now}
		return issue
	}
	if entry.Identifier == "" {
		return nil
	}
	issue, err := tr.FetchIssueByIdentifier(ctx, entry.Identifier)
	if err != nil {
		slog.Warn("automation: input-required replay identifier fetch failed",
			"identifier", entry.Identifier,
			"error", err)
		return nil
	}
	return issue
}

func matchesReplayInputRequiredAutomation(issue domain.Issue, automation orchestrator.InputRequiredAutomation, inputContext string) bool {
	return matchesAutomationFilter(issue, compiledAutomation{
		cfg: config.AutomationConfig{
			Filter: config.AutomationFilterConfig{
				MatchMode: automation.MatchMode,
				States:    automation.States,
				LabelsAny: automation.LabelsAny,
			},
		},
		identifierRe:   automation.IdentifierRegex,
		inputContextRe: automation.InputContextRegex,
	}, inputContext)
}

func replayInputRequiredDispatch(entry *orchestrator.InputRequiredEntry, automation orchestrator.InputRequiredAutomation, now time.Time) orchestrator.AutomationDispatch {
	return orchestrator.AutomationDispatch{
		AutomationID: automation.ID,
		ProfileName:  automation.ProfileName,
		Instructions: automation.Instructions,
		AutoResume:   automation.AutoResume,
		Trigger: orchestrator.AutomationTriggerContext{
			Type:           config.AutomationTriggerInputRequired,
			FiredAt:        now,
			AutomationID:   automation.ID,
			InputContext:   entry.Context,
			BlockedProfile: entry.ProfileName,
			BlockedBackend: entry.Backend,
		},
	}
}
