package orchestrator

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/rules"
)

// RunPhase is where a run is in its lifecycle (#125):
//
//	dispatched → running → finishing → exited
//
// The event loop owns it. A run is dispatched when its Running entry is
// created, running once its worker reports progress, finishing once the worker
// has started to wrap up (it is about to move the issue to the completion
// state, or is sending its exit), and exited when the loop has handled its
// exit. Reconcile and stall detection leave a finishing run to its own exit.
type RunPhase string

// Run lifecycle phases.
const (
	RunDispatched RunPhase = "dispatched"
	RunRunning    RunPhase = "running"
	RunFinishing  RunPhase = "finishing"
	RunExited     RunPhase = "exited"
)

// runHandle is what a run's worker shares with the event loop. The worker
// reports "finishing" here rather than by event because the report must be
// visible to the very tick whose tracker fetch sees the issue it just moved:
// an event could still be queued behind that tick.
type runHandle struct {
	id        string
	kind      string
	finishing atomic.Bool
	// rules is the rule set the run was dispatched with (#157), resolved by
	// startRun from the rules files on disk. Set before the worker starts and
	// never changed, so the worker reads it without a lock.
	rules rules.Set
	// usage is the run's latest cumulative usage, recorded by its worker
	// after every turn whatever the turn's outcome (#136). The worker's exit
	// carries it, so the event loop settles a run's final usage even when
	// the turn failed or its last progress update was dropped.
	usage atomic.Pointer[runUsage]
	// steer carries operator messages from the event loop to the worker
	// (#178). The loop only sends (non-blocking); the worker, or its runner
	// during a live turn, receives.
	steer chan agent.SteerMessage
}

// runUsage is a run's cumulative usage at the end of its latest turn (#136).
type runUsage struct {
	input, cached, output int
	// cost is the latest session-cumulative cost a turn reported, and
	// session the agent session it belongs to; nil when no turn reported one.
	cost    *float64
	session string
}

// recordRunUsage records a run's usage after a turn: the tokens of the turns
// before it plus the turn's own, and the turn's cost when it reported one
// (otherwise the last reported cost stands). Worker only.
func recordRunUsage(ctx context.Context, input, cached, output int, cost *float64, session string) {
	h := runFrom(ctx)
	if h == nil {
		return
	}
	u := &runUsage{input: input, cached: cached, output: output}
	if cost != nil {
		c := *cost
		u.cost, u.session = &c, session
	} else if prev := h.usage.Load(); prev != nil {
		u.cost, u.session = prev.cost, prev.session
	}
	h.usage.Store(u)
}

// settleRunUsage puts the run's final usage on its exit entry (#136). The
// event loop accounts it against what the run's progress updates already
// added, so a usage that was already counted adds nothing. Worker only.
func settleRunUsage(ctx context.Context, exit *RunEntry) {
	h := runFrom(ctx)
	if h == nil || exit == nil {
		return
	}
	u := h.usage.Load()
	if u == nil {
		return
	}
	exit.InputTokens, exit.OutputTokens = u.input, u.output
	exit.TotalTokens = u.input + u.cached + u.output
	exit.CostUSD, exit.AgentSessionID = u.cost, u.session
}

type runHandleKey struct{}

// RunRecord is what the event loop keeps about an issue's latest run (#127).
// It outlives the run's Running entry, which reconcile and stall detection
// delete before the run's own exit arrives, so the exit handler can still
// tell which run finished and what it was: its kind, its profile (a
// reviewer's chain step), and whether it was stopped. Event-loop state.
type RunRecord struct {
	ID          string
	Identifier  string
	Kind        string
	ProfileName string
	// Stopped is set when reconcile or stall detection stopped the run: its
	// work was cut short, so a success exit that follows does not start a
	// review.
	Stopped bool
	// Exited is set once the run's own exit has been handled.
	Exited bool
	// RulesHash / RuleIDs name the rule set the run used (#157).
	RulesHash string
	RuleIDs   []string
}

// IsReviewer reports whether the recorded run was a reviewer.
func (r RunRecord) IsReviewer() bool { return r.Kind == "reviewer" }

// startRun gives a new Running entry its run identity, records it as the
// issue's latest run, and returns the worker context that carries it. Event
// loop only.
func (o *Orchestrator) startRun(ctx context.Context, state *State, entry *RunEntry) context.Context {
	h := &runHandle{id: generateRunID(), kind: entry.Kind, steer: make(chan agent.SteerMessage, steerInboxSize)}
	// #157: the rules files are read again for every run, so an edit applies
	// to the next dispatch without a restart.
	h.rules = o.resolveRunRules(entry.ProfileName, entry.Backend)
	used := h.rules.Record()
	entry.RulesHash, entry.RuleIDs = used.Hash, used.IDs
	if state != nil {
		if state.LatestRun == nil {
			state.LatestRun = map[string]RunRecord{}
		}
		state.LatestRun[entry.Issue.ID] = RunRecord{
			ID: h.id, Identifier: entry.Issue.Identifier, Kind: entry.Kind, ProfileName: entry.ProfileName,
			RulesHash: used.Hash, RuleIDs: used.IDs,
		}
	}
	entry.RunID = h.id
	entry.SessionID = h.id
	entry.Phase = RunDispatched
	entry.run = h
	return context.WithValue(ctx, runHandleKey{}, h)
}

// runFrom returns the run a worker context belongs to, or nil when the worker
// was started without one (tests that call runWorker directly).
func runFrom(ctx context.Context) *runHandle {
	h, _ := ctx.Value(runHandleKey{}).(*runHandle)
	return h
}

// runIDFrom returns the ID of the run a worker context belongs to, or "".
func runIDFrom(ctx context.Context) string {
	if h := runFrom(ctx); h != nil {
		return h.id
	}
	return ""
}

// runIsReviewer reports whether a worker context belongs to a reviewer run.
func runIsReviewer(ctx context.Context) bool {
	h := runFrom(ctx)
	return h != nil && h.kind == "reviewer"
}

// reviewerKind is the RunEntry.Kind of a run that is, or is not, a reviewer.
func reviewerKind(reviewer bool) string {
	if reviewer {
		return "reviewer"
	}
	return ""
}

// reviewerProfileOf is the profile a run continuing rec runs as a reviewer,
// "" when rec was not a reviewer (#127).
func reviewerProfileOf(rec RunRecord) string {
	if rec.IsReviewer() {
		return rec.ProfileName
	}
	return ""
}

// retryAs makes the issue's pending retry re-run a reviewer under
// reviewerProfile (#127). No-op for "" or when no retry is pending.
func retryAs(state State, issueID, reviewerProfile string) {
	if r := state.RetryAttempts[issueID]; r != nil && reviewerProfile != "" {
		r.ReviewerProfile = reviewerProfile
	}
}

// markFinishing reports that the worker's run has started to finish. Safe to
// call from the worker goroutine; a no-op without a run.
func markFinishing(ctx context.Context) {
	if h := runFrom(ctx); h != nil {
		h.finishing.Store(true)
	}
}

// observeFinishing reports whether the run is finishing, folding a report from
// its worker into Phase. Event loop only.
func (e *RunEntry) observeFinishing() bool {
	if e.Phase != RunFinishing && e.run != nil && e.run.finishing.Load() {
		e.Phase = RunFinishing
	}
	return e.Phase == RunFinishing
}

// isStaleFor reports whether an event naming runID belongs to a run other than
// this one. An empty ID on either side matches (entries and events built by
// hand in tests carry none).
func (e *RunEntry) isStaleFor(runID string) bool {
	return runID != "" && e.RunID != "" && e.RunID != runID
}

// supersededRun reports whether exit ev belongs to a run other than the
// issue's current one, and names that current run. The current run is the
// live Running entry when there is one, else the issue's latest dispatched
// run: a replacement that has already exited (and left a retry or a pending
// review behind) must not be overwritten by the late exit of the run it
// replaced. Events and entries without a run ID match (tests).
func supersededRun(state State, live *RunEntry, ev OrchestratorEvent) (string, bool) {
	if live != nil {
		return live.RunID, live.isStaleFor(ev.RunID)
	}
	latest := state.LatestRun[ev.IssueID].ID
	return latest, ev.RunID != "" && latest != "" && latest != ev.RunID
}

// finishedRun identifies the run exit ev belongs to (#127): the live Running
// entry when there is one, else the issue's run record. supersededRun has
// already settled that the record, when it carries an ID, is this run. ok is
// false when neither is known (an exit built by hand in a test).
func finishedRun(state State, live *RunEntry, ev OrchestratorEvent) (RunRecord, bool) {
	rec, recorded := state.LatestRun[ev.IssueID]
	if recorded && ev.RunID != "" && rec.ID != "" && rec.ID != ev.RunID {
		recorded = false
	}
	if live != nil {
		return RunRecord{
			ID: live.RunID, Identifier: live.Issue.Identifier, Kind: live.Kind, ProfileName: live.ProfileName,
			Stopped: recorded && rec.Stopped, Exited: recorded && rec.Exited,
			RulesHash: live.RulesHash, RuleIDs: live.RuleIDs,
		}, true
	}
	return rec, recorded
}

// markRunStopped records that reconcile or stall detection stopped the
// issue's current run, whose Running entry it is about to delete.
func markRunStopped(state State, issueID, runID string) {
	if rec, ok := state.LatestRun[issueID]; ok && (runID == "" || rec.ID == runID) {
		rec.Stopped = true
		state.LatestRun[issueID] = rec
	}
}

// markRunExited records that the run's own exit has been handled.
func markRunExited(state State, issueID, runID string) {
	if rec, ok := state.LatestRun[issueID]; ok && (runID == "" || rec.ID == runID) {
		rec.Exited = true
		state.LatestRun[issueID] = rec
	}
}

// handleStaleExit settles the exit of a run that is no longer its issue's
// current run (#125): reconcile or stall detection stopped it and the issue
// was dispatched again before its exit arrived. The stopper already released
// the claim and scheduled any retry, so the exit changes no state; a stall is
// still recorded in history, which its own exit would otherwise do.
func (o *Orchestrator) handleStaleExit(ev OrchestratorEvent, currentRunID string) {
	reason := TerminalReason("")
	if ev.RunEntry != nil {
		reason = ev.RunEntry.TerminalReason
	}
	slog.Info("orchestrator: ignoring the exit of a superseded run",
		"issue_id", ev.IssueID, "run_id", ev.RunID, "current_run_id", currentRunID, "reason", reason)
	if reason == TerminalStalled {
		o.recordHistory(ev.RunEntry, ev.RunEntry.Issue, time.Now(), "stalled")
	}
}
