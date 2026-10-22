package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/playbook"
)

// Task-type playbooks (#218). startRun (event loop) reads the playbooks
// directory again for every run, like the rules and memory files, chooses
// the issue's playbook — State.PlaybookOverrides, else label, else keyword
// classifier — and keeps it on the run handle. The worker renders it as a
// `## Playbook: <name>` block right after the profile's INSTRUCTIONS.md
// (profilePromptTail) and folds its require_evidence into the profile's
// evidence gate (#80). The block is a prompt-digest component only when
// present. Worker runs only: reviewer and automation runs keep the prompt
// they had, so a reviewer is never handed a bug-fix recipe.

// EventSetPlaybookOverride is sent by SetPlaybookOverride (#218): Identifier
// names the issue and Message the playbook (a name, playbook.None, or "" to
// return to the automatic choice).
const EventSetPlaybookOverride EventType = "SetPlaybookOverride"

// playbookApplies reports whether a run gets a playbook: worker runs do;
// reviewer runs (or runs of a reviewer profile, which are digested with the
// reviewer prompt) and automation runs do not.
func playbookApplies(kind string, reviewerProfile, automation bool) bool {
	return kind != "reviewer" && kind != "automation" && !reviewerProfile && !automation
}

// resolvePlaybookFor chooses issue's playbook under cfg with the operator's
// override. Shared by startRun, ComposeEvalPrompt and the issues endpoint so
// all three agree. cfg.Agent.Playbooks and cfg.WorkflowPath are read-only
// after startup.
func resolvePlaybookFor(cfg *config.Config, issue domain.Issue, override string) (playbook.Choice, []playbook.FileError) {
	if cfg == nil || !cfg.Agent.Playbooks.Enabled {
		return playbook.Choice{}, nil
	}
	set, errs := playbook.Load(playbook.Dir(cfg))
	return playbook.Choose(set, cfg.Agent.Playbooks, playbookInput(issue, override)), errs
}

// playbookInput is what playbook.Choose needs to know about issue.
func playbookInput(issue domain.Issue, override string) playbook.Input {
	desc := ""
	if issue.Description != nil {
		desc = *issue.Description
	}
	return playbook.Input{Override: override, Labels: issue.Labels, Title: issue.Title, Description: desc}
}

// resolveRunPlaybook chooses the playbook of a run startRun is starting.
// Event loop only (it reads state.PlaybookOverrides and, under cfgMu, the
// reviewer profile). Unusable playbook files are logged; the run still
// starts.
func (o *Orchestrator) resolveRunPlaybook(state *State, entry *RunEntry) playbook.Choice {
	if o == nil || o.cfg == nil || entry == nil || !o.cfg.Agent.Playbooks.Enabled {
		return playbook.Choice{}
	}
	o.cfgMu.RLock()
	reviewerProfile := isReviewerProfile(o.cfg, entry.ProfileName)
	o.cfgMu.RUnlock()
	if !playbookApplies(entry.Kind, reviewerProfile, entry.AutomationID != "") {
		return playbook.Choice{}
	}
	override := ""
	if state != nil {
		override = state.PlaybookOverrides[entry.Issue.Identifier]
	}
	choice, errs := resolvePlaybookFor(o.cfg, entry.Issue, override)
	for _, e := range errs {
		slog.Warn("orchestrator: playbook file ignored", "issue_identifier", entry.Issue.Identifier,
			"path", e.Path, "error", e.Message)
	}
	return choice
}

// runPlaybook returns the playbook a worker's run was dispatched with, or,
// for a worker started without a run (tests that call runWorker directly),
// none.
func runPlaybook(ctx context.Context) playbook.Choice {
	if h := runFrom(ctx); h != nil {
		return h.playbook
	}
	return playbook.Choice{}
}

// runEvidenceChecks is the evidence a run must prove (#80): the profile's
// require_evidence plus its playbook's, normalized and de-duplicated. One
// list feeds the prompt's evidence block and the completion gate.
func runEvidenceChecks(profileChecks []string, pb playbook.Choice) []string {
	if len(pb.Playbook.RequireEvidence) == 0 {
		return profileChecks
	}
	all := append(append([]string(nil), profileChecks...), pb.Playbook.RequireEvidence...)
	return config.NormalizeEvidenceChecks(all)
}

// SetPlaybookOverride sets the operator's playbook for identifier (#218): a
// playbook name, playbook.None for none, or "" to return to the automatic
// choice. Non-blocking send of EventSetPlaybookOverride; false when the
// event channel is full (the caller may retry). The name is validated by
// the caller against the playbooks on disk. Safe from any goroutine.
func (o *Orchestrator) SetPlaybookOverride(identifier, name string) bool {
	select {
	case o.events <- OrchestratorEvent{Type: EventSetPlaybookOverride, Identifier: identifier, Message: strings.TrimSpace(name)}:
		slog.Info("orchestrator: playbook override queued", "identifier", identifier, "playbook", name)
		return true
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: playbook override event channel full", "identifier", identifier)
		return false
	}
}

// applyPlaybookOverrideEvent handles EventSetPlaybookOverride: mutates
// state.PlaybookOverrides and persists it. The next run of the issue picks
// it up; a running one keeps the playbook it started with. Event loop only.
func (o *Orchestrator) applyPlaybookOverrideEvent(state *State, ev OrchestratorEvent) {
	if ev.Identifier == "" {
		return
	}
	if state.PlaybookOverrides == nil {
		state.PlaybookOverrides = make(map[string]string)
	}
	if ev.Message == "" {
		delete(state.PlaybookOverrides, ev.Identifier)
	} else {
		state.PlaybookOverrides[ev.Identifier] = ev.Message
	}
	o.savePlaybookOverridesToDisk(maps.Clone(state.PlaybookOverrides))
	slog.Info("orchestrator: playbook override applied", "identifier", ev.Identifier, "playbook", ev.Message)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// SetPlaybookOverridesFile sets the path State.PlaybookOverrides persists
// to across restarts (.itervox/playbook_overrides.json). Before Run.
func (o *Orchestrator) SetPlaybookOverridesFile(path string) {
	o.depsOverridesMu.Lock()
	o.playbookOverridesFile = path
	o.depsOverridesMu.Unlock()
}

// loadPlaybookOverridesFromDisk pre-populates state.PlaybookOverrides at
// startup. A missing file is not an error; a corrupt one is logged and
// ignored so it never blocks startup.
func (o *Orchestrator) loadPlaybookOverridesFromDisk(state State) State {
	o.depsOverridesMu.RLock()
	path := o.playbookOverridesFile
	o.depsOverridesMu.RUnlock()
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load playbook overrides", "path", path, "error", err)
		}
		return state
	}
	var disk map[string]string
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Warn("orchestrator: failed to parse playbook overrides", "path", path, "error", err)
		return state
	}
	if state.PlaybookOverrides == nil {
		state.PlaybookOverrides = make(map[string]string, len(disk))
	}
	for id, name := range disk {
		if id != "" && (name == playbook.None || playbook.ValidName(name)) {
			state.PlaybookOverrides[id] = name
		}
	}
	return state
}

// savePlaybookOverridesToDisk hands a clone of the overrides to the ledger
// writer. Never reads live state.
func (o *Orchestrator) savePlaybookOverridesToDisk(overrides map[string]string) {
	o.depsOverridesMu.RLock()
	path := o.playbookOverridesFile
	o.depsOverridesMu.RUnlock()
	if path == "" {
		return
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal playbook overrides", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("orchestrator: failed to create playbook overrides dir", "path", path, "error", err)
		return
	}
	o.persistLedger(ledgerPlaybookOverrides, path, data, 0o644)
}

// Playbooks lists the playbooks on disk, for the dashboard's picker. Reads
// files only; any goroutine.
func (o *Orchestrator) Playbooks() []playbook.Playbook {
	if o == nil || o.cfg == nil || !o.cfg.Agent.Playbooks.Enabled {
		return nil
	}
	set, _ := playbook.Load(playbook.Dir(o.cfg))
	return set.Playbooks
}

// PlaybookChooser returns a function that chooses an issue's playbook the
// way dispatch would (with the operator's override), reading the playbooks
// directory once — for the issues endpoint, which labels every card.
func (o *Orchestrator) PlaybookChooser() func(issue domain.Issue, override string) playbook.Choice {
	if o == nil || o.cfg == nil || !o.cfg.Agent.Playbooks.Enabled {
		return func(domain.Issue, string) playbook.Choice { return playbook.Choice{} }
	}
	set, _ := playbook.Load(playbook.Dir(o.cfg))
	pc := o.cfg.Agent.Playbooks
	return func(issue domain.Issue, override string) playbook.Choice {
		return playbook.Choose(set, pc, playbookInput(issue, override))
	}
}

// playbookSource is how c was chosen, "" when the run has no playbook.
func playbookSource(c playbook.Choice) string {
	if c.Name() == "" {
		return ""
	}
	return c.Source
}

// PlaybooksEnabled reports agent.playbooks.enabled (read-only after
// startup).
func (o *Orchestrator) PlaybooksEnabled() bool {
	return o != nil && o.cfg != nil && o.cfg.Agent.Playbooks.Enabled
}
