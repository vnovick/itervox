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

	// Collapse this tick's detail reads into one request where the tracker
	// supports it (issue #42: the input-required replay was ~30% of the
	// measured budget — the single largest consumer).
	//
	// The batch is one request regardless of how many ids it carries, so this
	// deliberately does not try to predict exactly which entries will reach
	// the fetch below. It seeds the same-tick cache; entries the loop then
	// skips simply leave a warm cache for the next tick, guarded by the same
	// blockedKey and TTL checks as any other cached fetch.
	//
	// CRITICAL: only ids the batch actually returned are seeded. An id omitted
	// from the response is not proof of anything, and seeding a nil issue
	// would make replayInputRequiredIssueDetail return nil from its same-tick
	// cache WITHOUT falling back to FetchIssueDetail — silently skipping the
	// issue's automations instead of replaying them.
	if len(automations) > 0 {
		replayIDs := make([]string, 0, len(identifiers))
		for _, identifier := range identifiers {
			e := snap.InputRequiredIssues[identifier]
			if e == nil || e.IssueID == "" {
				continue
			}
			// Only ids the loop would actually spend a request on. Prefetching
			// cache hits would cost one request on every steady-state tick —
			// a backlog sitting blocked with its rules already fired costs
			// ZERO today, and must keep costing zero.
			if _, hit := replayDetailCached(prev.details, next.details, e.IssueID, inputRequiredReplayKey(e), now); hit {
				continue
			}
			replayIDs = append(replayIDs, e.IssueID)
		}
		if prefetched := tracker.PrefetchDetails(ctx, tr, uniqueIssueIDs(replayIDs)); len(prefetched) > 0 {
			for _, identifier := range identifiers {
				entry := snap.InputRequiredIssues[identifier]
				if entry == nil || entry.IssueID == "" {
					continue
				}
				issue, ok := prefetched[entry.IssueID]
				if !ok || issue == nil {
					continue // absent from the batch — leave the fallback path intact
				}
				next.details[entry.IssueID] = inputRequiredDetailCacheEntry{
					issue:      issue,
					blockedKey: inputRequiredReplayKey(entry),
					fetchedAt:  now,
				}
			}
		}
	}

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

// uniqueIssueIDs de-duplicates while preserving order. Two input-required
// entries can name the same issue, and sending an id twice in the batch filter
// wastes filter width for no extra data.
func uniqueIssueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

// replayDetailCached reports whether the two-tier cache already answers this
// entry, and with what.
//
// It is the SINGLE source of truth for that question, consulted both by
// replayInputRequiredIssueDetail below and by the batch prefetch in
// replayInputRequiredAutomations. Those two must agree exactly: if the prefetch
// thought a fetch was needed when the resolver did not, every steady-state tick
// would spend a request the cache was about to answer for free — which is the
// per-tick waste issue #42 was filed about, reintroduced through the fix for it.
//
// Same tick (next) honours ANY resolution including a failure, so one bad issue
// costs at most one request per tick. An earlier tick (prev) is reused only for
// a successful fetch taken under the same blocked context and still inside the
// TTL; a new question comment moves blockedKey and forces a refetch.
func replayDetailCached(
	prev, next map[string]inputRequiredDetailCacheEntry,
	issueID, blockedKey string,
	now time.Time,
) (*domain.Issue, bool) {
	if cached, ok := next[issueID]; ok {
		return cached.issue, true
	}
	if cached, ok := prev[issueID]; ok &&
		cached.issue != nil &&
		cached.blockedKey == blockedKey &&
		now.Sub(cached.fetchedAt) < inputRequiredDetailTTL {
		return cached.issue, true
	}
	return nil, false
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
		if cached, hit := replayDetailCached(prev, next, entry.IssueID, blockedKey, now); hit {
			// A prev-tick hit is promoted into next so the following tick
			// sees it as a same-tick resolution.
			if _, sameTick := next[entry.IssueID]; !sameTick {
				next[entry.IssueID] = prev[entry.IssueID]
			}
			return cached
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
