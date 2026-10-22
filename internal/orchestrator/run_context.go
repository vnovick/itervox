package orchestrator

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// #215 — the run context: what a run is doing right now, for
// GET /api/v1/issues/{identifier}/context and the dashboard's "Now" panel.
//
// Invariants:
//   - State.RunContexts is event-loop state keyed by run ID. A worker never
//     touches it: its runContextReporter sends EventRunContext, at most once
//     per runContextMinInterval per run (plus one flush at the end of each
//     turn), non-blocking; a dropped update is counted and superseded by the
//     next one.
//   - Everything in it is small, redacted and bounded at the source: the
//     last assistant message is redacted, whitespace-collapsed and cut to
//     runContextStepMaxBytes; the last tool call is its name and target
//     (agent.ToolTarget, which drops every output field), redacted and cut.
//     Tool output is never read.
//   - It is NOT part of the SSE frame: the dashboard snapshot rows are built
//     from other fields, and an update publishes only the RunContexts map
//     into lastSnap (publishRunContexts) without the store-and-announce of a
//     full snapshot. The endpoint reads one entry by identifier
//     (Orchestrator.RunContext).
//   - Bounded: the contexts of running runs plus the runContextFinishedCap
//     most recent finished ones (trimRunContexts). Session-scoped, not
//     persisted.

// EventRunContext carries one runContextUpdate from a worker.
const EventRunContext EventType = "RunContext"

const (
	// runContextMinInterval is the shortest gap between two updates a run
	// sends to the loop (an end-of-turn flush is always sent).
	runContextMinInterval = time.Second
	// runContextStepMaxBytes bounds the current-step summary.
	runContextStepMaxBytes = 500
	// runContextTargetMaxBytes bounds the last tool call's target.
	runContextTargetMaxBytes = 300
	// runContextPendingMaxBytes bounds the pending-input question.
	runContextPendingMaxBytes = 500
	// runContextFinishedCap is how many finished runs keep their context.
	runContextFinishedCap = 32
	// runContextSteersShown is how many delivered steering messages the
	// view carries (the newest).
	runContextSteersShown = 5
)

// RunContext is one run's context. A value type of scalars only.
type RunContext struct {
	RunID      string
	Identifier string
	// BaseBranch is the branch the run's pull request targets: the blocker
	// branch of a stacked worktree, else workspace.base_branch. The diff
	// endpoint uses it as the default merge-base ref.
	BaseBranch string
	// CurrentStep is the last assistant message, redacted and bounded.
	CurrentStep string
	// LastToolName / LastToolTarget are the most recent tool call (redacted,
	// bounded target; never its output) and LastToolAt when it was seen.
	LastToolName   string
	LastToolTarget string
	LastToolAt     time.Time
	// ToolCalls counts the run's completed tool calls.
	ToolCalls int
	// GuardrailBlocks counts the operations a guardrail refused in this run;
	// LastGuardrailKind is the newest one's kind.
	GuardrailBlocks   int
	LastGuardrailKind string
	UpdatedAt         time.Time
	// Set when the run exits, from its run entry.
	ProfileName  string
	Backend      string
	Kind         string
	StartedAt    time.Time
	FinishedAt   time.Time
	Outcome      string
	TurnCount    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// runContextUpdate is the payload of EventRunContext.
type runContextUpdate struct {
	RunID          string
	Identifier     string
	BaseBranch     string
	CurrentStep    string
	LastToolName   string
	LastToolTarget string
	ToolCalls      int
	At             time.Time
}

// sanitizeRunText redacts s, collapses its whitespace and bounds it.
// Redaction runs first, so a secret cut by the bound is still caught.
func sanitizeRunText(s string, maxBytes int) string {
	return boundUTF8(strings.Join(strings.Fields(logging.RedactString(s)), " "), maxBytes)
}

// runContextReporter turns a worker's progress callbacks into debounced
// EventRunContext sends. Owned by one worker goroutine; never touches State.
type runContextReporter struct {
	o          *Orchestrator
	runID      string
	identifier string
	baseBranch string
	interval   time.Duration
	now        func() time.Time
	// lastSent is when the last update was handed to the loop; sent is
	// that update (At zeroed) so an unchanged one is not resent.
	lastSent time.Time
	sent     runContextUpdate
	// cur carries the newest step and tool forward across turns: a new
	// turn's first progress has no text or tool yet.
	cur runContextUpdate
	// doneToolCalls counts the completed tool calls of finished turns.
	doneToolCalls int
}

// newRunContextReporter returns the reporter of the run ctx belongs to, or
// nil for a worker started without a run (tests calling runWorker directly).
func (o *Orchestrator) newRunContextReporter(ctx context.Context, identifier, baseBranch string) *runContextReporter {
	runID := runIDFrom(ctx)
	if runID == "" {
		return nil
	}
	return &runContextReporter{
		o: o, runID: runID, identifier: identifier, baseBranch: baseBranch,
		interval: runContextMinInterval, now: time.Now,
	}
}

// observe folds a partial turn result in and sends an update when it
// changed and the last send is at least interval old (always when force).
func (r *runContextReporter) observe(partial agent.TurnResult, force bool) {
	if r == nil {
		return
	}
	if step := sanitizeRunText(partial.LastText, runContextStepMaxBytes); step != "" {
		r.cur.CurrentStep = step
	}
	if partial.LastToolName != "" {
		r.cur.LastToolName = sanitizeFailureField(logging.RedactString(partial.LastToolName))
		r.cur.LastToolTarget = sanitizeRunText(partial.LastToolTarget, runContextTargetMaxBytes)
	}
	r.cur.ToolCalls = r.doneToolCalls + partial.ToolCalls
	r.cur.RunID, r.cur.Identifier, r.cur.BaseBranch = r.runID, r.identifier, r.baseBranch
	if r.cur == r.sent && !r.lastSent.IsZero() {
		return
	}
	now := r.now()
	if !force && !r.lastSent.IsZero() && now.Sub(r.lastSent) < r.interval {
		return
	}
	u := r.cur
	u.At = now
	select {
	case r.o.events <- OrchestratorEvent{Type: EventRunContext, RunID: r.runID, Identifier: r.identifier, runContext: &u}:
		r.lastSent, r.sent = now, r.cur
	default:
		metrics.EventDropped() // CORE-045; the next update supersedes it
		slog.Debug("orchestrator: run context update dropped (channel full)", "issue_identifier", r.identifier)
	}
}

// endTurn flushes the turn's final result and banks its tool calls.
func (r *runContextReporter) endTurn(result agent.TurnResult) {
	if r == nil {
		return
	}
	r.observe(result, true)
	r.doneToolCalls += result.ToolCalls
}

// runningByRunID returns the Running entry of runID, or nil.
func runningByRunID(state *State, runID string) *RunEntry {
	if runID == "" {
		return nil
	}
	for _, e := range state.Running {
		if e != nil && e.RunID == runID {
			return e
		}
	}
	return nil
}

// runningByIdentifier returns the Running entry of identifier, or nil.
func runningByIdentifier(running map[string]*RunEntry, identifier string) *RunEntry {
	for _, e := range running {
		if e != nil && e.Issue.Identifier == identifier {
			return e
		}
	}
	return nil
}

// applyRunContext handles EventRunContext. Event loop only. An update for a
// run that is no longer running is ignored. The update is published into
// lastSnap directly: it is not part of the SSE frame, so it neither stores
// a whole snapshot nor wakes the dashboards.
func (o *Orchestrator) applyRunContext(state *State, u *runContextUpdate) {
	o.eventLeftStateUnchanged = true
	if u == nil || runningByRunID(state, u.RunID) == nil {
		return
	}
	if state.RunContexts == nil {
		state.RunContexts = map[string]RunContext{}
	}
	rc := state.RunContexts[u.RunID]
	rc.RunID, rc.Identifier, rc.BaseBranch = u.RunID, u.Identifier, u.BaseBranch
	rc.CurrentStep = u.CurrentStep
	if u.LastToolName != "" && (u.LastToolName != rc.LastToolName || u.LastToolTarget != rc.LastToolTarget || u.ToolCalls != rc.ToolCalls) {
		rc.LastToolAt = u.At
	}
	rc.LastToolName, rc.LastToolTarget = u.LastToolName, u.LastToolTarget
	rc.ToolCalls = u.ToolCalls
	rc.UpdatedAt = u.At
	state.RunContexts[u.RunID] = rc
	o.publishRunContexts(state)
}

// publishRunContexts copies State.RunContexts into the published snapshot.
// Event loop only.
func (o *Orchestrator) publishRunContexts(state *State) {
	cp := maps.Clone(state.RunContexts)
	o.snapMu.Lock()
	o.lastSnap.RunContexts = cp
	o.snapMu.Unlock()
}

// noteRunContextGuardrail counts a guardrail refusal on the identifier's
// running run. Event loop only (EventFailureRecorded).
func noteRunContextGuardrail(state *State, identifier, kind string, now time.Time) {
	e := runningByIdentifier(state.Running, identifier)
	if e == nil || e.RunID == "" {
		return
	}
	if state.RunContexts == nil {
		state.RunContexts = map[string]RunContext{}
	}
	rc := state.RunContexts[e.RunID]
	rc.RunID, rc.Identifier = e.RunID, identifier
	rc.GuardrailBlocks++
	rc.LastGuardrailKind = sanitizeFailureField(kind)
	rc.UpdatedAt = now
	state.RunContexts[e.RunID] = rc
}

// finishRunContext records a run's exit on its context (creating one for a
// run that never reported) and trims the finished ones. Event loop only.
func finishRunContext(state *State, live, exit *RunEntry, runID string, now time.Time) {
	src := cmp.Or(live, exit)
	if src == nil {
		return
	}
	runID = cmp.Or(runID, src.RunID)
	if runID == "" {
		return
	}
	if state.RunContexts == nil {
		state.RunContexts = map[string]RunContext{}
	}
	rc := state.RunContexts[runID]
	rc.RunID, rc.Identifier = runID, src.Issue.Identifier
	rc.ProfileName, rc.Backend, rc.Kind = src.ProfileName, src.Backend, src.Kind
	rc.StartedAt, rc.TurnCount = src.StartedAt, src.TurnCount
	rc.InputTokens, rc.OutputTokens, rc.TotalTokens = src.InputTokens, src.OutputTokens, src.TotalTokens
	if exit != nil {
		rc.Outcome = string(exit.TerminalReason)
	}
	rc.FinishedAt, rc.UpdatedAt = now, now
	state.RunContexts[runID] = rc
	trimRunContexts(state)
}

// trimRunContexts keeps every running run's context and the
// runContextFinishedCap most recently finished others. Event loop only.
func trimRunContexts(state *State) {
	running := make(map[string]bool, len(state.Running))
	for _, e := range state.Running {
		if e != nil {
			running[e.RunID] = true
		}
	}
	type done struct {
		id string
		at time.Time
	}
	var finished []done
	for id, rc := range state.RunContexts {
		if !running[id] {
			finished = append(finished, done{id, cmp.Or(rc.FinishedAt, rc.UpdatedAt)})
		}
	}
	for len(finished) > runContextFinishedCap {
		oldest := 0
		for i, f := range finished {
			if f.at.Before(finished[oldest].at) {
				oldest = i
			}
		}
		delete(state.RunContexts, finished[oldest].id)
		finished = append(finished[:oldest], finished[oldest+1:]...)
	}
}

// RunContextView is the run context of an issue's current run (Live), or
// of its most recent finished run still kept, joined with what the event
// loop knows about it: the live entry's counters, a pending input question
// and the steering messages.
type RunContextView struct {
	RunContext
	Live bool
	// PendingInput is the question the issue waits on (redacted, bounded);
	// PendingApproval marks a guardrail approval request.
	PendingInput    string
	PendingApproval bool
	// QueuedSteers counts operator messages not yet delivered; Steers are
	// the newest delivered ones (already redacted and bounded, #186).
	QueuedSteers int
	Steers       []RunSteer
	// Playbook is the live run's task-type playbook (#218); empty when it
	// has none or the run has finished.
	Playbook string
}

// RunContext returns identifier's run context from the published snapshot.
// Safe from any goroutine; it copies only what it returns. ok is false when
// the issue has neither a running run nor a kept finished context.
func (o *Orchestrator) RunContext(identifier string) (RunContextView, bool) {
	o.snapMu.RLock()
	defer o.snapMu.RUnlock()
	snap := &o.lastSnap
	var v RunContextView
	if e := runningByIdentifier(snap.Running, identifier); e != nil {
		v.Live = true
		v.RunContext = snap.RunContexts[e.RunID]
		v.RunID, v.Identifier = e.RunID, identifier
		v.ProfileName, v.Backend, v.Kind = e.ProfileName, e.Backend, cmp.Or(e.Kind, "worker")
		v.StartedAt, v.TurnCount = e.StartedAt, e.TurnCount
		v.Playbook = e.Playbook // #218
		v.InputTokens, v.OutputTokens, v.TotalTokens = e.InputTokens, e.OutputTokens, e.TotalTokens
		if n := len(e.Steers); n > 0 {
			v.Steers = append([]RunSteer(nil), e.Steers[max(0, n-runContextSteersShown):]...)
		}
	} else {
		found := false
		for _, rc := range snap.RunContexts {
			if rc.Identifier == identifier && (!found || rc.FinishedAt.After(v.FinishedAt)) {
				v.RunContext, found = rc, true
			}
		}
		if !found {
			return RunContextView{}, false
		}
		v.Kind = cmp.Or(v.Kind, "worker")
	}
	if in := snap.InputRequiredIssues[identifier]; in != nil {
		v.PendingInput = sanitizeRunText(in.Context, runContextPendingMaxBytes)
		v.PendingApproval = in.GuardrailApprovalID != ""
	}
	for _, m := range snap.SessionMessages[identifier] {
		if m.Status == SessionMessageQueued {
			v.QueuedSteers++
		}
	}
	return v, true
}
