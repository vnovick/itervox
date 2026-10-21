package orchestrator

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/metrics"
)

// PR-event automations (#173): pr_check_completed, pr_synchronized,
// pr_review_submitted and pr_mergeable_changed fire on GitHub activity on a
// pull request an itervox run recorded (a "managed" PR).
//
// Producers (the GitHub webhook receiver and the managed-PR poll in
// cmd/itervox) call DispatchPREventAutomations off the event loop. It matches
// the rule's issue and PR filters and sends one EventDispatchAutomation per
// matching rule. The event loop then decides, in claimPREventDedup: the PR
// must be managed for that issue, the event must not predate the run that
// recorded the PR, a head SHA or mergeable_state must have changed for the
// rule, and the event key must not have dispatched before.

// ManagedPR is a pull request an itervox run recorded (#173).
type ManagedPR struct {
	PRURL      string `json:"pr_url"`
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Branch     string `json:"branch,omitempty"`
	// Since is when the first run that recorded the PR started. Checks that
	// completed and reviews submitted before it are not dispatched: they
	// predate itervox's work on the PR.
	Since time.Time `json:"since"`
	// OpenedHeadSHA is the head the first recording run left on the remote
	// (empty when it could not be read). It is every pr_synchronized rule's
	// baseline until the rule first evaluates the PR, so a later push is a
	// change even if no poll saw the first head.
	OpenedHeadSHA string `json:"opened_head_sha,omitempty"`
	// RecordedAt is when a run last recorded the PR.
	RecordedAt time.Time `json:"recorded_at"`
}

// PREventAutomation is the compiled form of a PR-event automation rule.
// TriggerType is one of the four PR-event triggers.
type PREventAutomation struct {
	ID              string
	TriggerType     string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	Groups          issuefilter.Filter // #198
	IdentifierRegex *regexp.Regexp
	// CheckNames / Conclusions filter pr_check_completed; ReviewStates /
	// ReviewAuthors filter pr_review_submitted; MergeableStates filters
	// pr_mergeable_changed (evaluated in the event loop, which knows the
	// previous state). Conclusions, ReviewStates and MergeableStates are
	// lower case.
	CheckNames      []string
	Conclusions     []string
	ReviewStates    []string
	ReviewAuthors   []string
	MergeableStates []string
}

// PREvent is one observation of GitHub activity on a PR, from a webhook
// delivery or the managed-PR poll.
type PREvent struct {
	// TriggerType is the PR-event trigger it can fire.
	TriggerType string
	PRURL       string
	PRNumber    int
	Branch      string
	HeadSHA     string
	// pr_check_completed: the check run name or status context, its
	// conclusion (lower case) and when it completed.
	CheckName        string
	CheckConclusion  string
	CheckCompletedAt time.Time
	// MergeableState is the PR's mergeable_state when the producer knows it.
	MergeableState string
	// pr_review_submitted.
	ReviewID          string
	ReviewAuthor      string
	ReviewState       string
	ReviewSubmittedAt time.Time
	// Source is "webhook" or "poll".
	Source string
}

// SetPREventAutomations installs the compiled PR-event rules. Safe to call
// from any goroutine.
func (o *Orchestrator) SetPREventAutomations(rules []PREventAutomation) {
	setAutomationRegistry(o, &o.prEventAutomations, rules)
}

func (o *Orchestrator) snapPREventAutomations() []PREventAutomation {
	return snapAutomationRegistry(o, &o.prEventAutomations)
}

// PREventTriggerTypes reports which PR-event trigger types have at least one
// registered rule, so producers fetch only what a rule can use.
func (o *Orchestrator) PREventTriggerTypes() map[string]bool {
	out := map[string]bool{}
	for _, r := range o.snapPREventAutomations() {
		out[r.TriggerType] = true
	}
	return out
}

// ManagedPRs returns the managed PRs as of the last snapshot. Safe to call
// from any goroutine.
func (o *Orchestrator) ManagedPRs() []ManagedPR {
	o.snapMu.RLock()
	defer o.snapMu.RUnlock()
	out := make([]ManagedPR, 0, len(o.lastSnap.ManagedPRs))
	for _, pr := range o.lastSnap.ManagedPRs {
		out = append(out, pr)
	}
	slices.SortFunc(out, func(a, b ManagedPR) int { return strings.Compare(a.PRURL, b.PRURL) })
	return out
}

// DispatchPREventAutomations sends an EventDispatchAutomation for every
// registered rule of ev.TriggerType whose filters match issue and ev, and
// returns how many it sent. Safe to call from any goroutine; the event loop
// makes the dispatch decision.
func (o *Orchestrator) DispatchPREventAutomations(ctx context.Context, issue domain.Issue, ev PREvent) int {
	rules := o.snapPREventAutomations()
	if len(rules) == 0 {
		return 0
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	sent := 0
	for _, rule := range rules {
		if rule.TriggerType != ev.TriggerType || !prEventFilterMatches(rule, ev) {
			continue
		}
		if !MatchesAutomationFilter(issue, rule.MatchMode, rule.States, rule.LabelsAny, rule.Groups, rule.IdentifierRegex, nil, "") {
			continue
		}
		dispatch := AutomationDispatch{
			AutomationID: rule.ID,
			ProfileName:  rule.ProfileName,
			Instructions: rule.Instructions,
			Trigger:      prEventTriggerContext(rule.ID, issue, ev, now),
		}
		select {
		case o.events <- OrchestratorEvent{Type: EventDispatchAutomation, Issue: &issue, Automation: &dispatch}:
			sent++
		case <-sendCtx.Done():
			metrics.EventDropped()
			slog.Warn("orchestrator: PR-event dispatch not accepted before context done",
				"identifier", issue.Identifier, "automation", rule.ID, "trigger", ev.TriggerType,
				"pr_url", ev.PRURL, "error", sendCtx.Err())
			return sent
		}
	}
	return sent
}

func prEventTriggerContext(automationID string, issue domain.Issue, ev PREvent, now time.Time) AutomationTriggerContext {
	return AutomationTriggerContext{
		Type:              ev.TriggerType,
		FiredAt:           now,
		AutomationID:      automationID,
		CurrentState:      issue.State,
		PRURL:             ev.PRURL,
		PRNumber:          ev.PRNumber,
		PRBranch:          ev.Branch,
		HeadSHA:           ev.HeadSHA,
		CheckName:         ev.CheckName,
		CheckConclusion:   strings.ToLower(ev.CheckConclusion),
		CheckCompletedAt:  ev.CheckCompletedAt,
		MergeableState:    strings.ToLower(ev.MergeableState),
		ReviewID:          ev.ReviewID,
		ReviewAuthor:      ev.ReviewAuthor,
		ReviewState:       strings.ToLower(ev.ReviewState),
		ReviewSubmittedAt: ev.ReviewSubmittedAt,
		EventSource:       ev.Source,
	}
}

// prEventFilterMatches applies the rule's check and review filters to ev.
// mergeable_states is applied in the event loop (prEventMergeableFires).
func prEventFilterMatches(rule PREventAutomation, ev PREvent) bool {
	switch ev.TriggerType {
	case config.AutomationTriggerPRCheckCompleted:
		if len(rule.CheckNames) > 0 && !slices.ContainsFunc(rule.CheckNames, func(n string) bool {
			return strings.EqualFold(strings.TrimSpace(n), ev.CheckName)
		}) {
			return false
		}
		return len(rule.Conclusions) == 0 || slices.Contains(rule.Conclusions, strings.ToLower(ev.CheckConclusion))
	case config.AutomationTriggerPRReviewSubmitted:
		if len(rule.ReviewAuthors) > 0 && !slices.ContainsFunc(rule.ReviewAuthors, func(a string) bool {
			return strings.EqualFold(strings.TrimSpace(a), ev.ReviewAuthor)
		}) {
			return false
		}
		return len(rule.ReviewStates) == 0 || slices.Contains(rule.ReviewStates, strings.ToLower(ev.ReviewState))
	}
	return true
}

// prEventMergeableFires reports whether a mergeable_state move from prev to
// cur fires a rule with the given mergeable_states filter. prev is "" when
// the rule has not evaluated the PR before: then only a conflict fires (a
// fresh PR is not "dirty", so a first sighting of "dirty" is the conflict).
// Without a filter, a move into or out of "dirty" fires.
func prEventMergeableFires(prev, cur string, filter []string) bool {
	if prev == cur {
		return false
	}
	if prev == "" && cur != "dirty" {
		return false
	}
	if len(filter) > 0 {
		return slices.Contains(filter, cur)
	}
	return prev == "dirty" || cur == "dirty"
}

// prEventMaxDispatched bounds PREventDispatched; past it the oldest entries
// are dropped (they belong to PRs long since fixed).
const prEventMaxDispatched = 5000

func prEventBaselineKey(prURL, automationID, kind string) string {
	return prURL + "|" + automationID + "|" + kind
}

// prEventKey is the dedup key of a PR-event dispatch for one rule: the
// event's identity per trigger type (#173) — (pr, head_sha, check_name,
// conclusion) for checks, (pr, head_sha) for pushes, the review id for
// reviews, and (pr, head_sha, mergeable transition) for mergeable changes.
// It starts with the PR URL so pruning by PR is a prefix match.
func prEventKey(automationID string, t AutomationTriggerContext) string {
	parts := []string{t.PRURL, automationID, t.Type}
	switch t.Type {
	case config.AutomationTriggerPRCheckCompleted:
		parts = append(parts, t.HeadSHA, strings.ToLower(t.CheckName), t.CheckConclusion)
	case config.AutomationTriggerPRSynchronized:
		parts = append(parts, t.HeadSHA)
	case config.AutomationTriggerPRReviewSubmitted:
		parts = append(parts, t.ReviewID)
	case config.AutomationTriggerPRMergeableChanged:
		parts = append(parts, t.HeadSHA, t.PreviousMergeableState, t.MergeableState)
	}
	return strings.Join(parts, "|")
}

// claimPREventDedup is the event loop's PR-event (#173) gate. It returns the
// dispatch (with PreviousMergeableState filled in for pr_mergeable_changed)
// and true when it may run; false drops it. Every other trigger passes
// through unchanged.
func (o *Orchestrator) claimPREventDedup(state *State, issue domain.Issue, dispatch AutomationDispatch, now time.Time) (AutomationDispatch, bool) {
	t := dispatch.Trigger
	if !config.IsPREventTrigger(t.Type) {
		return dispatch, true
	}
	mpr, managed := state.ManagedPRs[t.PRURL]
	if t.PRURL == "" || !managed || !strings.EqualFold(mpr.Identifier, issue.Identifier) {
		state.AutomationDroppedPREventUnmanagedTotal++
		slog.Info("orchestrator: PR-event automation skipped (PR not managed by itervox)",
			"identifier", issue.Identifier, "automation", dispatch.AutomationID, "trigger", t.Type, "pr_url", t.PRURL)
		return dispatch, false
	}
	if state.PREventBaselines == nil {
		state.PREventBaselines = make(map[string]string)
	}
	switch t.Type {
	case config.AutomationTriggerPRCheckCompleted:
		if !t.CheckCompletedAt.IsZero() && t.CheckCompletedAt.Before(mpr.Since) {
			return dispatch, false // predates itervox's work on the PR
		}
	case config.AutomationTriggerPRReviewSubmitted:
		if !t.ReviewSubmittedAt.IsZero() && t.ReviewSubmittedAt.Before(mpr.Since) {
			return dispatch, false
		}
	case config.AutomationTriggerPRSynchronized:
		if t.HeadSHA == "" {
			return dispatch, false
		}
		key := prEventBaselineKey(t.PRURL, dispatch.AutomationID, "sync")
		base, seen := state.PREventBaselines[key]
		if !seen {
			base = mpr.OpenedHeadSHA
		}
		state.PREventBaselines[key] = t.HeadSHA
		if base == "" || base == t.HeadSHA {
			return dispatch, false // first sighting, or no new push
		}
	case config.AutomationTriggerPRMergeableChanged:
		cur := t.MergeableState
		if cur == "" || cur == "unknown" {
			return dispatch, false // GitHub has not computed it yet
		}
		key := prEventBaselineKey(t.PRURL, dispatch.AutomationID, "mergeable")
		prev := state.PREventBaselines[key]
		state.PREventBaselines[key] = cur
		var filter []string
		for _, r := range o.snapPREventAutomations() {
			if r.ID == dispatch.AutomationID {
				filter = r.MergeableStates
			}
		}
		if !prEventMergeableFires(prev, cur, filter) {
			return dispatch, false
		}
		dispatch.Trigger.PreviousMergeableState = prev
	}
	key := prEventKey(dispatch.AutomationID, dispatch.Trigger)
	if state.PREventDispatched == nil {
		state.PREventDispatched = make(map[string]time.Time)
	}
	if _, fired := state.PREventDispatched[key]; fired {
		state.AutomationDroppedPREventDedupTotal++
		slog.Info("orchestrator: PR-event automation skipped (dedup)",
			"identifier", issue.Identifier, "automation", dispatch.AutomationID, "trigger", t.Type,
			"pr_url", t.PRURL, "head_sha", t.HeadSHA)
		return dispatch, false
	}
	state.PREventDispatched[key] = now
	evictOldestPREvents(state.PREventDispatched, prEventMaxDispatched)
	state.AutomationDispatchesPREventTotal++
	return dispatch, true
}

// evictOldestPREvents drops the oldest entries until m holds at most limit.
func evictOldestPREvents(m map[string]time.Time, limit int) {
	for len(m) > limit {
		var oldestKey string
		var oldest time.Time
		for k, at := range m {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = k, at
			}
		}
		delete(m, oldestKey)
	}
}

// recordManagedPR records the PR a finished run's exit carries (#173). live
// is the run's Running entry (nil when reconcile already removed it). A run
// that a pr_synchronized rule dispatched advances that rule's baseline to the
// head it left on the remote, so its own push does not fire it again. Event
// loop only.
func recordManagedPR(state *State, live, exit *RunEntry, now time.Time) {
	if exit == nil || exit.PRURL == "" {
		return
	}
	if state.ManagedPRs == nil {
		state.ManagedPRs = make(map[string]ManagedPR)
	}
	since := now
	if live != nil && !live.StartedAt.IsZero() {
		since = live.StartedAt
	}
	pr, known := state.ManagedPRs[exit.PRURL]
	if !known || since.Before(pr.Since) {
		pr.Since = since
	}
	if pr.OpenedHeadSHA == "" {
		pr.OpenedHeadSHA = exit.HeadSHA
	}
	pr.PRURL = exit.PRURL
	pr.IssueID = exit.Issue.ID
	pr.Identifier = exit.Issue.Identifier
	if exit.BranchName != "" {
		pr.Branch = exit.BranchName
	}
	pr.RecordedAt = now
	state.ManagedPRs[exit.PRURL] = pr
	if live != nil && live.TriggerType == config.AutomationTriggerPRSynchronized && live.AutomationID != "" && exit.HeadSHA != "" {
		if state.PREventBaselines == nil {
			state.PREventBaselines = make(map[string]string)
		}
		state.PREventBaselines[prEventBaselineKey(exit.PRURL, live.AutomationID, "sync")] = exit.HeadSHA
		// The poll may have seen that push while the run was still going,
		// queueing the rule behind it (already_running): drop that entry too.
		for _, entry := range sortedAutomationQueue(*state) {
			if entry.AutomationID == live.AutomationID && entry.Trigger.Type == config.AutomationTriggerPRSynchronized &&
				entry.Trigger.PRURL == exit.PRURL && entry.Trigger.HeadSHA == exit.HeadSHA {
				removeAutomationQueueEntry(state, entry.ID)
				state.AutomationDropsSelfReentryTotal++
			}
		}
	}
}

// pruneManagedPRs drops the managed PRs of issues keep rejects, with their
// dedup and baseline entries. Event loop only.
func pruneManagedPRs(state *State, keep func(ident string) bool) {
	for url, pr := range state.ManagedPRs {
		if keep(pr.Identifier) {
			continue
		}
		delete(state.ManagedPRs, url)
		prefix := url + "|"
		for k := range state.PREventDispatched {
			if strings.HasPrefix(k, prefix) {
				delete(state.PREventDispatched, k)
			}
		}
		for k := range state.PREventBaselines {
			if strings.HasPrefix(k, prefix) {
				delete(state.PREventBaselines, k)
			}
		}
	}
}
