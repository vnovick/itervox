package orchestrator

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// Per-issue profile overrides (the operator's POST /issues/{id}/profile, a
// tracker `/itervox profile` command) live in o.issueProfiles, not State:
// SetIssueProfile is synchronous so the dashboard sees the assignment at
// once. Like State.PlaybookOverrides (#218) they are an operator choice, so
// they persist to .itervox/issue_profiles.json through the ledger writer
// and are loaded again by every orchestrator generation — which is what
// carries them across a WORKFLOW.md / settings reload (cmd/itervox builds a
// new Orchestrator per generation) as well as a restart.

// issueProfilesFileVersion is the issue_profiles.json envelope version.
const issueProfilesFileVersion = 1

type issueProfileDisk struct {
	Profile string `json:"profile"`
	SetAt   string `json:"set_at,omitempty"`
}

type issueProfilesEnvelope struct {
	Version   int                         `json:"version"`
	Overrides map[string]issueProfileDisk `json:"overrides"`
}

// SetIssueProfilesFile sets the path the per-issue profile overrides persist
// to and loads what it holds. An override already set in memory wins over
// the file's. A missing file is not an error; a corrupt one is logged and
// ignored so it never blocks startup. Call before Run.
func (o *Orchestrator) SetIssueProfilesFile(path string) {
	o.issueProfilesMu.Lock()
	defer o.issueProfilesMu.Unlock()
	o.issueProfilesFile = path
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load issue profile overrides", "path", path, "error", err)
		}
		return
	}
	var env issueProfilesEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Warn("orchestrator: failed to parse issue profile overrides", "path", path, "error", err)
		return
	}
	if env.Version != issueProfilesFileVersion {
		slog.Warn("orchestrator: unsupported issue profile overrides version, ignoring",
			"path", path, "version", env.Version, "want", issueProfilesFileVersion)
		return
	}
	if o.issueProfiles == nil {
		o.issueProfiles = make(map[string]string, len(env.Overrides))
	}
	if o.issueProfileSetAt == nil {
		o.issueProfileSetAt = make(map[string]time.Time, len(env.Overrides))
	}
	loaded := 0
	for id, rec := range env.Overrides {
		if id == "" || rec.Profile == "" {
			continue
		}
		if _, set := o.issueProfiles[id]; set {
			continue
		}
		o.issueProfiles[id] = rec.Profile
		if t := parseOptionalTime(rec.SetAt); !t.IsZero() {
			o.issueProfileSetAt[id] = t
		}
		loaded++
	}
	if loaded > 0 {
		slog.Info("orchestrator: loaded issue profile overrides", "path", path, "count", loaded)
	}
}

// saveIssueProfilesLocked hands the current overrides to the ledger writer.
// The caller holds issueProfilesMu, so submissions reach the writer in the
// order the map changed (the writer keeps the newest).
func (o *Orchestrator) saveIssueProfilesLocked() {
	path := o.issueProfilesFile
	if path == "" {
		return
	}
	env := issueProfilesEnvelope{
		Version:   issueProfilesFileVersion,
		Overrides: make(map[string]issueProfileDisk, len(o.issueProfiles)),
	}
	for id, name := range o.issueProfiles {
		rec := issueProfileDisk{Profile: name}
		if t := o.issueProfileSetAt[id]; !t.IsZero() {
			rec.SetAt = t.UTC().Format(time.RFC3339Nano)
		}
		env.Overrides[id] = rec
	}
	data, err := json.Marshal(env)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal issue profile overrides", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create issue profile overrides dir", "path", path, "error", err)
		return
	}
	o.persistLedger(ledgerIssueProfiles, path, data, 0o644)
}

// enabledProfileOrEmpty returns name when it is a configured, enabled
// profile, else "" — the default, as dispatchRun falls back. Reads
// cfg.Agent.Profiles under cfgMu.
func (o *Orchestrator) enabledProfileOrEmpty(name string) string {
	if name == "" {
		return ""
	}
	o.cfgMu.RLock()
	profile, ok := o.cfg.Agent.Profiles[name]
	o.cfgMu.RUnlock()
	if !ok || !config.ProfileEnabled(profile) {
		return ""
	}
	return name
}

// autoSwitchSupersedes reports whether the issue's automatic switch
// (State.IssueProfiles / IssueBackends written by a rate_limited
// switch_to_profile or a backend_fallback reroute) was recorded after the
// operator's override set at setAt, and so takes precedence over it until
// the switch is cleared (success, switch_revert_hours, switch_back). The
// docs promise switch_to_profile "reassigns the issue"; an operator pin
// that predates the switch used to mask it. An override set after the
// switch is the operator's newer choice and wins. An override with no
// recorded time (a test literal) predates any switch.
func autoSwitchSupersedes(state State, identifier string, setAt time.Time) bool {
	if _, switched := state.AutoSwitchedIdentifiers[identifier]; !switched {
		return false
	}
	at, ok := state.AutoSwitchedAt[identifier]
	if !ok || at.IsZero() {
		return false
	}
	return setAt.IsZero() || at.After(setAt)
}
