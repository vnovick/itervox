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

type inputRequiredReplayState struct {
	initialized bool
	issues      map[string]inputRequiredReplayIssueState
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

	detailCache := make(map[string]*domain.Issue)
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

		issue := replayInputRequiredIssueDetail(ctx, tr, detailCache, entry)
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

func replayInputRequiredIssueDetail(ctx context.Context, tr tracker.Tracker, cache map[string]*domain.Issue, entry *orchestrator.InputRequiredEntry) *domain.Issue {
	if entry == nil {
		return nil
	}
	if entry.IssueID != "" {
		if cached, ok := cache[entry.IssueID]; ok {
			return cached
		}
		issue, err := tr.FetchIssueDetail(ctx, entry.IssueID)
		if err != nil {
			slog.Warn("automation: input-required replay detail fetch failed",
				"identifier", entry.Identifier,
				"issue_id", entry.IssueID,
				"error", err)
			cache[entry.IssueID] = nil
			return nil
		}
		cache[entry.IssueID] = issue
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
