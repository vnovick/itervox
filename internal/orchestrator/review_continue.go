package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// Review and continue: an operator's review of an issue's diff (line
// comments plus an overall note), sent while no run is live, becomes the
// input of the issue's next worker run.
//
//   - QueueReviewContinuation records it in State.ReviewContinuations
//     through the event loop; the set is persisted to
//     review_continuations.json by the ledger writer, so it survives a
//     daemon restart until a run has it.
//   - startRun hands it to the next worker run of the issue (not a reviewer
//     or automation run). The worker writes it into the issue's handoff dir
//     as <ISO8601>_operator-review.md once its workspace exists, so the
//     "Prior Agent Handoffs" block carries it, and reports the delivery; only
//     then is the record dropped. A run that exits before writing it gives
//     it back to the next run.
//   - With MoveToActive the caller moves the issue to the first active
//     state (the board's Dispatch) and the normal tick dispatches it, under
//     every gate a tick applies. Without it the issue keeps its tracker
//     state: resumeReviewContinuations dispatches it on a tick whose gates
//     admit it (slots, dispatch pause, paused-by-state, per-state limits,
//     blockers and inferred blockers, branch in use, then the backend
//     breaker and the budget inside dispatchRun), and leaves it queued
//     otherwise. Such a run does not move the issue to the working state,
//     reconciliation does not stop it for being outside the active states,
//     and it skips the completion move when the issue already sits in
//     tracker.completion_state.

// OperatorReviewRole is the role part of an operator-review handoff file
// name: <ISO8601>_operator-review.md.
const OperatorReviewRole = "operator-review"

// Errors of QueueReviewContinuation.
var (
	ErrReviewContinueRunLive       = errors.New("orchestrator: the issue has a live run")
	ErrReviewContinueInputRequired = errors.New("orchestrator: the issue is waiting for input")
	ErrReviewContinueEmpty         = errors.New("orchestrator: the review has no comments and no note")
	ErrReviewContinueNotRunning    = errors.New("orchestrator: the orchestrator is not running")
)

// ReviewContinueComment is one line or range comment of an operator review.
// Line is the last line, StartLine the first (0 for one line); Side and
// StartSide are "RIGHT" (the new file) or "LEFT" (the base file).
type ReviewContinueComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Side      string `json:"side,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
	// Quote is the selected diff lines.
	Quote string `json:"quote,omitempty"`
}

// ReviewContinuation is an operator review waiting for the issue's next
// worker run. Event-loop state.
type ReviewContinuation struct {
	IssueID    string                  `json:"issue_id"`
	Identifier string                  `json:"identifier"`
	Note       string                  `json:"note,omitempty"`
	Comments   []ReviewContinueComment `json:"comments,omitempty"`
	// MoveToActive records that the issue was moved to the first active
	// state for this review; false means the run starts without a tracker
	// write.
	MoveToActive bool      `json:"move_to_active"`
	QueuedAt     time.Time `json:"queued_at"`

	// takenBy is the run delivering it; not persisted (after a restart no
	// run is delivering anything).
	takenBy string
}

func (rc ReviewContinuation) clone() ReviewContinuation {
	rc.Comments = slices.Clone(rc.Comments)
	return rc
}

// copyReviewContinuations deep-copies the set for a snapshot.
func copyReviewContinuations(m map[string]ReviewContinuation) map[string]ReviewContinuation {
	if m == nil {
		return nil
	}
	out := make(map[string]ReviewContinuation, len(m))
	for k, v := range m {
		out[k] = v.clone()
	}
	return out
}

// reviewContinueRequest is the payload of EventReviewContinue.
type reviewContinueRequest struct {
	rc    ReviewContinuation
	drop  bool // drop the record queued at rc.QueuedAt instead
	reply chan error
}

// reviewDelivery is the payload of EventReviewContinueDelivered.
type reviewDelivery struct {
	identifier string
	queuedAt   time.Time
	runID      string
}

// SetReviewContinuationsFile sets the path queued operator reviews persist
// to. Must be called before Run.
func (o *Orchestrator) SetReviewContinuationsFile(path string) {
	o.reviewContinuationsFile = path
}

// QueueReviewContinuation records an operator review for the issue's next
// worker run. It returns ErrReviewContinueRunLive when a run is live (the
// caller steers it instead) and ErrReviewContinueInputRequired when the
// issue waits for an answer. A review queued while another is waiting is
// merged into it. Safe from any goroutine; blocks until the loop decided.
func (o *Orchestrator) QueueReviewContinuation(ctx context.Context, rc ReviewContinuation) (time.Time, error) {
	if rc.Identifier == "" || rc.IssueID == "" {
		return time.Time{}, errors.New("orchestrator: a review needs the issue")
	}
	if strings.TrimSpace(rc.Note) == "" && len(rc.Comments) == 0 {
		return time.Time{}, ErrReviewContinueEmpty
	}
	if rc.QueuedAt.IsZero() {
		rc.QueuedAt = time.Now().UTC()
	}
	err := o.sendReviewContinue(ctx, reviewContinueRequest{rc: rc.clone()})
	return rc.QueuedAt, err
}

// DropReviewContinuation removes the review queued at queuedAt (a caller
// whose tracker move failed takes its review back). Safe from any goroutine.
func (o *Orchestrator) DropReviewContinuation(ctx context.Context, identifier string, queuedAt time.Time) error {
	return o.sendReviewContinue(ctx, reviewContinueRequest{
		rc: ReviewContinuation{Identifier: identifier, QueuedAt: queuedAt}, drop: true})
}

func (o *Orchestrator) sendReviewContinue(ctx context.Context, req reviewContinueRequest) error {
	done := o.loopExitedCh()
	if done == nil {
		return ErrReviewContinueNotRunning
	}
	req.reply = make(chan error, 1)
	select {
	case o.events <- OrchestratorEvent{Type: EventReviewContinue, Identifier: req.rc.Identifier, reviewContinue: &req}:
	case <-done:
		return ErrReviewContinueNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-done:
		return ErrReviewContinueNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleReviewContinue applies an EventReviewContinue. Event loop only.
func (o *Orchestrator) handleReviewContinue(state *State, req *reviewContinueRequest) error {
	rc := req.rc
	if req.drop {
		if cur, ok := state.ReviewContinuations[rc.Identifier]; ok && cur.QueuedAt.Equal(rc.QueuedAt) && cur.takenBy == "" {
			delete(state.ReviewContinuations, rc.Identifier)
			o.saveReviewContinuations(state)
		}
		return nil
	}
	if issueHasLiveRun(*state, rc.Identifier) {
		return ErrReviewContinueRunLive
	}
	if _, waiting := state.InputRequiredIssues[rc.Identifier]; waiting {
		return ErrReviewContinueInputRequired
	}
	if state.ReviewContinuations == nil {
		state.ReviewContinuations = make(map[string]ReviewContinuation)
	}
	if cur, ok := state.ReviewContinuations[rc.Identifier]; ok {
		// A second review before a run took the first: one handoff.
		rc.Comments = append(slices.Clone(cur.Comments), rc.Comments...)
		rc.Note = strings.TrimSpace(strings.Join([]string{cur.Note, rc.Note}, "\n\n"))
		rc.QueuedAt = cur.QueuedAt
	}
	state.ReviewContinuations[rc.Identifier] = rc
	o.saveReviewContinuations(state)
	slog.Info("orchestrator: operator review queued for the next run",
		"identifier", rc.Identifier, "comments", len(rc.Comments), "move_to_active", rc.MoveToActive)
	if o.logBuf != nil {
		o.logBuf.Add(rc.Identifier, makeBufLine("INFO",
			fmt.Sprintf("worker: operator review queued for the next run (%s)", reviewCommentCount(len(rc.Comments)))))
	}
	return nil
}

// issueHasLiveRun reports whether identifier has a run in State.Running.
func issueHasLiveRun(state State, identifier string) bool {
	for _, e := range state.Running {
		if e != nil && e.Issue.Identifier == identifier {
			return true
		}
	}
	return false
}

func reviewCommentCount(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return fmt.Sprintf("%d comments", n)
}

// attachReviewContinuation gives a waiting review to the run startRun is
// starting when it is a worker run of the issue. Event loop only.
func attachReviewContinuation(state *State, entry *RunEntry, h *runHandle, reviewerProfile bool) {
	if state == nil || entry == nil || h == nil {
		return
	}
	if entry.Kind == "reviewer" || entry.Kind == "automation" || entry.AutomationID != "" || reviewerProfile {
		return
	}
	rc, ok := state.ReviewContinuations[entry.Issue.Identifier]
	if !ok || rc.takenBy != "" {
		return
	}
	rc.takenBy = h.id
	state.ReviewContinuations[entry.Issue.Identifier] = rc
	given := rc.clone()
	h.review = &given
	h.noTrackerMove = !rc.MoveToActive
	entry.ContinuedFromReview = true
	entry.ReviewComments = len(rc.Comments)
	entry.NoTrackerMove = !rc.MoveToActive
}

// runReviewContinuation returns the review a worker's run was given.
func runReviewContinuation(ctx context.Context) *ReviewContinuation {
	if h := runFrom(ctx); h != nil {
		return h.review
	}
	return nil
}

// runNoTrackerMove reports whether a worker's run was started without a
// tracker move (a review continued in place).
func runNoTrackerMove(ctx context.Context) bool {
	h := runFrom(ctx)
	return h != nil && h.noTrackerMove
}

// handleReviewDelivered drops a review its run has written into the
// workspace. Event loop only.
func (o *Orchestrator) handleReviewDelivered(state *State, d *reviewDelivery) {
	rc, ok := state.ReviewContinuations[d.identifier]
	if !ok || !rc.QueuedAt.Equal(d.queuedAt) {
		return
	}
	delete(state.ReviewContinuations, d.identifier)
	o.saveReviewContinuations(state)
	slog.Info("orchestrator: operator review handed to its run", "identifier", d.identifier, "run_id", d.runID)
}

// releaseReviewContinuation gives a review back when the run that took it
// exited without delivering it, so the next run gets it. Event loop only.
func releaseReviewContinuation(state *State, identifier, runID string) {
	if runID == "" {
		return
	}
	if rc, ok := state.ReviewContinuations[identifier]; ok && rc.takenBy == runID {
		rc.takenBy = ""
		state.ReviewContinuations[identifier] = rc
	}
}

// deliverReviewContinuation writes the run's review into its workspace's
// handoff dir and reports the delivery to the loop. Worker goroutine.
func (o *Orchestrator) deliverReviewContinuation(ctx context.Context, rc *ReviewContinuation, wsPath string, runStart time.Time, runLogID string) {
	if rc == nil {
		return
	}
	if wsPath == "" {
		slog.Warn("worker: no workspace to hand the operator review to", "identifier", rc.Identifier)
		return
	}
	// One millisecond before the run's own handoff timestamp, so the review
	// sorts before the deliverable this run writes.
	rel := handoffPathFor(handoffRunTimestamp(runStart.Add(-time.Millisecond)), OperatorReviewRole)
	abs := filepath.Join(wsPath, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		slog.Warn("worker: cannot create the handoff dir for the operator review", "identifier", rc.Identifier, "error", err)
		return
	}
	if err := atomicfs.WriteFile(abs, []byte(RenderOperatorReview(*rc)), 0o644); err != nil {
		slog.Warn("worker: cannot write the operator review handoff", "identifier", rc.Identifier, "error", err)
		return
	}
	if o.logBuf != nil {
		o.logBuf.Add(rc.Identifier, makeBufLineWithSession("INFO",
			fmt.Sprintf("worker: continued from the operator's review (%s): %s", reviewCommentCount(len(rc.Comments)), rel), runLogID))
	}
	ev := OrchestratorEvent{Type: EventReviewContinueDelivered, Identifier: rc.Identifier,
		reviewDelivered: &reviewDelivery{identifier: rc.Identifier, queuedAt: rc.QueuedAt, runID: runIDFrom(ctx)}}
	select {
	case o.events <- ev:
	case <-ctx.Done():
	}
}

// RenderOperatorReview is the handoff file an operator review becomes.
func RenderOperatorReview(rc ReviewContinuation) string {
	var b strings.Builder
	b.WriteString("# Operator review\n\n")
	fmt.Fprintf(&b, "The operator reviewed this issue's changes (%s) and asks this run to continue from it: address every comment below on this branch, then write your own handoff as usual.\n",
		reviewCommentCount(len(rc.Comments)))
	if note := strings.TrimSpace(rc.Note); note != "" {
		b.WriteString("\n## Overall\n\n")
		b.WriteString(note)
		b.WriteString("\n")
	}
	for i, c := range rc.Comments {
		fmt.Fprintf(&b, "\n## %d. %s, %s\n\n", i+1, c.Path, ReviewLineSpan(c.Line, c.StartLine, c.Side, c.StartSide))
		if q := strings.TrimRight(c.Quote, "\n"); strings.TrimSpace(q) != "" {
			fence := "```"
			for strings.Contains(q, fence) {
				fence += "`"
			}
			b.WriteString(fence + "diff\n" + q + "\n" + fence + "\n\n")
		}
		b.WriteString(strings.TrimSpace(c.Body))
		b.WriteString("\n")
	}
	return b.String()
}

// ReviewLineSpan describes a comment's lines: "line 12", "lines 12-14",
// with "(base)" for the LEFT side.
func ReviewLineSpan(line, startLine int, side, startSide string) string {
	mark := func(s string) string {
		if strings.EqualFold(s, "LEFT") {
			return " (base)"
		}
		return ""
	}
	if startSide == "" {
		startSide = side
	}
	switch {
	case startLine == 0 || (startLine == line && strings.EqualFold(startSide, side)):
		return fmt.Sprintf("line %d%s", line, mark(side))
	case strings.EqualFold(startSide, side):
		return fmt.Sprintf("lines %d-%d%s", startLine, line, mark(side))
	default:
		return fmt.Sprintf("lines %d%s to %d%s", startLine, mark(startSide), line, mark(side))
	}
}

// ineligibleReasonForContinuation is the admission check for a review
// continued without a tracker move: every guard of normal dispatch except
// the active-state requirement. The issue may sit in completion_state even
// when that state is terminal (where the review normally finds it); any
// other terminal state refuses it.
func ineligibleReasonForContinuation(issue domain.Issue, state State, cfg *config.Config) string {
	if issue.ID == "" || issue.Identifier == "" || issue.State == "" {
		return "missing_fields"
	}
	if isTerminalState(issue.State, state) && !isReviewState(issue.State, state) {
		return "terminal_state"
	}
	if _, pending := state.DeferredStateMoves[issue.Identifier]; pending {
		return IneligibleStateMovePending
	}
	probe := state
	probe.TerminalStates = slices.DeleteFunc(slices.Clone(state.TerminalStates), func(s string) bool {
		return isReviewState(s, state)
	})
	return ineligibleReasonShared(issue, probe, cfg, false)
}

// resumeReviewContinuations dispatches the issues whose review continues
// without a tracker move, oldest first, once their gates admit them; the
// rest stay queued for a later tick. A review queued with a tracker move
// needs nothing here: the moved issue is dispatched by the normal tick.
// Event loop only.
func (o *Orchestrator) resumeReviewContinuations(ctx context.Context, state *State) {
	if len(state.ReviewContinuations) == 0 {
		return
	}
	ids := slices.Collect(maps.Keys(state.ReviewContinuations))
	slices.SortFunc(ids, func(a, b string) int {
		return state.ReviewContinuations[a].QueuedAt.Compare(state.ReviewContinuations[b].QueuedAt)
	})
	for _, id := range ids {
		rc := state.ReviewContinuations[id]
		if rc.MoveToActive || rc.takenBy != "" || issueHasLiveRun(*state, id) {
			continue
		}
		if AvailableSlots(*state) <= 0 {
			return
		}
		if _, claimed := state.Claimed[rc.IssueID]; claimed {
			continue
		}
		rctx, cancel := loopReadCtx(ctx)
		issue, err := o.tracker.FetchIssueDetail(rctx, rc.IssueID)
		cancel()
		if errors.Is(err, tracker.ErrNotFound) || (err == nil && issue == nil) {
			slog.Warn("orchestrator: operator review dropped, the issue no longer exists", "identifier", id)
			delete(state.ReviewContinuations, id)
			o.saveReviewContinuations(state)
			continue
		}
		if err != nil {
			slog.Warn("orchestrator: operator review: issue fetch failed, retrying next tick", "identifier", id, "error", err)
			continue
		}
		reason := ineligibleReasonForContinuation(*issue, *state, o.cfg)
		if reason == "terminal_state" {
			slog.Info("orchestrator: operator review dropped, the issue was closed", "identifier", id, "state", issue.State)
			if o.logBuf != nil {
				o.logBuf.Add(id, makeBufLine("WARN", fmt.Sprintf("worker: operator review dropped: the issue moved to %q", issue.State)))
			}
			delete(state.ReviewContinuations, id)
			o.saveReviewContinuations(state)
			continue
		}
		if reason != "" {
			slog.Debug("orchestrator: operator review waits for admission", "identifier", id, "reason", reason)
			continue
		}
		slog.Info("orchestrator: dispatching an issue to continue from the operator's review",
			"identifier", id, "state", issue.State, "queued_at", rc.QueuedAt)
		*state = o.dispatch(ctx, *state, *issue, 0)
	}
}

// loadReviewContinuationsFromDisk restores queued reviews at startup. A
// missing file is normal; a corrupt one is logged and ignored.
func (o *Orchestrator) loadReviewContinuationsFromDisk(state State) State {
	path := o.reviewContinuationsFile
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to read queued operator reviews", "path", path, "error", err)
		}
		return state
	}
	var disk map[string]ReviewContinuation
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse queued operator reviews", "path", path, "error", err)
		return state
	}
	if state.ReviewContinuations == nil {
		state.ReviewContinuations = make(map[string]ReviewContinuation, len(disk))
	}
	for id, rc := range disk {
		if id == "" || rc.IssueID == "" {
			continue
		}
		rc.Identifier = id
		state.ReviewContinuations[id] = rc
	}
	if len(state.ReviewContinuations) > 0 {
		slog.Info("orchestrator: loaded queued operator reviews", "path", path, "count", len(state.ReviewContinuations))
	}
	return state
}

// saveReviewContinuations persists the queued reviews. Event loop.
func (o *Orchestrator) saveReviewContinuations(state *State) {
	path := o.reviewContinuationsFile
	if path == "" {
		return
	}
	data, err := json.Marshal(copyReviewContinuations(state.ReviewContinuations))
	if err != nil {
		slog.Warn("orchestrator: failed to marshal queued operator reviews", "error", err)
		return
	}
	o.persistLedger(ledgerReviewContinuations, path, data, 0o600)
}

// isReviewerProfileName reports whether name is a configured reviewer
// profile (cfgMu: the reviewer settings are runtime-mutable).
func (o *Orchestrator) isReviewerProfileName(name string) bool {
	if o == nil || o.cfg == nil || name == "" {
		return false
	}
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	return isReviewerProfile(o.cfg, name)
}

// cmpRunID is the exiting run's ID: the event's, else its live entry's.
func cmpRunID(evRunID string, live *RunEntry) string {
	if evRunID != "" || live == nil {
		return evRunID
	}
	return live.RunID
}
