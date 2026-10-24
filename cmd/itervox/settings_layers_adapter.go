package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// settings_layers_adapter.go backs the schema-driven Settings page (#158):
// orchestratorAdapter implements server.SettingsLayersClient. A save names
// its layer (user, project or local), writes that JSONC file through the one
// generic comment-preserving writer (config.SetSettings, or the JSON view's
// text checked by config.ParseSettings), and then applies each changed
// setting the registry marks Live through its existing cfgMu setter, so the
// running daemon picks it up without a reload. Other changed settings are
// reported as needing a restart.

// SettingsLayers implements server.SettingsLayersClient.
func (a *orchestratorAdapter) SettingsLayers() (server.SettingsLayersView, error) {
	view := server.SettingsLayersView{SchemaURL: config.SettingsSchemaURL}
	for _, name := range config.SettingsLayerNames {
		view.Layers = append(view.Layers, readSettingsLayerView(a.workflowPath, name))
	}
	effective, err := config.Explain(a.workflowPath)
	if err != nil {
		return view, fmt.Errorf("settings: %w", err)
	}
	view.Effective = []server.SettingsEffective{}
	for _, s := range effective {
		if _, ok := config.SettingFieldFor(s.Key); ok {
			view.Effective = append(view.Effective, server.SettingsEffective{Key: s.Key, Value: s.Value, Layer: s.Layer, Path: s.Path})
		}
	}
	return view, nil
}

func readSettingsLayerView(workflowPath, name string) server.SettingsLayerView {
	v := server.SettingsLayerView{Name: name, Values: map[string]any{}, Unknown: []string{}}
	v.Path = config.SettingsLayerPath(workflowPath, name)
	if v.Path == "" {
		return v
	}
	v.Writable = true
	raw, err := os.ReadFile(v.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return v
	case err != nil:
		v.Error = err.Error()
		return v
	}
	v.Exists, v.Raw, v.Version = true, string(raw), settingsVersion(raw)
	data, err := config.ParseSettings(raw)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Values, v.Unknown = flattenSettings(data)
	for key, value := range v.Values {
		// #239: a secret is a $VAR reference in a settings file; one
		// written inline by hand is not shown on the form.
		if s, ok := value.(string); ok && s != "" && config.IsSecretKey(key) && !config.IsEnvReference(s) {
			v.Values[key] = "(set, hidden)"
		}
	}
	return v
}

// settingsVersion identifies a layer file's bytes for the stale-save check;
// "" for a file that does not exist.
func settingsVersion(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// flattenSettings maps each registered key a layer sets to its value, and
// lists the leaf keys it sets that are not registered.
func flattenSettings(data map[string]any) (map[string]any, []string) {
	values := map[string]any{}
	for _, f := range config.SettingFields {
		if v, ok := lookupSetting(data, f.Key); ok {
			values[f.Key] = v
		}
	}
	var unknown []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if _, ok := config.SettingFieldFor(key); ok {
				continue
			}
			if sub, isMap := v.(map[string]any); isMap && slices.ContainsFunc(config.SettingFields, func(f config.SettingField) bool {
				return strings.HasPrefix(f.Key, key+".")
			}) {
				walk(key, sub)
				continue
			}
			unknown = append(unknown, key)
		}
	}
	walk("", data)
	slices.Sort(unknown)
	if unknown == nil {
		unknown = []string{}
	}
	return values, unknown
}

func lookupSetting(data map[string]any, key string) (any, bool) {
	var cur any = data
	for _, p := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// SaveSettingsLayer implements server.SettingsLayersClient.
func (a *orchestratorAdapter) SaveSettingsLayer(layer string, req server.SettingsLayerSaveRequest) (server.SettingsLayerSaveResult, error) {
	var res server.SettingsLayerSaveResult
	path := config.SettingsLayerPath(a.workflowPath, layer)
	if path == "" {
		return res, fmt.Errorf("settings: layer %q: %w", layer, server.ErrUnknownSettingsLayer)
	}
	unlock, err := a.beginSettingsSave()
	if err != nil {
		return res, err
	}
	defer unlock()

	before := readSettingsLayerView(a.workflowPath, layer)
	if req.BaseVersion != before.Version {
		return res, fmt.Errorf("settings: %s: %w", path, server.ErrSettingsStale)
	}
	write, touched, set, err := settingsLayerWrite(req, before)
	if err != nil {
		return res, fmt.Errorf("%w: %w", server.ErrSettingsInvalid, err)
	}
	if len(touched) == 0 {
		return a.settingsSaveResult(layer, nil, nil, nil)
	}
	oldEffective := explainedValues(a.workflowPath)
	check := func() error {
		cfg, err := config.Load(a.workflowPath)
		if err != nil {
			return fmt.Errorf("%w: WORKFLOW.md does not load with this change, so it was not made: %w", server.ErrSettingsInvalid, err)
		}
		if err := validateLiveSettings(cfg, a.orch.ProfilesCfg(), touched); err != nil {
			return fmt.Errorf("%w: %w", server.ErrSettingsInvalid, err)
		}
		return nil
	}
	// A project or local save also moves the keys it sets out of WORKFLOW.md
	// (a copy left there would be reported as a conflict: every settings
	// file wins over it, #239); a user save never edits the project.
	var remove []string
	if layer != config.LayerUser {
		remove = set
	}
	if err := persistLayer(a.workflowPath, path, write, check, remove); err != nil {
		return res, err
	}
	if layer == config.LayerLocal {
		if err := ensureItervoxGitignore(filepath.Dir(path)); err != nil {
			slog.Warn("settings: could not make sure the local settings file is gitignored", "path", path, "error", err)
		}
	}
	cfg, err := config.Load(a.workflowPath)
	if err != nil {
		return res, fmt.Errorf("settings: %s was saved but the config no longer loads: %w", path, err)
	}
	newEffective := explainedValues(a.workflowPath)
	var applied, restart []string
	for _, key := range touched {
		if sameSettingValue(oldEffective[key], newEffective[key]) {
			continue
		}
		f, _ := config.SettingFieldFor(key)
		if !f.Live {
			restart = append(restart, key)
			continue
		}
		applied = append(applied, key)
	}
	if err := a.applyLiveSettings(cfg, applied); err != nil {
		return res, fmt.Errorf("settings: %s was saved but applying it failed (it applies at the next reload): %w", path, err)
	}
	slog.Info("settings: layer saved from the dashboard", "layer", layer, "path", path, "applied", applied, "restart_required", restart)
	if a.notify != nil {
		a.notify()
	}
	return a.settingsSaveResult(layer, set, applied, restart)
}

// settingsLayerWrite turns a save request into the write persistLayer runs,
// the keys it touches and the keys it sets (non-nil). Values are checked
// against the registry before anything is written.
func settingsLayerWrite(req server.SettingsLayerSaveRequest, before server.SettingsLayerView) (func(string) error, []string, []string, error) {
	if req.Raw != nil {
		raw := []byte(*req.Raw)
		data, err := config.ParseSettings(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		values, _ := flattenSettings(data)
		for key, v := range values {
			if err := config.ValidateSettingValue(key, v); err != nil {
				return nil, nil, nil, err
			}
		}
		keys := map[string]bool{}
		for k := range values {
			keys[k] = true
		}
		for k := range before.Values {
			keys[k] = true
		}
		touched := slices.Sorted(maps.Keys(keys))
		set := slices.Sorted(maps.Keys(values))
		write := func(path string) error {
			return config.WriteJSONCFile(path, raw, func(b []byte) error { _, err := config.ParseSettings(b); return err })
		}
		return write, touched, set, nil
	}
	touched := slices.Sorted(maps.Keys(req.Values))
	assignments := make([]config.SettingAssignment, 0, len(touched))
	var set []string
	for _, key := range touched {
		v, err := normalizeSettingValue(req.Values[key])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", key, err)
		}
		if err := config.ValidateSettingValue(key, v); err != nil {
			return nil, nil, nil, err
		}
		if f, _ := config.SettingFieldFor(key); f.Editor != "" {
			return nil, nil, nil, fmt.Errorf("%s is edited on the %s page", key, f.Editor)
		}
		assignments = append(assignments, config.SettingAssignment{Key: key, Value: v})
		if v != nil {
			set = append(set, key)
		}
	}
	write := func(path string) error { return config.SetSettings(path, assignments...) }
	return write, touched, set, nil
}

// normalizeSettingValue gives a value decoded from a JSON request body the
// shapes the settings files decode to (whole numbers as int).
func normalizeSettingValue(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return config.ParseJSONC(b)
}

func explainedValues(workflowPath string) map[string]any {
	out := map[string]any{}
	settings, err := config.Explain(workflowPath)
	if err != nil {
		return out
	}
	for _, s := range settings {
		out[s.Key] = s.Value
	}
	return out
}

func sameSettingValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// validateLiveSettings refuses a save the running daemon's setters would
// reject, before WORKFLOW.md is touched.
func validateLiveSettings(cfg *config.Config, profiles map[string]config.AgentProfile, touched []string) error {
	if slices.Contains(touched, "agent.reviewer_profile") || slices.Contains(touched, "agent.auto_review") {
		if err := config.ValidateReviewerAutoReview(cfg.Agent.ReviewerProfile, cfg.Agent.AutoReview); err != nil {
			return err
		}
		if err := config.ValidateReviewerProfile(profiles, cfg.Agent.ReviewerProfile); err != nil {
			return err
		}
	}
	if slices.Contains(touched, "agent.deps_analyzer_profile") {
		if err := config.ValidateDepsAnalyzerProfile(profiles, cfg.Agent.DepsAnalyzerProfile); err != nil {
			return err
		}
	}
	if state := strings.TrimSpace(cfg.Tracker.FailedState); state != "" && slices.Contains(touched, "tracker.failed_state") {
		// As SetFailedState refuses it: the state must be one Itervox knows.
		known := slices.Concat(cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.BacklogStates)
		if !slices.Contains(known, state) {
			return fmt.Errorf("failed_state %q is not in tracker.active_states / terminal_states / backlog_states", state)
		}
	}
	return nil
}

// liveSettingAppliers applies one Live setting from a freshly loaded config to
// the running orchestrator through its cfgMu setter. Every Live registry
// entry has one (TestLiveSettingsHaveAppliers).
var liveSettingAppliers = map[string]func(a *orchestratorAdapter, c *config.Config) error{
	"agent.max_concurrent_agents": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetMaxWorkers(c.Agent.MaxConcurrentAgents)
		return nil
	},
	"agent.max_retries": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetMaxRetriesCfg(c.Agent.MaxRetries)
		return nil
	},
	"agent.max_switches_per_issue_per_window": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetMaxSwitchesPerIssuePerWindowCfg(c.Agent.MaxSwitchesPerIssuePerWindow)
		return nil
	},
	"agent.switch_window_hours": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetSwitchWindowHoursCfg(c.Agent.SwitchWindowHours)
		return nil
	},
	"agent.inline_input": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetInlineInputCfg(c.Agent.InlineInput)
		return nil
	},
	"agent.dispatch_strategy": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetDispatchStrategyCfg(c.Agent.DispatchStrategy)
		return nil
	},
	"agent.reviewer_profile": applyReviewer,
	"agent.auto_review":      applyReviewer,
	"agent.deps_analyzer_profile": func(a *orchestratorAdapter, c *config.Config) error {
		return a.orch.SetDepsAnalyzerProfileCfg(c.Agent.DepsAnalyzerProfile)
	},
	"agent.ssh_hosts":             applySSHHosts,
	"agent.ssh_host_descriptions": applySSHHosts,
	"agent.available_models": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetAvailableModelsCfg(c.Agent.AvailableModels)
		return nil
	},
	"workspace.auto_clear": func(a *orchestratorAdapter, c *config.Config) error {
		return a.orch.SetAutoClearWorkspaceCfg(c.Workspace.AutoClearWorkspace)
	},
	// #239: the tracker state lists and dependencies.analysis_mode are
	// layered now; they apply through the same cfgMu setters the dedicated
	// endpoints use.
	"tracker.active_states":    applyTrackerStates,
	"tracker.terminal_states":  applyTrackerStates,
	"tracker.completion_state": applyTrackerStates,
	"tracker.failed_state": func(a *orchestratorAdapter, c *config.Config) error {
		a.orch.SetFailedStateCfg(c.Tracker.FailedState)
		return nil
	},
	"dependencies.analysis_mode": func(a *orchestratorAdapter, c *config.Config) error {
		return a.orch.SetDepsAnalysisModeCfg(c.Dependencies.AnalysisMode)
	},
}

// applyTrackerStates applies the three state settings together, to the
// orchestrator and to the tracker client's own copies (CORE-160), as
// UpdateTrackerStates does.
func applyTrackerStates(a *orchestratorAdapter, c *config.Config) error {
	a.orch.SetTrackerStatesCfg(c.Tracker.ActiveStates, c.Tracker.TerminalStates, c.Tracker.CompletionState)
	if setter, ok := a.tr.(tracker.StateListSetter); ok {
		setter.SetStateLists(c.Tracker.ActiveStates, c.Tracker.TerminalStates)
	}
	return nil
}

func applyReviewer(a *orchestratorAdapter, c *config.Config) error {
	return a.orch.SetReviewerCfg(c.Agent.ReviewerProfile, c.Agent.AutoReview)
}

// applySSHHosts makes the running SSH pool match c: hosts no longer listed
// are removed, and every listed host is added with its description.
func applySSHHosts(a *orchestratorAdapter, c *config.Config) error {
	current, _ := a.orch.SSHHostsCfg()
	for _, h := range current {
		if !slices.Contains(c.Agent.SSHHosts, h) {
			a.orch.RemoveSSHHostCfg(h)
		}
	}
	for _, h := range c.Agent.SSHHosts {
		a.orch.AddSSHHostCfg(h, c.Agent.SSHHostDescriptions[h])
	}
	return nil
}

// sharedApplier names, for a key whose applier also applies another key, that
// other key: the reviewer pair and the SSH pair are each applied once.
var sharedApplier = map[string]string{
	"agent.auto_review":           "agent.reviewer_profile",
	"agent.ssh_host_descriptions": "agent.ssh_hosts",
	"tracker.terminal_states":     "tracker.active_states",
	"tracker.completion_state":    "tracker.active_states",
}

// applyLiveSettings runs each key's applier once.
func (a *orchestratorAdapter) applyLiveSettings(c *config.Config, keys []string) error {
	done := map[string]bool{}
	for _, key := range keys {
		apply, ok := liveSettingAppliers[key]
		if !ok {
			continue
		}
		id := key
		if shared, ok := sharedApplier[key]; ok {
			id = shared
		}
		if done[id] {
			continue
		}
		done[id] = true
		if err := apply(a, c); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

func (a *orchestratorAdapter) settingsSaveResult(layer string, set, applied, restart []string) (server.SettingsLayerSaveResult, error) {
	view, err := a.SettingsLayers()
	if err != nil {
		return server.SettingsLayerSaveResult{}, err
	}
	res := server.SettingsLayerSaveResult{
		SettingsLayersView: view, Layer: layer,
		Applied: nonNil(applied), RestartRequired: nonNil(restart), Shadowed: map[string]string{},
	}
	rank := config.SettingsLayerRank(layer)
	for _, e := range view.Effective {
		if slices.Contains(set, e.Key) && config.SettingsLayerRank(e.Layer) > rank {
			res.Shadowed[e.Key] = e.Layer
		}
	}
	return res, nil
}
