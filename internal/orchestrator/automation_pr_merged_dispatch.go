package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// DispatchPRMergedAutomations is the public entry point invoked from outside
// the event-loop goroutine (e.g. the daemon-side merge_pr success path or a
// future GitHub poller). It snapshots the registered rules, applies the
// filter, and posts an EventDispatchAutomation per match so the event loop
// preserves the single-goroutine state invariant. Safe to call from any
// goroutine. P1.
func (o *Orchestrator) DispatchPRMergedAutomations(ctx context.Context, issue domain.Issue, event PRMergedEvent) {
	rules := o.snapPRMergedAutomations()
	if len(rules) == 0 {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	for _, rule := range rules {
		if sendCtx.Err() != nil {
			return
		}
		if !matchesAutomationFilter(issue, rule.MatchMode, rule.States, rule.LabelsAny, rule.IdentifierRegex, nil, "") {
			continue
		}
		dispatch := AutomationDispatch{
			AutomationID: rule.ID,
			ProfileName:  rule.ProfileName,
			Instructions: rule.Instructions,
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerPRMerged,
				FiredAt:      now,
				AutomationID: rule.ID,
				CurrentState: issue.State,
				PRURL:        event.PRURL,
				PRNumber:     event.PRNumber,
				PRBranch:     event.Branch,
				PRBaseBranch: event.BaseRef,
				MergedSHA:    event.MergedSHA,
				MergedAt:     event.MergedAt,
			},
		}
		select {
		case o.events <- OrchestratorEvent{
			Type:       EventDispatchAutomation,
			Issue:      &issue,
			Automation: &dispatch,
		}:
		case <-sendCtx.Done():
			slog.Warn("orchestrator: pr_merged dispatch event not accepted before context done",
				"identifier", issue.Identifier,
				"automation", rule.ID,
				"pr_url", event.PRURL,
				"error", sendCtx.Err())
			return
		}
	}
}

// PRMergedEvent is the data the daemon hands to
// dispatchMatchingPRMergedAutomations after detecting a merged PR (either
// from the daemon-side merge_pr action's success path or from a future
// poller of externally-merged PRs).
type PRMergedEvent struct {
	PRURL     string
	PRNumber  int
	Branch    string
	BaseRef   string
	MergedSHA string
	MergedAt  time.Time
}

// dispatchMatchingPRMergedAutomations fires every registered pr_merged
// automation whose filter matches the issue. Mirrors
// dispatchMatchingPROpenedAutomations including the issue-level dedup
// ledger (PRMergedDispatched).
func (o *Orchestrator) dispatchMatchingPRMergedAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	event PRMergedEvent,
	now time.Time,
) {
	automations := o.snapPRMergedAutomations()
	if len(automations) == 0 {
		return
	}
	for _, automation := range automations {
		if !matchesAutomationFilter(issue, automation.MatchMode, automation.States, automation.LabelsAny, automation.IdentifierRegex, nil, "") {
			continue
		}
		key := prMergedDedupKey(issue.Identifier, event.PRURL, automation.ID)
		if _, alreadyFired := state.PRMergedDispatched[key]; alreadyFired {
			state.AutomationDroppedPRMergedDedupTotal++
			continue
		}
		dispatch := AutomationDispatch{
			AutomationID: automation.ID,
			ProfileName:  automation.ProfileName,
			Instructions: automation.Instructions,
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerPRMerged,
				FiredAt:      now,
				AutomationID: automation.ID,
				CurrentState: issue.State,
				PRURL:        event.PRURL,
				PRNumber:     event.PRNumber,
				PRBranch:     event.Branch,
				PRBaseBranch: event.BaseRef,
				MergedSHA:    event.MergedSHA,
				MergedAt:     event.MergedAt,
			},
		}
		o.dispatchOrQueueAutomation(ctx, state, issue, dispatch, now)
		if state.PRMergedDispatched == nil {
			state.PRMergedDispatched = make(map[string]struct{})
		}
		state.PRMergedDispatched[key] = struct{}{}
		state.AutomationDispatchesPRMergedTotal++
	}
}

func prMergedDedupKey(identifier, prURL, automationID string) string {
	return identifier + "|" + prURL + "|" + automationID
}
