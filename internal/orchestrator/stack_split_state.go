package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/metrics"
)

// Stack split decisions in the event loop (State.StackSplits). The worker
// (stack_split.go) sends one EventStackSplit when it decides, and again as
// each pull request opens; the loop keeps the latest per issue, emits one
// stack_split feed event per decision, persists the records to
// stack_splits.json (ledger writer) and prunes them when the issue turns
// terminal. merge_pr reads them through MergeLaneRefusalForPR: the lower,
// code-only pull request of a code-first split is judged by its own lane,
// not by the issue's (which the pipeline push above it raised to human),
// while its head is still the commit the split pushed (LowerSHA).

// EventStackSplit records a stack split decision (guardrails.lanes.split).
const EventStackSplit EventType = "StackSplit"

// FeedStackSplit is the feed kind of a stack split decision: payload RunID,
// Outcome (the order), Reason, Count (pipeline files), PRURL (the lower PR).
const FeedStackSplit = "stack_split"

// StackSplitRecord is one run's stack split decision.
type StackSplitRecord struct {
	// RunID is the run that split (its log session id).
	RunID string `json:"runId"`
	// Order is StackSplitCodeFirst, StackSplitPipelineFirst or
	// StackSplitCombined (no split: one PR, Reason says why).
	Order  string `json:"order"`
	Reason string `json:"reason,omitempty"`
	// CodeFiles and PipelineFiles are the files on each side (bounded,
	// redacted), with their totals.
	CodeFiles          []string `json:"codeFiles,omitempty"`
	CodeFilesTotal     int      `json:"codeFilesTotal,omitempty"`
	PipelineFiles      []string `json:"pipelineFiles,omitempty"`
	PipelineFilesTotal int      `json:"pipelineFilesTotal,omitempty"`
	// CodeLane is the lane of the code side alone (auto, second_opinion,
	// or human when a human-lane lockfile stayed with the code).
	CodeLane string `json:"codeLane,omitempty"`
	// LowerBranch is the branch at the bottom of the stack (against the
	// base); UpperBranch the issue branch stacked on it.
	LowerBranch string `json:"lowerBranch,omitempty"`
	UpperBranch string `json:"upperBranch,omitempty"`
	// LowerSHA is the commit the daemon last pushed to LowerBranch ("" until
	// that push succeeded). merge_pr grants the lower pull request its own
	// lane only while its head is this commit, and a later run of the issue
	// updates the lower branch only from it (a fast-forward), never over a
	// branch it did not push.
	LowerSHA string `json:"lowerSha,omitempty"`
	// LowerPR / LowerPRURL and UpperPRURL are the pull requests, once open.
	LowerPR    int    `json:"lowerPr,omitempty"`
	LowerPRURL string `json:"lowerPrUrl,omitempty"`
	UpperPRURL string `json:"upperPrUrl,omitempty"`
	// DecidedAt is when the loop first recorded the decision.
	DecidedAt time.Time `json:"decidedAt"`
}

// sendStackSplit hands a decision to the event loop. Worker goroutine; a
// saturated loop is retried (retryBusy), and a decision that still cannot
// be sent leaves merge policy as strict as without the split.
func (o *Orchestrator) sendStackSplit(ctx context.Context, identifier string, rec StackSplitRecord) {
	rec.CodeFiles, rec.PipelineFiles = cloneSlice(rec.CodeFiles), cloneSlice(rec.PipelineFiles)
	err := o.retryBusy(ctx, func() error {
		select {
		case o.events <- OrchestratorEvent{Type: EventStackSplit, Identifier: identifier, stackSplit: &rec}:
			return nil
		default:
			metrics.EventDropped()
			return ErrBusy
		}
	})
	if err != nil {
		slog.Warn("worker: stack split decision not recorded", "issue_identifier", identifier, "error", err)
	}
}

// handleStackSplit records a decision. A record for the same run and order
// is an update (a pull request opened): no second feed event. Event loop.
func (o *Orchestrator) handleStackSplit(state *State, ev OrchestratorEvent, now time.Time) {
	rec := ev.stackSplit
	if rec == nil || ev.Identifier == "" {
		return
	}
	if state.StackSplits == nil {
		state.StackSplits = make(map[string]StackSplitRecord)
	}
	prev, had := state.StackSplits[ev.Identifier]
	update := had && prev.RunID == rec.RunID && prev.Order == rec.Order
	rec.Reason = sanitizeFailureMessage(rec.Reason)
	rec.DecidedAt = now
	if update {
		rec.DecidedAt = prev.DecidedAt
	}
	state.StackSplits[ev.Identifier] = *rec
	if !update {
		slog.Info("guardrails: stack split decided", "issue_identifier", ev.Identifier, "run_id", rec.RunID,
			"order", rec.Order, "code_files", rec.CodeFilesTotal, "pipeline_files", rec.PipelineFilesTotal)
		if o.logBuf != nil {
			o.logBuf.Add(ev.Identifier, makeBufLineWithSession("INFO", "guardrails: stack split "+rec.Order+": "+rec.Reason+" (pipeline: "+
				guardrails.LanePathSummary(rec.PipelineFiles, rec.PipelineFilesTotal, guardrails.MaxLanePathsShown)+")", rec.RunID))
		}
		o.emitFeed(state, FeedStackSplit, ev.Identifier, FeedPayload{RunID: rec.RunID, Outcome: rec.Order,
			Reason: rec.Reason, Count: rec.PipelineFilesTotal, PRURL: rec.LowerPRURL})
	}
	o.saveStackSplitsToDisk(cloneStackSplits(state.StackSplits))
}

// stackSplitLaneRecord is the lane record merge_pr may judge pull request
// pr of identifier by, when it is the lower, code-only pull request of a
// code-first split: the code side's own lane (nil when that lane is auto),
// granted only while the pull request's head is sha, the commit the split
// pushed to the lower branch. ok false: pr is not such a pull request (or
// the split recorded no pushed commit); the issue's record applies. Pure
// over State.
func stackSplitLaneRecord(state State, identifier string, pr int) (rec *PushLaneRecord, sha string, ok bool) {
	s, found := state.StackSplits[identifier]
	if !found || pr <= 0 || s.LowerPR != pr || s.Order != StackSplitCodeFirst || s.LowerSHA == "" {
		return nil, "", false
	}
	if !guardrails.IsLane(s.CodeLane) {
		return nil, s.LowerSHA, true
	}
	r := PushLaneRecord{Lane: s.CodeLane, Paths: s.CodeFiles, PathsTotal: s.CodeFilesTotal, RunID: s.RunID, UpdatedAt: s.DecidedAt}
	if issue, has := state.PushLanes[identifier]; has {
		r.WriterBackends = issue.WriterBackends
	}
	return &r, s.LowerSHA, true
}

// pruneStackSplits drops the records of issues keep rejects (terminal
// issues) and persists the result when anything went. Event loop.
func (o *Orchestrator) pruneStackSplits(state *State, keep func(ident string) bool) {
	if pruneMap(state.StackSplits, identOfKey, keep) > 0 {
		o.saveStackSplitsToDisk(cloneStackSplits(state.StackSplits))
	}
}

// cloneStackSplits deep-copies State.StackSplits.
func cloneStackSplits(m map[string]StackSplitRecord) map[string]StackSplitRecord {
	if m == nil {
		return nil
	}
	cp := make(map[string]StackSplitRecord, len(m))
	for k, v := range m {
		v.CodeFiles = cloneSlice(v.CodeFiles)
		v.PipelineFiles = cloneSlice(v.PipelineFiles)
		cp[k] = v
	}
	return cp
}

// SetStackSplitsFile sets the path State.StackSplits persists to
// (.itervox/stack_splits.json), so a restart keeps the per-PR merge policy
// of a split. Before Run.
func (o *Orchestrator) SetStackSplitsFile(path string) { o.stackSplitsFile = path }

// loadStackSplitsFromDisk pre-populates State.StackSplits at startup. A
// missing file is not an error; a corrupt one is logged and ignored (the
// issue's own lane then applies to both pull requests: stricter).
func (o *Orchestrator) loadStackSplitsFromDisk(state State) State {
	if o.stackSplitsFile == "" {
		return state
	}
	data, err := os.ReadFile(o.stackSplitsFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("orchestrator: failed to load stack splits", "path", o.stackSplitsFile, "error", err)
		}
		return state
	}
	var disk map[string]StackSplitRecord
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse stack splits", "path", o.stackSplitsFile, "error", err)
		return state
	}
	if state.StackSplits == nil {
		state.StackSplits = make(map[string]StackSplitRecord, len(disk))
	}
	for id, rec := range disk {
		if id != "" && rec.Order != "" {
			state.StackSplits[id] = rec
		}
	}
	return state
}

// saveStackSplitsToDisk hands a copy of the records to the ledger writer.
func (o *Orchestrator) saveStackSplitsToDisk(splits map[string]StackSplitRecord) {
	if o.stackSplitsFile == "" {
		return
	}
	data, err := json.Marshal(maps.Clone(splits))
	if err != nil {
		slog.Warn("orchestrator: failed to marshal stack splits", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(o.stackSplitsFile), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create stack splits dir", "path", o.stackSplitsFile, "error", err)
		return
	}
	o.persistLedger(ledgerStackSplits, o.stackSplitsFile, data, 0o600)
}
