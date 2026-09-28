package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// CORE-053 — backend_health.json, next to auto_switched.json in the daemon
// log directory (runtime state, never committed). Written through the
// CORE-038 ledger writer: marshalled on the event loop, dirty-checked (an
// unchanged breaker set costs no write), latest-wins. Only limited and
// probing breakers are persisted: a warning is a 5-minute window, and a
// probe reservation names a run that cannot survive a restart.

// backendHealthFileVersion is the backend_health.json envelope version.
const backendHealthFileVersion = 1

type backendHealthRecord struct {
	Backend      string    `json:"backend"`
	Host         string    `json:"host,omitempty"`
	Status       string    `json:"status"`
	Kind         string    `json:"kind,omitempty"`
	LimitType    string    `json:"limit_type,omitempty"`
	Source       string    `json:"source,omitempty"`
	LimitedUntil time.Time `json:"limited_until"`
	ResetKnown   bool      `json:"reset_known"`
	Since        time.Time `json:"since"`
	Hits         int       `json:"hits,omitempty"`
	LastIssue    string    `json:"last_issue,omitempty"`
}

type backendHealthEnvelope struct {
	Version  int                            `json:"version"`
	Backends map[string]backendHealthRecord `json:"backends"`
}

// SetBackendHealthFile sets where the backend circuit breakers are persisted
// (CORE-053). Must be called before Run; empty disables persistence.
func (o *Orchestrator) SetBackendHealthFile(path string) {
	o.autoSwitchedMu.Lock()
	o.backendHealthFile = path
	o.autoSwitchedMu.Unlock()
}

// saveBackendHealthToDisk persists the open breakers. Event loop only.
func (o *Orchestrator) saveBackendHealthToDisk(state *State) {
	o.autoSwitchedMu.RLock()
	path := o.backendHealthFile
	o.autoSwitchedMu.RUnlock()
	if path == "" || state == nil {
		return
	}
	env := backendHealthEnvelope{Version: backendHealthFileVersion, Backends: map[string]backendHealthRecord{}}
	for key, e := range state.BackendHealth {
		if e.Status != BackendStatusLimited && e.Status != BackendStatusProbing {
			continue
		}
		env.Backends[key] = backendHealthRecord{
			Backend: e.Backend, Host: e.Host, Status: e.Status, Kind: e.Kind,
			LimitType: e.LimitType, Source: e.Source, LimitedUntil: e.LimitedUntil.UTC(),
			ResetKnown: e.ResetKnown, Since: e.Since.UTC(), Hits: e.Hits, LastIssue: e.LastIssue,
		}
	}
	data, err := json.Marshal(env)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal backend health", "error", err)
		return
	}
	o.persistLedger(ledgerBackendHealth, path, data, 0o644)
}

// loadBackendHealthFromDisk restores the open breakers at startup. A
// probing breaker comes back without its reservation (no run survives a
// restart); expiry is applied by the first tick. Missing or malformed files
// are logged and ignored.
func (o *Orchestrator) loadBackendHealthFromDisk(state State) State {
	o.autoSwitchedMu.RLock()
	path := o.backendHealthFile
	o.autoSwitchedMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load backend health file", "path", path, "error", err)
		}
		return state
	}
	var env backendHealthEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		o.quarantineLedgerFile(path, data, err) // BH-M3-7
		return state
	}
	if env.Version != backendHealthFileVersion {
		o.quarantineLedgerFile(path, data, fmt.Errorf("orchestrator: unsupported backend_health.json version %d (want %d)", env.Version, backendHealthFileVersion))
		return state
	}
	if state.BackendHealth == nil {
		state.BackendHealth = make(map[string]BackendHealthEntry)
	}
	now := time.Now()
	for key, r := range env.Backends {
		if r.Status != BackendStatusLimited && r.Status != BackendStatusProbing {
			continue
		}
		// V1: an already-persisted far reset (written before the caps
		// existed, or hand-edited) heals on load.
		until := clampLimitedUntil(r.LimitedUntil, now, r.Kind, r.Source, r.LimitType)
		state.BackendHealth[key] = BackendHealthEntry{
			Backend: r.Backend, Host: r.Host, Status: r.Status, Kind: r.Kind,
			LimitType: r.LimitType, Source: r.Source, LimitedUntil: until,
			ResetKnown: r.ResetKnown, Since: r.Since, Hits: r.Hits, LastIssue: r.LastIssue,
		}
	}
	slog.Info("orchestrator: loaded backend health", "path", path, "open_breakers", len(state.BackendHealth))
	return state
}
