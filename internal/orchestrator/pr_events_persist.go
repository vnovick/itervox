package orchestrator

import (
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"time"
)

// prEventsDisk is the pr_events.json shape (#173).
type prEventsDisk struct {
	ManagedPRs map[string]ManagedPR `json:"managed_prs,omitempty"`
	Dispatched map[string]time.Time `json:"dispatched,omitempty"`
	Baselines  map[string]string    `json:"baselines,omitempty"`
}

// SetPREventsFile sets the path that persists the managed PRs and the
// PR-event dedup ledger (#173), so a restart neither forgets which PRs
// itervox manages nor dispatches an event twice. Must be called before Run.
func (o *Orchestrator) SetPREventsFile(path string) {
	o.prEventsFile = path
}

// loadPREventsFromDisk restores the PR-event ledgers at startup. A missing
// file is normal; a corrupt one is logged and ignored.
func (o *Orchestrator) loadPREventsFromDisk(state State) State {
	path := o.prEventsFile
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to read PR events file", "path", path, "error", err)
		}
		return state
	}
	var disk prEventsDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: ignoring corrupt PR events file", "path", path, "error", err)
		return state
	}
	if state.ManagedPRs == nil {
		state.ManagedPRs = make(map[string]ManagedPR, len(disk.ManagedPRs))
	}
	for url, pr := range disk.ManagedPRs {
		if url == "" || pr.Identifier == "" {
			continue
		}
		pr.PRURL = url
		state.ManagedPRs[url] = pr
	}
	if state.PREventDispatched == nil {
		state.PREventDispatched = make(map[string]time.Time, len(disk.Dispatched))
	}
	maps.Copy(state.PREventDispatched, disk.Dispatched)
	if state.PREventBaselines == nil {
		state.PREventBaselines = make(map[string]string, len(disk.Baselines))
	}
	maps.Copy(state.PREventBaselines, disk.Baselines)
	if len(state.ManagedPRs) > 0 {
		slog.Info("orchestrator: loaded managed pull requests", "path", path, "count", len(state.ManagedPRs))
	}
	return state
}

// savePREventsToDisk persists the PR-event ledgers from a snapshot clone.
// The ledger writer's dirty check makes an unchanged ledger free.
func (o *Orchestrator) savePREventsToDisk(snap State) {
	path := o.prEventsFile
	if path == "" {
		return
	}
	data, err := json.Marshal(prEventsDisk{
		ManagedPRs: snap.ManagedPRs,
		Dispatched: snap.PREventDispatched,
		Baselines:  snap.PREventBaselines,
	})
	if err != nil {
		slog.Warn("orchestrator: failed to marshal PR events", "error", err)
		return
	}
	o.persistLedger(ledgerPREvents, path, data, 0o644)
}
