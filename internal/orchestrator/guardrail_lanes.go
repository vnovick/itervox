package orchestrator

import (
	"cmp"
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

	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// guardrails.lanes. A run's pre-push hook classifies what each push
// changes (internal/guardrails): a push changing human-lane files waits for
// a human approval through the #204 approval flow (an input-required entry
// with GuardrailLanePaths); a push the hook lets through that reached the
// second_opinion or human lane is reported (GuardrailLane →
// EventGuardrailLane), and the event loop keeps the highest lane per issue
// in State.PushLanes. merge_pr asks the loop (MergeLaneRefusal →
// EventMergeLaneCheck) before it merges: a human-lane issue is never
// auto-merged; a second-opinion issue needs an approving review from a
// reviewer whose backend differs from every backend that pushed in the
// lane, recorded after the last such push.
//
// PushLanes is keyed by issue identifier, not by run: the pull request
// merge_pr merges carries every run's pushes, and the run that calls
// merge_pr (often a later run or a reviewer) is rarely the run that pushed.

// EventGuardrailLane records a pushed change's lane (guardrails.lanes).
const EventGuardrailLane EventType = "GuardrailLane"

// EventMergeLaneCheck asks the loop whether merge_pr may merge an issue's
// pull request under guardrails.lanes.
const EventMergeLaneCheck EventType = "MergeLaneCheck"

// maxPushLanePaths bounds the paths a PushLaneRecord keeps.
const maxPushLanePaths = 20

// PushLaneRecord is State.PushLanes' value: the highest lane an issue's
// agent pushes reached.
type PushLaneRecord struct {
	// Lane is guardrails.LaneSecondOpinion or guardrails.LaneHuman.
	Lane string `json:"lane"`
	// Paths are the pushed files in Lane (bounded, redacted); PathsTotal
	// how many there were across the pushes.
	Paths      []string `json:"paths,omitempty"`
	PathsTotal int      `json:"pathsTotal,omitempty"`
	// WriterBackends are the backends of the runs that pushed in the lane
	// (a second-opinion reviewer must be on another one).
	WriterBackends []string `json:"writerBackends,omitempty"`
	// RunID is the last run that pushed in the lane.
	RunID string `json:"runId,omitempty"`
	// UpdatedAt is that push's time: a review verdict recorded before it
	// does not count.
	UpdatedAt time.Time `json:"updatedAt"`
}

// guardrailLaneEvent is the payload of EventGuardrailLane.
type guardrailLaneEvent struct {
	RunID  string
	Report guardrails.LaneReport
}

// mergeLaneQuery is the payload of EventMergeLaneCheck. pr is the pull
// request's number (0 when the caller did not say).
type mergeLaneQuery struct {
	reply chan string
	pr    int
}

// laneBackend names a run's backend for the second-opinion comparison: the
// configured default runner (claude) when the run recorded none.
func laneBackend(b string) string {
	return cmp.Or(strings.ToLower(strings.TrimSpace(b)), "claude")
}

// sanitizeLanePaths redacts and bounds the paths a shim or hook sent.
func sanitizeLanePaths(paths []string, total int) ([]string, int) {
	out := make([]string, 0, min(len(paths), guardrails.MaxLanePathsSent))
	for _, p := range paths[:min(len(paths), guardrails.MaxLanePathsSent)] {
		p = logging.RedactString(strings.TrimSpace(p))
		if len(p) > 300 {
			p = p[:300] + "…"
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out, max(total, len(out))
}

// GuardrailLane records that a push of identifier's run runID reached a
// lane above auto. Safe from any goroutine: a non-blocking send to the
// loop; ErrBusy when the loop is saturated (the hook then retries, and
// refuses the push if it cannot record it).
func (o *Orchestrator) GuardrailLane(identifier, runID string, rep guardrails.LaneReport) error {
	if !guardrails.IsLane(rep.Lane) {
		return fmt.Errorf("orchestrator: unknown guardrail lane %q", rep.Lane)
	}
	rep.Paths, rep.PathsTotal = sanitizeLanePaths(rep.Paths, rep.PathsTotal)
	select {
	case o.events <- OrchestratorEvent{Type: EventGuardrailLane, Identifier: identifier,
		guardrailLane: &guardrailLaneEvent{RunID: runID, Report: rep}}:
		return nil
	default:
		metrics.EventDropped()
		return ErrBusy
	}
}

// handleGuardrailLane folds a pushed change's lane into State.PushLanes.
// The writer's backend comes from the run's live entry (the push happens
// while it runs); an unknown run still records the lane, so merge policy
// only ever gets stricter. Event loop.
func (o *Orchestrator) handleGuardrailLane(state *State, ev OrchestratorEvent, now time.Time) {
	g := ev.guardrailLane
	if g == nil || ev.Identifier == "" {
		return
	}
	backend := ""
	for _, r := range state.Running {
		if r != nil && r.Issue.Identifier == ev.Identifier && r.SessionID == g.RunID {
			backend = laneBackend(r.Backend)
			break
		}
	}
	if _, tmux := state.TmuxSessions[ev.Identifier]; tmux && backend == "" {
		return // a tmux session (New session) has no pull request to merge
	}
	recordPushLane(state, ev.Identifier, g.RunID, backend, g.Report, now)
	rec := state.PushLanes[ev.Identifier]
	slog.Info("guardrails: push reached a lane", "issue_identifier", ev.Identifier, "run_id", g.RunID,
		"lane", g.Report.Lane, "issue_lane", rec.Lane, "paths", strings.Join(g.Report.Paths, ","))
	if o.logBuf != nil {
		o.logBuf.Add(ev.Identifier, makeBufLineWithSession("INFO", "guardrails: pushed "+g.Report.Lane+"-lane changes: "+
			guardrails.LanePathSummary(g.Report.Paths, g.Report.PathsTotal, guardrails.MaxLanePathsShown), g.RunID))
	}
	o.savePushLanesToDisk(clonePushLanes(state.PushLanes))
}

// recordPushLane merges one lane report into State.PushLanes: a higher
// lane replaces the record's lane and paths, the same lane adds its paths,
// a lower lane changes nothing but the writers. Event loop.
func recordPushLane(state *State, identifier, runID, backend string, rep guardrails.LaneReport, now time.Time) {
	if state.PushLanes == nil {
		state.PushLanes = make(map[string]PushLaneRecord)
	}
	rec, had := state.PushLanes[identifier]
	switch {
	case !had || guardrails.LaneRank(rep.Lane) > guardrails.LaneRank(rec.Lane):
		rec.Lane, rec.Paths, rec.PathsTotal = rep.Lane, nil, 0
		fallthrough
	case rep.Lane == rec.Lane:
		added := 0
		for _, p := range rep.Paths {
			if !slices.Contains(rec.Paths, p) {
				added++
				if len(rec.Paths) < maxPushLanePaths {
					rec.Paths = append(rec.Paths, p)
				}
			}
		}
		rec.PathsTotal += added + max(rep.PathsTotal-len(rep.Paths), 0)
		rec.RunID, rec.UpdatedAt = runID, now
	}
	if backend != "" && !slices.Contains(rec.WriterBackends, backend) {
		rec.WriterBackends = append(rec.WriterBackends, backend)
	}
	state.PushLanes[identifier] = rec
}

// Merge refusal reasons under guardrails.lanes (merge_pr's merge_blocked).
const (
	MergeRefusalLaneHuman         = "lane_human"
	MergeRefusalLaneSecondOpinion = "lane_second_opinion"
)

// mergeLaneRefusal is why merge_pr must not merge identifier's pull request
// number pr (0: unknown) under guardrails.lanes, or "" when it may. The
// lower, code-only pull request of a code-first stack split is judged by
// the code side's own lane; every other pull request by the issue's. Pure
// over State.
func mergeLaneRefusal(state State, identifier string, pr int) string {
	if split, ok := stackSplitLaneRecord(state, identifier, pr); ok {
		if split == nil {
			return ""
		}
		return laneRecordRefusal(state, identifier, *split)
	}
	rec, ok := state.PushLanes[identifier]
	if !ok {
		return ""
	}
	return laneRecordRefusal(state, identifier, rec)
}

// laneRecordRefusal is why a pull request whose pushes reached rec's lane
// must not be merged, or "" when it may. Pure over State.
func laneRecordRefusal(state State, identifier string, rec PushLaneRecord) string {
	paths := guardrails.LanePathSummary(rec.Paths, rec.PathsTotal, guardrails.MaxLanePathsShown)
	switch rec.Lane {
	case guardrails.LaneSecondOpinion:
		for _, v := range state.ReviewVerdicts[identifier] {
			if v.Verdict == ReviewVerdictApprove && v.Backend != "" &&
				!slices.Contains(rec.WriterBackends, laneBackend(v.Backend)) && !v.RecordedAt.Before(rec.UpdatedAt) {
				return ""
			}
		}
		writers := strings.Join(rec.WriterBackends, ", ")
		if writers == "" {
			writers = "unknown"
		}
		return MergeRefusalLaneSecondOpinion + ": this pull request changes second-opinion files (" + paths +
			"); merging it needs an approving review, recorded after the last such push, from a reviewer on a backend other than the writer's (" +
			writers + ")"
	default: // human, or anything unknown: fail closed
		return MergeRefusalLaneHuman + ": this pull request changes pipeline files (" + paths +
			"; guardrails.lanes.human). Merging it is a human's job: leave the pull request open for review"
	}
}

// MergeLaneRefusal asks the event loop whether merge_pr may merge
// identifier's pull request under guardrails.lanes: "" when it may, else
// the reason. The query goes through the loop, after any lane report the
// agent's push sent before it. Fails closed (a reason) when the loop does
// not answer. Safe from any goroutine.
func (o *Orchestrator) MergeLaneRefusal(ctx context.Context, identifier string) string {
	return o.MergeLaneRefusalForPR(ctx, identifier, 0)
}

// MergeLaneRefusalForPR is MergeLaneRefusal for pull request number pr of
// identifier: a stack split's lower, code-only pull request is judged by
// its own lane (guardrails.lanes.split). pr 0 judges by the issue's lane.
// Safe from any goroutine.
func (o *Orchestrator) MergeLaneRefusalForPR(ctx context.Context, identifier string, pr int) string {
	done := o.loopExitedCh()
	if done == nil {
		return "lane_unknown: the orchestrator is not running, so the pushed lanes cannot be checked"
	}
	reply := make(chan string, 1)
	select {
	case o.events <- OrchestratorEvent{Type: EventMergeLaneCheck, Identifier: identifier, mergeLane: &mergeLaneQuery{reply: reply, pr: pr}}:
	case <-done:
		return "lane_unknown: the orchestrator stopped"
	case <-ctx.Done():
		return "lane_unknown: " + ctx.Err().Error()
	}
	select {
	case r := <-reply:
		return r
	case <-done:
		select {
		case r := <-reply:
			return r
		default:
			return "lane_unknown: the orchestrator stopped"
		}
	case <-ctx.Done():
		return "lane_unknown: " + ctx.Err().Error()
	}
}

// handleMergeLaneCheck answers an EventMergeLaneCheck. Event loop; changes
// no State.
func (o *Orchestrator) handleMergeLaneCheck(state State, ev OrchestratorEvent) {
	o.eventLeftStateUnchanged = true
	if ev.mergeLane == nil {
		return
	}
	reason := mergeLaneRefusal(state, ev.Identifier, ev.mergeLane.pr)
	if reason != "" {
		kind, _, _ := strings.Cut(reason, ":")
		slog.Warn("guardrails: merge_pr refused", "issue_identifier", ev.Identifier, "reason", kind)
		if o.logBuf != nil {
			o.logBuf.Add(ev.Identifier, makeBufLine("WARN", "guardrails: merge_pr refused: "+reason))
		}
	}
	ev.mergeLane.reply <- reason
}

// stampVerdictBackend records the backend of the reviewer whose verdict
// was just recorded for identifier (the second-opinion comparison). Event
// loop.
func stampVerdictBackend(state *State, identifier, profile, backend string) {
	vs := state.ReviewVerdicts[identifier]
	for i := range vs {
		if vs[i].Profile == profile {
			vs[i].Backend = laneBackend(backend)
		}
	}
}

// prunePushLanes drops the lane records of issues keep rejects (terminal
// issues) and persists the result when anything went. Event loop.
func (o *Orchestrator) prunePushLanes(state *State, keep func(ident string) bool) {
	if pruneMap(state.PushLanes, identOfKey, keep) > 0 {
		o.savePushLanesToDisk(clonePushLanes(state.PushLanes))
	}
}

// clonePushLanes deep-copies State.PushLanes.
func clonePushLanes(m map[string]PushLaneRecord) map[string]PushLaneRecord {
	if m == nil {
		return nil
	}
	cp := make(map[string]PushLaneRecord, len(m))
	for k, v := range m {
		v.Paths = cloneSlice(v.Paths)
		v.WriterBackends = cloneSlice(v.WriterBackends)
		cp[k] = v
	}
	return cp
}

// SetPushLanesFile sets the path State.PushLanes persists to
// (.itervox/push_lanes.json), so a restart never forgets that an issue's
// pull request needs a human or a second opinion. Before Run.
func (o *Orchestrator) SetPushLanesFile(path string) { o.pushLanesFile = path }

// loadPushLanesFromDisk pre-populates State.PushLanes at startup. A missing
// file is not an error; a corrupt one is logged and ignored.
func (o *Orchestrator) loadPushLanesFromDisk(state State) State {
	if o.pushLanesFile == "" {
		return state
	}
	data, err := os.ReadFile(o.pushLanesFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("orchestrator: failed to load push lanes", "path", o.pushLanesFile, "error", err)
		}
		return state
	}
	var disk map[string]PushLaneRecord
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse push lanes", "path", o.pushLanesFile, "error", err)
		return state
	}
	if state.PushLanes == nil {
		state.PushLanes = make(map[string]PushLaneRecord, len(disk))
	}
	for id, rec := range disk {
		if id != "" && guardrails.IsLane(rec.Lane) {
			state.PushLanes[id] = rec
		}
	}
	return state
}

// savePushLanesToDisk hands a copy of the lane records to the ledger
// writer. Never reads live state.
func (o *Orchestrator) savePushLanesToDisk(lanes map[string]PushLaneRecord) {
	if o.pushLanesFile == "" {
		return
	}
	data, err := json.Marshal(maps.Clone(lanes))
	if err != nil {
		slog.Warn("orchestrator: failed to marshal push lanes", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(o.pushLanesFile), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create push lanes dir", "path", o.pushLanesFile, "error", err)
		return
	}
	o.persistLedger(ledgerPushLanes, o.pushLanesFile, data, 0o600)
}
