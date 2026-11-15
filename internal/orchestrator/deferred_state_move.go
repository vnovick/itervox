package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// #177 — an itervox-originated state move never stops the worker it concerns.
//
// An agent opens its PR and then, still inside its run, moves its own issue
// (the move_state agent action) — typically to tracker.completion_state. The
// move used to land in the tracker at once, so a reconcile tick that ran
// before the worker's own exit saw the issue terminal (or no longer active)
// and cancelled the worker as if a person had closed the issue. The worker
// then never posted its post-PR tracker comment, never wrote its handoff and
// never reported the PR, so pr_opened did not fire either.
//
// Such a move is now deferred while the issue has a running worker that
// reconciliation would stop for it. The event loop records it in
// State.DeferredStateMoves (persisted to deferred_state_moves.json), flags the
// run so its turn loop ends exactly as it would on a landed move, and applies
// it once the run has exited — after the worker's post-run work. Until then
// the tracker still shows the issue's active state, so reconciliation leaves
// the worker alone. A person moving the issue (dashboard or tracker) is not
// deferred and still stops the worker.
//
// On the run's exit the move is:
//   - dropped when a person stopped the run (terminate, or reconciliation
//     stopped it because the tracker state changed under it),
//   - dropped when the run succeeded and the worker applied completion_state
//     itself: the worker's completion move is the last word, as it was when
//     the agent's move landed first,
//   - otherwise applied through the write sink, and only while the tracker
//     still shows the state the agent moved the issue from (a later move by
//     anyone else wins).
//
// While a deferred move is pending, normal dispatch and retries of the issue
// are held (state_move_pending), so the issue is not started again between
// the exit and the move. A restart in between loses nothing: the ledger is
// reloaded and applied on the first tick, when no worker is running.

// IneligibleStateMovePending is the dispatch-ineligible reason for an issue
// whose deferred state move has not been applied yet (#177).
const IneligibleStateMovePending = "state_move_pending"

// maxDeferredMoveAttempts bounds how often a failing deferred move is
// retried (once per tick) before it is dropped and reported.
const maxDeferredMoveAttempts = 5

// deferredMoveExitWait is how long a tick waits for the exit of a run that
// reconciliation or stall detection stopped (its Running entry is gone, its
// exit not yet handled) before applying the run's deferred move itself. The
// exit decides: a stop by reconciliation drops the move. The bound only
// matters when that exit event was lost.
const deferredMoveExitWait = 30 * time.Second

// deferredMoveApplyStale is how long an apply may stay in flight before a
// tick assumes its completion event was lost and applies the move again.
const deferredMoveApplyStale = 2 * time.Minute

// Outcomes of one deferred-move apply, carried by EventDeferredStateMoveSettled.
const (
	deferredMoveApplied    = "applied"
	deferredMoveAlready    = "already_in_target"
	deferredMoveSuperseded = "superseded"
	deferredMoveNotFound   = "not_found"
	deferredMoveFailed     = "failed"
)

// DeferredStateMove is an itervox-originated tracker state move held back
// until the issue's running worker has exited (#177). Event-loop state.
type DeferredStateMove struct {
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier"`
	TargetState string `json:"target_state"`
	// FromState is the tracker state the move was requested from. The move
	// is applied only while the tracker still shows it.
	FromState string `json:"from_state,omitempty"`
	// RunID is the run the move waits for.
	RunID       string    `json:"run_id,omitempty"`
	ProfileName string    `json:"profile_name,omitempty"`
	Backend     string    `json:"backend,omitempty"`
	WorkerHost  string    `json:"worker_host,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	// Attempts counts failed applies.
	Attempts int `json:"attempts,omitempty"`

	// applyingSince is set while an apply is in flight. Not persisted: after
	// a restart nothing is in flight.
	applyingSince time.Time
	// exitWaitSince is when a tick first found the move's run stopped but its
	// exit not yet handled. Not persisted.
	exitWaitSince time.Time
}

func (m DeferredStateMove) sameRequest(o DeferredStateMove) bool {
	return m.RunID == o.RunID && m.RequestedAt.Equal(o.RequestedAt) && m.TargetState == o.TargetState
}

// SetDeferredStateMovesFile sets the path deferred state moves persist to.
// Must be called before Run.
func (o *Orchestrator) SetDeferredStateMovesFile(path string) {
	o.deferredStateMovesFile = path
}

// DeferAgentStateMove offers a state move an agent requested (the move_state
// action) to the event loop. It returns true when the move was deferred until
// the issue's running worker exits; the caller must then NOT write it to the
// tracker. It returns false when the caller should apply the move itself: no
// worker is running on the issue, the move would not stop it, or the loop is
// not running. Safe from any goroutine; blocks until the loop has decided.
func (o *Orchestrator) DeferAgentStateMove(ctx context.Context, move DeferredStateMove) bool {
	if move.IssueID == "" || move.Identifier == "" || strings.TrimSpace(move.TargetState) == "" {
		return false
	}
	done := o.loopExitedCh()
	if done == nil {
		return false // the loop has not started: nothing runs, nothing to defer for
	}
	reply := make(chan bool, 1)
	ev := OrchestratorEvent{Type: EventDeferStateMove, IssueID: move.IssueID, Identifier: move.Identifier,
		DeferredMove: &move, deferReply: reply}
	select {
	case o.events <- ev:
	case <-done:
		return false
	case <-ctx.Done():
		return false
	}
	select {
	case deferred := <-reply:
		return deferred
	case <-done:
		// The loop may have decided just before it exited.
		select {
		case deferred := <-reply:
			return deferred
		default:
			return false
		}
	}
}

// moveWouldStopRun reports whether reconciliation would stop run entry when
// its issue reached target: the mirror of ReconcileTrackerStates' rules.
func moveWouldStopRun(entry *RunEntry, target string, state State) bool {
	switch {
	case entry.Kind == "automation":
		return false // automation runs are never stopped for their issue's state
	case entry.Kind == "reviewer" && isReviewState(target, state):
		return false
	case isActiveState(target, state):
		return false
	}
	return true
}

// handleDeferStateMove decides an EventDeferStateMove. Event loop only.
func (o *Orchestrator) handleDeferStateMove(state *State, ev OrchestratorEvent, now time.Time) bool {
	if ev.DeferredMove == nil {
		return false
	}
	move := *ev.DeferredMove
	live := state.Running[move.IssueID]
	if live == nil {
		return false
	}
	if !moveWouldStopRun(live, move.TargetState, *state) {
		return false
	}
	move.RunID = live.RunID
	move.ProfileName = live.ProfileName
	move.Backend = live.Backend
	move.WorkerHost = live.WorkerHost
	move.RequestedAt = now
	move.Attempts = 0
	move.applyingSince = time.Time{}
	if state.DeferredStateMoves == nil {
		state.DeferredStateMoves = make(map[string]DeferredStateMove)
	}
	state.DeferredStateMoves[move.Identifier] = move
	if live.run != nil {
		live.run.stateMoveDeferred.Store(true)
	}
	o.saveDeferredStateMoves(state)
	slog.Info("orchestrator: state move deferred until the running worker exits",
		"identifier", move.Identifier, "target_state", move.TargetState, "run_id", move.RunID)
	if o.logBuf != nil {
		o.logBuf.Add(move.Identifier, makeBufLine("INFO",
			"worker: move to \""+move.TargetState+"\" deferred until this run finishes"))
	}
	return true
}

// settleDeferredMoveOnExit runs for every exit the event loop handles as the
// issue's current run, right after its Running entry was removed. Event loop
// only.
func (o *Orchestrator) settleDeferredMoveOnExit(state *State, ev OrchestratorEvent, live *RunEntry) {
	identifier := ""
	for id, m := range state.DeferredStateMoves {
		if m.IssueID == ev.IssueID {
			identifier = id
			break
		}
	}
	if identifier == "" {
		return
	}
	move := state.DeferredStateMoves[identifier]
	if ev.RunID != "" && move.RunID != "" && move.RunID != ev.RunID {
		return // waits for a different run
	}
	// A person stopped the run: an operator terminated it, or
	// reconciliation stopped it because the tracker state changed under it
	// (a nil RunEntry is reconcile's not-found / non-active stop).
	var reason TerminalReason
	if ev.RunEntry != nil {
		reason = ev.RunEntry.TerminalReason
	}
	o.userTerminatedMu.Lock()
	_, terminated := o.userTerminatedIDs[identifier]
	o.userTerminatedMu.Unlock()
	stoppedByPerson := ev.RunEntry == nil || reason == TerminalCanceledByReconciliation || terminated
	completionApplied := live != nil && live.run != nil && live.run.completionApplied.Load()
	switch {
	case stoppedByPerson:
		o.dropDeferredStateMove(state, identifier, "the run was stopped by a state change or an operator")
	case reason == TerminalSucceeded && completionApplied:
		o.dropDeferredStateMove(state, identifier, "the worker applied completion_state itself")
	default:
		o.applyDeferredStateMove(state, identifier, time.Now())
	}
}

func (o *Orchestrator) dropDeferredStateMove(state *State, identifier, why string) {
	move := state.DeferredStateMoves[identifier]
	delete(state.DeferredStateMoves, identifier)
	o.saveDeferredStateMoves(state)
	slog.Info("orchestrator: deferred state move dropped",
		"identifier", identifier, "target_state", move.TargetState, "reason", why)
}

// applyPendingDeferredStateMoves applies every deferred move whose issue has
// no running worker: moves loaded after a restart, retries of failed applies,
// and applies whose completion event was lost. Called once per tick. Event
// loop only.
func (o *Orchestrator) applyPendingDeferredStateMoves(state *State, now time.Time) {
	for identifier, move := range state.DeferredStateMoves {
		if _, running := state.Running[move.IssueID]; running {
			continue
		}
		if !move.applyingSince.IsZero() && now.Sub(move.applyingSince) < deferredMoveApplyStale {
			continue
		}
		// The run was stopped and its exit is still on its way: that exit
		// settles the move (a stop by reconciliation drops it).
		if rec, ok := state.LatestRun[move.IssueID]; ok && move.RunID != "" && rec.ID == move.RunID && !rec.Exited {
			if move.exitWaitSince.IsZero() {
				move.exitWaitSince = now
				state.DeferredStateMoves[identifier] = move
			}
			if now.Sub(move.exitWaitSince) < deferredMoveExitWait {
				continue
			}
		}
		o.applyDeferredStateMove(state, identifier, now)
	}
}

// applyDeferredStateMove starts the tracker write of a deferred move on a
// tracked goroutine. The goroutine never touches State: its outcome comes
// back as EventDeferredStateMoveSettled. Event loop only.
func (o *Orchestrator) applyDeferredStateMove(state *State, identifier string, now time.Time) {
	move, ok := state.DeferredStateMoves[identifier]
	if !ok {
		return
	}
	move.applyingSince = now
	state.DeferredStateMoves[identifier] = move
	goSafe(&o.discardWg, "deferred-state-move", identifier, func() {
		outcome, current, err := o.writeDeferredStateMove(move)
		settled := move
		o.sendDeferredMoveSettled(OrchestratorEvent{
			Type: EventDeferredStateMoveSettled, IssueID: move.IssueID, Identifier: identifier,
			DeferredMove: &settled, Message: outcome, Error: err, deferFromState: current,
		})
	}, o.withPanicFailure("deferred-state-move", identifier, nil))
}

// sendDeferredMoveSettled delivers an apply's outcome to the loop, bounded:
// a lost event leaves the move in place, and a later tick applies it again
// (deferredMoveApplyStale). Off-loop.
func (o *Orchestrator) sendDeferredMoveSettled(ev OrchestratorEvent) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case o.events <- ev:
	case <-o.loopExitedCh(): // nil before Run: never ready
	case <-timer.C:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: deferred state move outcome not delivered; a later tick retries it",
			"identifier", ev.Identifier, "outcome", ev.Message)
	}
}

// writeDeferredStateMove reads the issue's current state and writes the move
// when the tracker still shows the state it was requested from. Worker
// goroutine (off-loop): touches only the tracker and the write sink.
func (o *Orchestrator) writeDeferredStateMove(move DeferredStateMove) (outcome, current string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	issues, err := o.tracker.FetchIssueStatesByIDs(ctx, []string{move.IssueID})
	if err != nil {
		return deferredMoveFailed, "", err
	}
	for _, iss := range issues {
		if iss.ID == move.IssueID {
			current = iss.State
		}
	}
	switch {
	case current == "":
		return deferredMoveNotFound, "", nil
	case strings.EqualFold(current, move.TargetState):
		return deferredMoveAlready, current, nil
	case move.FromState != "" && !strings.EqualFold(current, move.FromState):
		return deferredMoveSuperseded, current, nil
	}
	if err := o.writeSink().UpdateIssueState(ctx, move.IssueID, move.Identifier, move.TargetState, current); err != nil {
		return deferredMoveFailed, current, err
	}
	return deferredMoveApplied, current, nil
}

// handleDeferredStateMoveSettled records the outcome of one apply. Event
// loop only.
func (o *Orchestrator) handleDeferredStateMoveSettled(state *State, ev OrchestratorEvent, now time.Time) {
	if ev.DeferredMove == nil {
		return
	}
	cur, ok := state.DeferredStateMoves[ev.Identifier]
	if !ok || !cur.sameRequest(*ev.DeferredMove) {
		return // replaced by a newer move, or already settled
	}
	if ev.Message == deferredMoveFailed {
		cur.Attempts++
		cur.applyingSince = time.Time{}
		if cur.Attempts < maxDeferredMoveAttempts {
			state.DeferredStateMoves[ev.Identifier] = cur
			o.saveDeferredStateMoves(state)
			slog.Warn("orchestrator: deferred state move failed, retrying next tick",
				"identifier", ev.Identifier, "target_state", cur.TargetState, "attempt", cur.Attempts, "error", ev.Error)
			return
		}
		slog.Error("orchestrator: deferred state move failed, giving up",
			"identifier", ev.Identifier, "target_state", cur.TargetState, "attempts", cur.Attempts, "error", ev.Error)
		msg := "deferred move to " + cur.TargetState + " failed"
		if ev.Error != nil {
			msg += ": " + ev.Error.Error()
		}
		appendRecentFailure(state, FailureRecord{
			Kind: FailureKindTrackerWrite, Identifier: ev.Identifier, Source: "deferred_state_move", Message: msg,
		}, now)
	}
	delete(state.DeferredStateMoves, ev.Identifier)
	o.saveDeferredStateMoves(state)
	switch ev.Message {
	case deferredMoveApplied:
		slog.Info("orchestrator: deferred state move applied",
			"identifier", ev.Identifier, "from_state", ev.deferFromState, "target_state", cur.TargetState)
		appendIssueStatusChange(state, IssueStatusChange{
			Identifier:  ev.Identifier,
			FromState:   ev.deferFromState,
			ToState:     cur.TargetState,
			Source:      StatusSourceWorkerLifecycle,
			ProfileName: cur.ProfileName,
			Backend:     cur.Backend,
			WorkerHost:  cur.WorkerHost,
			At:          now,
		})
	case deferredMoveSuperseded:
		slog.Info("orchestrator: deferred state move dropped: the issue was moved since",
			"identifier", ev.Identifier, "target_state", cur.TargetState, "current_state", ev.deferFromState)
	case deferredMoveAlready, deferredMoveNotFound:
		slog.Info("orchestrator: deferred state move settled without a write",
			"identifier", ev.Identifier, "target_state", cur.TargetState, "outcome", ev.Message)
	}
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// saveDeferredStateMoves persists a clone of the deferred moves. Event loop.
func (o *Orchestrator) saveDeferredStateMoves(state *State) {
	path := o.deferredStateMovesFile
	if path == "" {
		return
	}
	data, err := json.Marshal(maps.Clone(state.DeferredStateMoves))
	if err != nil {
		slog.Warn("orchestrator: failed to marshal deferred state moves", "error", err)
		return
	}
	o.persistLedger(ledgerDeferredStateMoves, path, data, 0o644)
}

// loadDeferredStateMovesFromDisk restores deferred moves at startup. A
// missing file is normal; a corrupt one is logged and ignored.
func (o *Orchestrator) loadDeferredStateMovesFromDisk(state State) State {
	path := o.deferredStateMovesFile
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("orchestrator: failed to read deferred state moves", "path", path, "error", err)
		}
		return state
	}
	var disk map[string]DeferredStateMove
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse deferred state moves", "path", path, "error", err)
		return state
	}
	if state.DeferredStateMoves == nil {
		state.DeferredStateMoves = make(map[string]DeferredStateMove, len(disk))
	}
	for id, move := range disk {
		if id == "" || move.IssueID == "" || move.TargetState == "" {
			continue
		}
		move.Identifier = id
		state.DeferredStateMoves[id] = move
	}
	if len(state.DeferredStateMoves) > 0 {
		slog.Info("orchestrator: loaded deferred state moves", "path", path, "count", len(state.DeferredStateMoves))
	}
	return state
}

// runStateMoveDeferred reports whether a state move of this run's issue was
// deferred: the turn loop ends as it would on a landed move. Worker only.
func runStateMoveDeferred(ctx context.Context) bool {
	h := runFrom(ctx)
	return h != nil && h.stateMoveDeferred.Load()
}

// markCompletionApplied records that the worker moved its issue to
// completion_state. Worker only.
func markCompletionApplied(ctx context.Context) {
	if h := runFrom(ctx); h != nil {
		h.completionApplied.Store(true)
	}
}
