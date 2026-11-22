package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/prdetector"
)

// PR outcomes (cost and outcome visibility): what became of each pull
// request an itervox run opened or continued. State.PROutcomes is event-loop
// state keyed by PR URL. A run's exit that carries a PR records it as open
// (recordPROutcomeOpened, next to recordManagedPR); off-loop producers report
// what they observed through RecordPROutcome (EventPROutcome): the PR-event
// watcher's poll and webhook (internal/prwatch, GitHub only) and the
// daemon's own merge_pr action. The reverted-PR scan (#217, the base
// branch's revert commits) marks a merged PR reverted. Unlike ManagedPRs the
// ledger is NOT pruned when the issue turns terminal (a PR usually merges
// right as the issue moves to Done): entries are pruned by age
// (prOutcomeRetention since their last change) and bounded (maxPROutcomes,
// oldest change first). Persisted to pr_outcomes.json by the ledger writer.

// PR outcome states.
const (
	PROutcomeOpen     = "open"
	PROutcomeMerged   = "merged"
	PROutcomeClosed   = "closed" // closed without merging
	PROutcomeReverted = "reverted"
)

const (
	// maxPROutcomes bounds State.PROutcomes.
	maxPROutcomes = 1000
	// prOutcomeRetention is how long an entry is kept after its last change.
	prOutcomeRetention = 90 * 24 * time.Hour
	// prOutcomeSendBound bounds RecordPROutcome's wait for the event loop.
	prOutcomeSendBound = 5 * time.Second
)

// EventPROutcome carries a PROutcomeReport to the event loop.
const EventPROutcome EventType = "PROutcome"

// PROutcome is what became of one PR an itervox run recorded.
type PROutcome struct {
	PRURL      string `json:"pr_url"`
	Identifier string `json:"identifier"`
	IssueID    string `json:"issue_id,omitempty"`
	// RunID is the run whose exit first recorded the PR.
	RunID string `json:"run_id,omitempty"`
	State string `json:"state"`
	// OpenedAt is when a run first recorded the PR; ClosedAt when it was
	// seen merged or closed; RevertedAt when a revert of it was found.
	OpenedAt   time.Time `json:"opened_at"`
	ClosedAt   time.Time `json:"closed_at,omitzero"`
	RevertedAt time.Time `json:"reverted_at,omitzero"`
	// UpdatedAt is the entry's last change (retention is counted from it).
	UpdatedAt time.Time `json:"updated_at"`
	// Source names what reported the latest transition: "poll", "webhook",
	// "merge_pr" or "revert_scan".
	Source string `json:"source,omitempty"`
}

// PROutcomeReport is an observation of a PR's state from off the loop.
// State is PROutcomeMerged, PROutcomeClosed or PROutcomeOpen (reopened).
type PROutcomeReport struct {
	PRURL  string
	State  string
	At     time.Time
	Source string
}

// SetPROutcomesFile sets the path that persists State.PROutcomes. Must be
// called before Run.
func (o *Orchestrator) SetPROutcomesFile(path string) {
	o.prOutcomesFile = path
}

// RecordPROutcome hands an observed PR state to the event loop, waiting at
// most prOutcomeSendBound. A dropped report is not lost for good: the
// watcher reports a PR again on every poll while the ledger still shows it
// open. Safe to call from any goroutine.
func (o *Orchestrator) RecordPROutcome(ctx context.Context, rep PROutcomeReport) bool {
	if rep.PRURL == "" {
		return false
	}
	if p := o.loopExited.Load(); p != nil {
		select {
		case <-*p:
			return false
		default:
		}
	}
	timer := time.NewTimer(prOutcomeSendBound)
	defer timer.Stop()
	select {
	case o.events <- OrchestratorEvent{Type: EventPROutcome, PROutcome: &rep}:
		return true
	case <-ctx.Done():
	case <-timer.C:
		metrics.EventDropped()
		slog.Warn("orchestrator: PR outcome report dropped (event queue full)", "pr_url", rep.PRURL, "state", rep.State)
	}
	return false
}

// PROutcomes returns the PR outcome ledger as of the last snapshot, sorted
// by URL. Safe to call from any goroutine.
func (o *Orchestrator) PROutcomes() []PROutcome {
	o.snapMu.RLock()
	out := make([]PROutcome, 0, len(o.lastSnap.PROutcomes))
	for _, pr := range o.lastSnap.PROutcomes {
		out = append(out, pr)
	}
	o.snapMu.RUnlock()
	slices.SortFunc(out, func(a, b PROutcome) int { return strings.Compare(a.PRURL, b.PRURL) })
	return out
}

// TrackedPROutcomes returns the PRs whose outcome can still change (open,
// or closed unmerged: it can be reopened), for the watcher's outcome poll.
func (o *Orchestrator) TrackedPROutcomes() []PROutcome {
	all := o.PROutcomes()
	out := all[:0]
	for _, pr := range all {
		if pr.State == PROutcomeOpen || pr.State == PROutcomeClosed {
			out = append(out, pr)
		}
	}
	return out
}

// recordPROutcomeOpened records the PR a finished run's exit carries as
// open when the ledger does not know it yet. Event loop only.
func recordPROutcomeOpened(state *State, exit *RunEntry, runID string, now time.Time) {
	if exit == nil || exit.PRURL == "" {
		return
	}
	if state.PROutcomes == nil {
		state.PROutcomes = make(map[string]PROutcome)
	}
	if _, known := state.PROutcomes[exit.PRURL]; known {
		return
	}
	state.PROutcomes[exit.PRURL] = PROutcome{
		PRURL: exit.PRURL, Identifier: exit.Issue.Identifier, IssueID: exit.Issue.ID, RunID: runID,
		State: PROutcomeOpen, OpenedAt: now, UpdatedAt: now,
	}
	prunePROutcomes(state, now)
}

// applyPROutcome applies an observed PR state. Transitions: open → merged
// or closed; closed → merged (merged after a reopen we did not see) or open
// (reopened); merged → reverted only through markPROutcomesReverted;
// reverted is final. A report about a PR the ledger does not know changes
// nothing. Reports whether the entry changed. Event loop only.
func applyPROutcome(state *State, rep *PROutcomeReport, now time.Time) bool {
	if rep == nil || state.PROutcomes == nil {
		return false
	}
	pr, ok := state.PROutcomes[rep.PRURL]
	if !ok {
		return false
	}
	at := rep.At
	if at.IsZero() {
		at = now
	}
	switch {
	case rep.State == PROutcomeMerged && (pr.State == PROutcomeOpen || pr.State == PROutcomeClosed):
		pr.State, pr.ClosedAt = PROutcomeMerged, at
	case rep.State == PROutcomeClosed && pr.State == PROutcomeOpen:
		pr.State, pr.ClosedAt = PROutcomeClosed, at
	case rep.State == PROutcomeOpen && pr.State == PROutcomeClosed:
		pr.State, pr.ClosedAt = PROutcomeOpen, time.Time{}
	default:
		return false
	}
	pr.UpdatedAt, pr.Source = now, rep.Source
	state.PROutcomes[rep.PRURL] = pr
	metrics.PROutcome(pr.State)
	return true
}

// markPROutcomesReverted marks the merged PRs whose number a revert scan
// reported as reverted. A PR number is matched against the ledger's merged
// PRs (the scan reads the project repository's base branch). Reports how
// many changed. Event loop only.
func markPROutcomesReverted(state *State, reverted []RevertedPR, now time.Time) int {
	if len(reverted) == 0 || len(state.PROutcomes) == 0 {
		return 0
	}
	numbers := make(map[int]bool, len(reverted))
	for _, rv := range reverted {
		if rv.Number > 0 {
			numbers[rv.Number] = true
		}
	}
	changed := 0
	for url, pr := range state.PROutcomes {
		if pr.State != PROutcomeMerged || !numbers[prdetector.PRNumber(url)] {
			continue
		}
		pr.State, pr.RevertedAt, pr.UpdatedAt, pr.Source = PROutcomeReverted, now, now, "revert_scan"
		state.PROutcomes[url] = pr
		metrics.PROutcome(PROutcomeReverted)
		changed++
	}
	return changed
}

// hasMergedPROutcome reports whether a revert scan could change the ledger.
func hasMergedPROutcome(state State) bool {
	for _, pr := range state.PROutcomes {
		if pr.State == PROutcomeMerged {
			return true
		}
	}
	return false
}

// prunePROutcomes drops entries unchanged for prOutcomeRetention, then the
// oldest-changed entries past maxPROutcomes. Event loop only.
func prunePROutcomes(state *State, now time.Time) {
	for url, pr := range state.PROutcomes {
		if now.Sub(pr.UpdatedAt) > prOutcomeRetention {
			delete(state.PROutcomes, url)
		}
	}
	for len(state.PROutcomes) > maxPROutcomes {
		oldest, oldestAt := "", time.Time{}
		for url, pr := range state.PROutcomes {
			if oldest == "" || pr.UpdatedAt.Before(oldestAt) || (pr.UpdatedAt.Equal(oldestAt) && url < oldest) {
				oldest, oldestAt = url, pr.UpdatedAt
			}
		}
		delete(state.PROutcomes, oldest)
	}
}

// prOutcomesDisk is the pr_outcomes.json shape.
type prOutcomesDisk struct {
	PROutcomes map[string]PROutcome `json:"pr_outcomes,omitempty"`
}

// loadPROutcomesFromDisk restores the ledger at startup. A missing file is
// normal; a corrupt one is logged and ignored.
func (o *Orchestrator) loadPROutcomesFromDisk(state State) State {
	path := o.prOutcomesFile
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to read PR outcomes file", "path", path, "error", err)
		}
		return state
	}
	var disk prOutcomesDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: ignoring corrupt PR outcomes file", "path", path, "error", err)
		return state
	}
	if state.PROutcomes == nil {
		state.PROutcomes = make(map[string]PROutcome, len(disk.PROutcomes))
	}
	for url, pr := range disk.PROutcomes {
		if url == "" || pr.Identifier == "" {
			continue
		}
		switch pr.State {
		case PROutcomeOpen, PROutcomeMerged, PROutcomeClosed, PROutcomeReverted:
		default:
			continue
		}
		pr.PRURL = url
		state.PROutcomes[url] = pr
	}
	prunePROutcomes(&state, time.Now())
	return state
}

// savePROutcomesToDisk persists the ledger from a snapshot clone. The ledger
// writer's dirty check makes an unchanged ledger free.
func (o *Orchestrator) savePROutcomesToDisk(snap State) {
	path := o.prOutcomesFile
	if path == "" {
		return
	}
	data, err := json.Marshal(prOutcomesDisk{PROutcomes: snap.PROutcomes})
	if err != nil {
		slog.Warn("orchestrator: failed to marshal PR outcomes", "error", err)
		return
	}
	o.persistLedger(ledgerPROutcomes, path, data, 0o644)
}
