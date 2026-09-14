package orchestrator

import (
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// SetDepsOverride enables or disables an operator dismissal of the
// LLM-inferred dependency gating layer for the given target (blocked) issue
// identifier. enabled=true dismisses any inferred edges gating identifier's
// dispatch (State.DepsOverrides[identifier] is set to the current time);
// enabled=false clears the dismissal, restoring gating on the next
// reconciliation.
//
// Sends EventSetDepsOverride to the event loop (non-blocking); the loop
// mutates state.DepsOverrides and recomputes identifier's InferredDeps
// entries in place so the effect is visible on the very next snapshot.
// Returns false only when the event channel is full — the caller may retry.
// Safe to call from any goroutine.
func (o *Orchestrator) SetDepsOverride(identifier string, enabled bool) bool {
	select {
	case o.events <- OrchestratorEvent{Type: EventSetDepsOverride, Identifier: identifier, Enabled: enabled}:
		slog.Info("orchestrator: deps override queued", "identifier", identifier, "enabled", enabled)
		return true
	default:
		slog.Warn("orchestrator: deps override event channel full", "identifier", identifier)
		return false
	}
}

// applyDepsOverrideEvent handles EventSetDepsOverride: mutates
// state.DepsOverrides, persists it to disk, and recomputes the target
// identifier's InferredDeps entries in place so the dispatch guard
// (IneligibleReason) and the published snapshot both reflect the change on
// this same tick — without waiting for the next onTick's full
// ReconcileInferredDeps pass. Runs in the event-loop goroutine only.
func (o *Orchestrator) applyDepsOverrideEvent(state State, ev OrchestratorEvent) State {
	if state.DepsOverrides == nil {
		state.DepsOverrides = make(map[string]time.Time)
	}
	if ev.Enabled {
		state.DepsOverrides[ev.Identifier] = time.Now()
	} else {
		delete(state.DepsOverrides, ev.Identifier)
	}
	o.saveDepsOverridesToDisk(maps.Clone(state.DepsOverrides))

	if entries, ok := state.InferredDeps[ev.Identifier]; ok {
		inferredGatingEnabled := resolveDependenciesConfig(o.cfg).InferredGating
		updated := make([]InferredDepEntry, len(entries))
		for i, entry := range entries {
			entry.Overridden = ev.Enabled
			entry.Gating = inferredGatingFor(entry, inferredGatingEnabled)
			updated[i] = entry
		}
		state.InferredDeps[ev.Identifier] = updated
	}

	slog.Info("orchestrator: deps override applied", "identifier", ev.Identifier, "enabled", ev.Enabled)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
	return state
}

// SetDepsOverridesFile sets the path for persisting operator dependency
// overrides across restarts. Must be called before Run.
func (o *Orchestrator) SetDepsOverridesFile(path string) {
	o.depsOverridesMu.Lock()
	o.depsOverridesFile = path
	o.depsOverridesMu.Unlock()
}

// loadDepsOverridesFromDisk reads the deps-overrides file (if set) and
// pre-populates state.DepsOverrides. Called once at startup before the event
// loop begins. A missing file is not an error; a corrupt file is logged and
// otherwise ignored so a bad file never blocks daemon startup.
func (o *Orchestrator) loadDepsOverridesFromDisk(state State) State {
	o.depsOverridesMu.RLock()
	path := o.depsOverridesFile
	o.depsOverridesMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load deps overrides file", "path", path, "error", err)
		}
		return state
	}
	var disk map[string]time.Time
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse deps overrides file", "path", path, "error", err)
		return state
	}
	if state.DepsOverrides == nil {
		state.DepsOverrides = make(map[string]time.Time, len(disk))
	}
	maps.Copy(state.DepsOverrides, disk)
	slog.Info("orchestrator: loaded deps overrides", "path", path, "count", len(disk))
	return state
}

// saveDepsOverridesToDisk writes DepsOverrides to disk. Must NOT be called
// with snapMu held. The overrides arg is a clone provided by the caller —
// this function never reads live event-loop state to avoid races.
func (o *Orchestrator) saveDepsOverridesToDisk(overrides map[string]time.Time) {
	o.depsOverridesMu.RLock()
	path := o.depsOverridesFile
	o.depsOverridesMu.RUnlock()
	if path == "" {
		return
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal deps overrides", "error", err)
		return
	}
	// Unlike paused.json/automation_queue.json (under --log's dir, created by
	// main.go's os.MkdirAll before Run starts), the deps-overrides file lives
	// under the project's .itervox/ dir, which may not exist yet on a fresh
	// checkout — create it defensively, mirroring depsanalysis.SaveSidecar.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create deps overrides dir", "path", path, "error", err)
		return
	}
	if err := writeFileAtomically(path, data, 0o644); err != nil {
		slog.Warn("orchestrator: failed to write deps overrides file", "path", path, "error", err)
	}
}
