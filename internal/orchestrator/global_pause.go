package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// #213 — pause and resume all dispatch.
//
// The pause is State.DispatchPaused, set and cleared only by the event loop
// (EventSetDispatchPause from PauseDispatch / ResumeDispatch, and the expiry
// at its Until). It stops ADMISSION, nothing else: running agents keep
// running, their exits are handled as usual, and every admission path holds
// its work instead of dropping it:
//   - tick dispatch, pending-input resumes, automations and the
//     pending-review replay through AvailableSlots, which reports 0 while
//     paused (picks wait, replies stay queued, automations queue with
//     reason dispatch_paused);
//   - fireRetries keeps a due retry due (no tracker read, no attempt spent);
//   - dispatchRun refuses and keeps the issue's claim as a retry due at once,
//     covering the paths that do not consult AvailableSlots (the
//     backend_fallback re-dispatch);
//   - dispatchReviewer records a PendingReview (the drain's mechanism),
//     which resumePendingReviews dispatches once the pause ends.
//
// The pause persists to dispatch_paused.json through the ledger writer, so a
// restart or a WORKFLOW.md reload stays paused; a pause whose Until has
// passed ends on the first tick after it.

// DispatchPause is the daemon-wide dispatch pause.
type DispatchPause struct {
	Active bool `json:"active"`
	// Reason is the operator's redacted reason, if any.
	Reason string `json:"reason,omitempty"`
	// Until is when the pause ends by itself; zero = until resumed.
	Until time.Time `json:"until,omitzero"`
	// PausedAt is when the pause began.
	PausedAt time.Time `json:"pausedAt,omitzero"`
}

// EventSetDispatchPause pauses or resumes all dispatch (#213).
const EventSetDispatchPause EventType = "SetDispatchPause"

// IneligibleDispatchPaused is the ineligible (and automation queue) reason
// while all dispatch is paused.
const IneligibleDispatchPaused = "dispatch_paused"

// ErrPauseUntilPast is returned by PauseDispatch for an until that is not in
// the future.
var ErrPauseUntilPast = errors.New("orchestrator: pause until must be in the future")

// errLoopNotRunning is returned by the loop round-trips before Run started
// or after it returned.
var errLoopNotRunning = errors.New("orchestrator: event loop is not running")

type dispatchPauseRequest struct {
	pause  bool
	reason string
	until  time.Time
	reply  chan DispatchPause
}

// dispatchPauseFileVersion is the dispatch_paused.json envelope version.
const dispatchPauseFileVersion = 1

type dispatchPauseEnvelope struct {
	Version int `json:"version"`
	DispatchPause
}

// SetDispatchPauseFile sets where the dispatch pause is persisted (#213).
// Must be called before Run; empty disables persistence.
func (o *Orchestrator) SetDispatchPauseFile(path string) {
	o.dispatchPauseFile = path
}

// PauseDispatch pauses all dispatch until ResumeDispatch, or until until
// when it is non-zero. Re-pausing an active pause updates its reason and
// until. Safe from any goroutine; returns the pause the loop applied.
func (o *Orchestrator) PauseDispatch(ctx context.Context, reason string, until time.Time) (DispatchPause, error) {
	if !until.IsZero() && !until.After(time.Now()) {
		return DispatchPause{}, ErrPauseUntilPast
	}
	return o.setDispatchPause(ctx, dispatchPauseRequest{pause: true, reason: reason, until: until})
}

// ResumeDispatch ends the dispatch pause (a no-op when none is active).
// Safe from any goroutine; returns the (inactive) pause.
func (o *Orchestrator) ResumeDispatch(ctx context.Context) (DispatchPause, error) {
	return o.setDispatchPause(ctx, dispatchPauseRequest{})
}

// setDispatchPause sends the request to the loop and waits for its answer.
func (o *Orchestrator) setDispatchPause(ctx context.Context, req dispatchPauseRequest) (DispatchPause, error) {
	done := o.loopExitedCh()
	if done == nil {
		return DispatchPause{}, errLoopNotRunning
	}
	req.reply = make(chan DispatchPause, 1)
	select {
	case o.events <- OrchestratorEvent{Type: EventSetDispatchPause, dispatchPause: &req}:
	case <-done:
		return DispatchPause{}, errLoopNotRunning
	case <-ctx.Done():
		return DispatchPause{}, ctx.Err()
	}
	select {
	case p := <-req.reply:
		return p, nil
	case <-done:
		select {
		case p := <-req.reply:
			return p, nil
		default:
			return DispatchPause{}, errLoopNotRunning
		}
	case <-ctx.Done():
		return DispatchPause{}, ctx.Err()
	}
}

// applySetDispatchPause handles EventSetDispatchPause. Event loop only.
func (o *Orchestrator) applySetDispatchPause(ctx context.Context, state *State, req *dispatchPauseRequest, now time.Time) {
	if req == nil {
		return
	}
	if req.pause {
		o.pauseDispatch(state, req.reason, req.until, now)
	} else {
		o.resumeDispatch(ctx, state, "operator")
	}
	if req.reply != nil {
		req.reply <- state.DispatchPaused
	}
}

// pauseDispatch sets the pause and records it. Event loop only.
func (o *Orchestrator) pauseDispatch(state *State, reason string, until time.Time, now time.Time) {
	next := DispatchPause{Active: true, Reason: sanitizeFailureMessage(reason), PausedAt: now.UTC()}
	if !until.IsZero() {
		next.Until = until.UTC()
	}
	if cur := state.DispatchPaused; cur.Active {
		next.PausedAt = cur.PausedAt
		if next.Reason == cur.Reason && next.Until.Equal(cur.Until) {
			return
		}
	}
	state.DispatchPaused = next
	o.saveDispatchPauseToDisk(state)
	o.emitFeed(state, FeedDispatchPaused, "", FeedPayload{Reason: next.Reason, Until: next.Until})
	slog.Warn("orchestrator: all dispatch paused — running agents continue, nothing new starts",
		"reason", next.Reason, "until", next.Until, "running", len(state.Running))
}

// resumeDispatch clears the pause, records why ("operator" or "expired")
// and lets held automations start at once; the next tick dispatches the
// rest. Event loop only.
func (o *Orchestrator) resumeDispatch(ctx context.Context, state *State, via string) {
	if !state.DispatchPaused.Active {
		return
	}
	state.DispatchPaused = DispatchPause{}
	o.saveDispatchPauseToDisk(state)
	o.emitFeed(state, FeedDispatchResumed, "", FeedPayload{Via: via})
	slog.Info("orchestrator: dispatch resumed", "via", via)
	if ctx != nil {
		o.drainAutomationQueue(ctx, state, time.Now())
	}
	o.Refresh()
}

// expireDispatchPause ends a pause whose Until has passed. Called at the
// top of every tick and event. Event loop only. Reports whether it ended one.
func (o *Orchestrator) expireDispatchPause(ctx context.Context, state *State, now time.Time) bool {
	p := state.DispatchPaused
	if !p.Active || p.Until.IsZero() || now.Before(p.Until) {
		return false
	}
	o.resumeDispatch(ctx, state, "expired")
	return true
}

// saveDispatchPauseToDisk persists the pause. Event loop only.
func (o *Orchestrator) saveDispatchPauseToDisk(state *State) {
	if o.dispatchPauseFile == "" {
		return
	}
	data, err := json.Marshal(dispatchPauseEnvelope{Version: dispatchPauseFileVersion, DispatchPause: state.DispatchPaused})
	if err != nil {
		slog.Warn("orchestrator: failed to marshal the dispatch pause", "error", err)
		return
	}
	o.persistLedger(ledgerDispatchPause, o.dispatchPauseFile, data, 0o644)
}

// loadDispatchPauseFromDisk restores the pause at startup. An expired one
// is restored as is and ends (with its dispatch_resumed feed event) on the
// first tick. A missing file is normal; a malformed one is quarantined and
// the daemon starts unpaused.
func (o *Orchestrator) loadDispatchPauseFromDisk(state State) State {
	if o.dispatchPauseFile == "" {
		return state
	}
	data, err := os.ReadFile(o.dispatchPauseFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load the dispatch pause", "path", o.dispatchPauseFile, "error", err)
		}
		return state
	}
	var env dispatchPauseEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		o.quarantineLedgerFile(o.dispatchPauseFile, data, err)
		return state
	}
	if env.Version != dispatchPauseFileVersion {
		o.quarantineLedgerFile(o.dispatchPauseFile, data,
			fmt.Errorf("orchestrator: unsupported dispatch_paused.json version %d (want %d)", env.Version, dispatchPauseFileVersion))
		return state
	}
	if env.Active {
		state.DispatchPaused = env.DispatchPause
		slog.Warn("orchestrator: all dispatch is paused (restored)", "reason", env.Reason, "until", env.Until)
	}
	return state
}
