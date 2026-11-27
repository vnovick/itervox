package orchestrator

import (
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// Policy changes while agents run (approval integrity). A change to a file
// that governs agents is made loud when any run is active:
//
//   - the config watcher (cmd/itervox) sees WORKFLOW.md or a settings file
//     change and calls NotePolicyFilesChanged (a non-blocking send of
//     EventPolicyFilesChanged);
//   - startRun, which re-reads the governing files for every dispatch,
//     compares them with what the previous dispatch saw
//     (State.PolicyBaseline);
//   - a run's exit carries what changed since the run started
//     (RunEntry.PolicyChanged, computed by its worker).
//
// Each logs at Warn and emits a policy_changed feed event naming the files
// (names only) and the active runs; the first two also tell the policy
// change hook (the dashboard banner and `itervox watch` read the alert).
// When a changed file lies in the daemon's project directory and a running
// agent could write there — it runs on this host without a sandbox — the
// event says so (PossiblyAgent). Event-loop state only: State.PolicyBaseline.

// FeedPolicyChanged is the feed kind of a governing-file change seen while
// runs were active.
const FeedPolicyChanged = "policy_changed"

// EventPolicyFilesChanged carries governing files the config watcher saw
// change (NotePolicyFilesChanged).
const EventPolicyFilesChanged EventType = "PolicyFilesChanged"

// Policy-change sources (FeedPayload.Source, PolicyChangeAlert.Source).
const (
	PolicySourceWatcher  = "config_watcher"
	PolicySourceDispatch = "dispatch"
	PolicySourceRunExit  = "run_exit"
)

// maxPolicyNames bounds the file and run lists one event carries.
const maxPolicyNames = 32

// PolicyChangeAlert is a governing-file change seen while runs were active:
// the files (names only), the runs then active, and whether a running agent
// could have made it.
type PolicyChangeAlert struct {
	At            time.Time
	Source        string
	Files         []string
	RunIDs        []string
	PossiblyAgent bool
}

// SetPolicyChangeHook installs the function told about every policy change
// seen while runs were active (cmd/itervox: the dashboard banner). Called
// from the event loop; it must not block. Must be called before Run.
func (o *Orchestrator) SetPolicyChangeHook(fn func(PolicyChangeAlert)) {
	o.onPolicyChange = fn
}

// NotePolicyFilesChanged reports governing files the config watcher saw
// change (absolute paths or names). Safe from any goroutine; never blocks:
// a full channel drops the report (counted).
func (o *Orchestrator) NotePolicyFilesChanged(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	if p := o.loopExited.Load(); p != nil {
		select {
		case <-*p:
			return false
		default:
		}
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventPolicyFilesChanged, policyFiles: slices.Clone(paths)}:
		return true
	default:
		metrics.EventDropped()
		slog.Warn("orchestrator: policy change report dropped (event channel full)", "files", paths)
		return false
	}
}

// projectDir is WORKFLOW.md's directory ("" without a workflow path).
func (o *Orchestrator) projectDir() string {
	if o.cfg == nil || o.cfg.WorkflowPath == "" {
		return ""
	}
	if abs, err := filepath.Abs(filepath.Dir(o.cfg.WorkflowPath)); err == nil {
		return abs
	}
	return filepath.Dir(o.cfg.WorkflowPath)
}

// policyNames names paths for an event: relative to the project directory
// when inside it, bounded.
func (o *Orchestrator) policyNames(paths []string) []string {
	base := o.projectDir()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		n := p
		if filepath.IsAbs(p) && base != "" {
			n = policyFileName(base, p)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), maxPolicyNames)]
}

// inProjectDir reports whether a changed name is a file inside the
// daemon's project directory (not a user-layer file, a file elsewhere, or a
// config key).
func inProjectDir(name string) bool {
	return !strings.HasPrefix(name, "~/") && !strings.HasPrefix(name, policyKeyPrefix) &&
		!filepath.IsAbs(filepath.FromSlash(name))
}

// agentCouldWrite reports whether run's agent could have written a file in
// the daemon's project directory: it runs on this host without a sandbox.
func agentCouldWrite(run *RunEntry) bool {
	return run != nil && run.WorkerHost == "" && run.Isolation == ""
}

// activeRunsExcept lists the running runs (but skipIssueID's), sorted, and
// whether any of them could write the project directory.
func activeRunsExcept(state *State, skipIssueID string) (ids []string, agentWritable bool) {
	for id, r := range state.Running {
		if r == nil || id == skipIssueID || r.RunID == "" {
			continue
		}
		ids = append(ids, r.RunID)
		agentWritable = agentWritable || agentCouldWrite(r)
	}
	slices.Sort(ids)
	return ids[:min(len(ids), maxPolicyNames)], agentWritable
}

// possiblyAgent: some changed file is in the project directory and an
// active agent could write there.
func possiblyAgent(files []string, agentWritable bool) bool {
	return agentWritable && slices.ContainsFunc(files, inProjectDir)
}

// raisePolicyChange logs, feeds and hooks one policy change seen while runs
// were active. Event loop only.
func (o *Orchestrator) raisePolicyChange(state *State, source string, files, runIDs []string, byAgent bool) {
	msg := "policy: files that govern agents changed while runs were active"
	if byAgent {
		msg += " (possibly changed by a running agent)"
	}
	slog.Warn(msg, "source", source, "files", files, "runs", runIDs)
	o.emitFeed(state, FeedPolicyChanged, "", FeedPayload{Source: source, Files: files, RunIDs: runIDs, PossiblyAgent: byAgent})
	if o.onPolicyChange != nil {
		o.onPolicyChange(PolicyChangeAlert{At: time.Now().UTC(), Source: source,
			Files: slices.Clone(files), RunIDs: slices.Clone(runIDs), PossiblyAgent: byAgent})
	}
}

// applyPolicyFilesChanged handles EventPolicyFilesChanged. Event loop.
func (o *Orchestrator) applyPolicyFilesChanged(state *State, ev OrchestratorEvent) {
	files := o.policyNames(ev.policyFiles)
	runIDs, writable := activeRunsExcept(state, "")
	if len(files) == 0 || len(runIDs) == 0 {
		return
	}
	o.raisePolicyChange(state, PolicySourceWatcher, files, runIDs, possiblyAgent(files, writable))
}

// observeDispatchPolicy compares the governing files a dispatch read with
// what the previous dispatch saw, and raises a change seen while other runs
// were active. Event loop (startRun).
func (o *Orchestrator) observeDispatchPolicy(state *State, entry *RunEntry, files map[string]string) {
	if state == nil || files == nil {
		return
	}
	was := state.PolicyBaseline
	state.PolicyBaseline = cloneFiles(files)
	if was == nil {
		return
	}
	changed := policyFileChanges(was, files)
	if len(changed) == 0 {
		return
	}
	runIDs, writable := activeRunsExcept(state, entry.Issue.ID)
	if len(runIDs) == 0 {
		return
	}
	changed = changed[:min(len(changed), maxPolicyNames)]
	o.raisePolicyChange(state, PolicySourceDispatch, changed, runIDs, possiblyAgent(changed, writable))
}

// notePolicyChangedOnExit records what changed while the run ran (its exit
// carries it) on the run's entry for history, and reports it. Event loop
// (EventWorkerExited).
func (o *Orchestrator) notePolicyChangedOnExit(state *State, live *RunEntry, ev OrchestratorEvent) {
	if ev.RunEntry == nil || len(ev.RunEntry.PolicyChanged) == 0 {
		return
	}
	changed := slices.Clone(ev.RunEntry.PolicyChanged)
	changed = changed[:min(len(changed), maxPolicyNames)]
	if live != nil {
		live.PolicyChanged = changed
	}
	byAgent := agentCouldWrite(live) && slices.ContainsFunc(changed, inProjectDir)
	runID := cmpRunID(ev.RunID, live)
	msg := "policy: files or settings that govern this run changed while it ran"
	if byAgent {
		msg += " (possibly changed by a running agent)"
	}
	slog.Warn(msg, "issue_identifier", ev.RunEntry.Issue.Identifier, "run_id", runID, "changed", changed)
	p := feedRunPayload(live)
	if live == nil {
		p = FeedPayload{RunID: runID}
	}
	p.Source, p.Files, p.RunIDs, p.PossiblyAgent = PolicySourceRunExit, changed, []string{runID}, byAgent
	o.emitFeed(state, FeedPolicyChanged, ev.RunEntry.Issue.Identifier, p)
}
