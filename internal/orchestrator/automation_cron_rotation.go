package orchestrator

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
)

// CronAutomationBatch is one tick of a cron automation whose filter.limit is
// set (#176). The scheduler goroutine fetches and filters the matching issues;
// the event loop picks which of them this tick serves, because the rotation
// cursor and the "already running / queued for this rule" checks are
// event-loop state.
type CronAutomationBatch struct {
	// Dispatch is the dispatch every selected issue gets. Its
	// Trigger.CurrentState is overwritten with each issue's own state.
	Dispatch AutomationDispatch
	// Candidates are the issues that match the rule this tick, in any order.
	Candidates []domain.Issue
	// Limit caps how many issues the tick serves. Must be > 0.
	Limit int
}

// DispatchCronAutomationBatch hands one limited cron tick to the event loop.
// Safe to call from any goroutine. Returns false when the event loop did not
// accept the batch before ctx was done.
func (o *Orchestrator) DispatchCronAutomationBatch(ctx context.Context, batch CronAutomationBatch) bool {
	select {
	case o.events <- OrchestratorEvent{Type: EventDispatchCronBatch, CronBatch: &batch}:
		return true
	case <-ctx.Done():
		metrics.EventDropped()
		slog.Warn("orchestrator: cron automation batch not accepted before context done",
			"automation", batch.Dispatch.AutomationID,
			"candidates", len(batch.Candidates),
			"error", ctx.Err())
		return false
	}
}

// dispatchCronAutomationBatch serves the next batch.Limit issues of the
// rule's rotation and returns how many dispatches were accepted (started or
// queued). Event loop only.
func (o *Orchestrator) dispatchCronAutomationBatch(ctx context.Context, state *State, batch CronAutomationBatch, now time.Time) int {
	id := batch.Dispatch.AutomationID
	if id == "" || batch.Limit <= 0 {
		return 0
	}
	selected := selectCronRotation(*state, id, batch.Candidates, batch.Limit)
	accepted := 0
	for _, issue := range selected {
		dispatch := batch.Dispatch
		dispatch.Trigger.CurrentState = issue.State
		if !o.dispatchOrQueueAutomation(ctx, state, issue, dispatch, now) {
			continue
		}
		accepted++
		// The cursor follows the last issue actually served. One that was
		// dropped is not marked as served and comes up first next tick.
		if state.CronCursors == nil {
			state.CronCursors = make(map[string]string)
		}
		state.CronCursors[id] = issue.Identifier
	}
	if accepted > 0 {
		slog.Info("automation: accepted cron rotation batch",
			"automation", id, "count", accepted, "candidates", len(batch.Candidates),
			"limit", batch.Limit, "cursor", state.CronCursors[id])
	}
	return accepted
}

// selectCronRotation picks up to limit issues for one tick of automation id.
// Candidates are ordered by identifier; issues already running or queued for
// this automation are left out (they do not use up a slot); the pick starts
// right after the rule's cursor and wraps around. An issue that left the
// filter is simply absent from candidates, so it drops out of the rotation —
// the cursor is a position in identifier order, not a member of the list.
func selectCronRotation(state State, automationID string, candidates []domain.Issue, limit int) []domain.Issue {
	if limit <= 0 {
		return nil
	}
	pool := make([]domain.Issue, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, issue := range candidates {
		if issue.Identifier == "" {
			continue
		}
		if _, dup := seen[issue.Identifier]; dup {
			continue
		}
		seen[issue.Identifier] = struct{}{}
		if cronIssueBusyForAutomation(state, automationID, issue) {
			continue
		}
		pool = append(pool, issue)
	}
	if len(pool) == 0 {
		return nil
	}
	slices.SortFunc(pool, func(a, b domain.Issue) int { return cmp.Compare(a.Identifier, b.Identifier) })

	start := 0
	if cursor := state.CronCursors[automationID]; cursor != "" {
		// First issue after the cursor; none after it wraps to the top.
		start = len(pool)
		for i, issue := range pool {
			if issue.Identifier > cursor {
				start = i
				break
			}
		}
		if start == len(pool) {
			start = 0
		}
	}
	n := min(limit, len(pool))
	out := make([]domain.Issue, 0, n)
	for i := range n {
		out = append(out, pool[(start+i)%len(pool)])
	}
	return out
}

// cronIssueBusyForAutomation reports whether issue already has a run or a
// queued dispatch from this automation.
func cronIssueBusyForAutomation(state State, automationID string, issue domain.Issue) bool {
	for key, run := range state.Running {
		if run == nil || run.AutomationID != automationID {
			continue
		}
		if key == issue.ID || (issue.ID != "" && run.Issue.ID == issue.ID) || run.Issue.Identifier == issue.Identifier {
			return true
		}
	}
	for _, entry := range state.AutomationQueue {
		if entry == nil || entry.AutomationID != automationID {
			continue
		}
		if (issue.ID != "" && entry.Issue.ID == issue.ID) || entry.Issue.Identifier == issue.Identifier {
			return true
		}
	}
	return false
}

// restoreCronCursors copies the persisted cursors, dropping empty keys or
// values a hand-edited file might carry.
func restoreCronCursors(disk map[string]string) map[string]string {
	out := make(map[string]string, len(disk))
	for id, cursor := range disk {
		if id == "" || cursor == "" {
			continue
		}
		out[id] = cursor
	}
	return out
}
