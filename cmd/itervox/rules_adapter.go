package main

import (
	"cmp"
	"context"
	"log/slog"
	"slices"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/server"
)

// rulesAdapter implements server.RulesClient for the Rules editor (#157).
// Rules live in files the daemon reads again for every run, so the editor
// reads and writes those files directly: no cfg field, no reload, no
// event-loop state. cfg.WorkflowPath, cfg.PromptTemplate, cfg.Agent.Command
// and cfg.Tracker.ProjectSlug are read-only after startup; profiles are read
// through the orchestrator's cfgMu accessor.
type rulesAdapter struct {
	cfg  *config.Config
	orch *orchestrator.Orchestrator
}

func (a *rulesAdapter) workflowPath() string { return a.cfg.WorkflowPath }

func layerView(l rules.Layer) server.RulesLayerView {
	v := server.RulesLayerView{
		Name: l.Name, Path: l.Path, Writable: l.Path != "", Exists: l.Exists,
		Raw: string(l.Raw), Version: l.Version, Rules: l.Rules,
	}
	if v.Rules == nil {
		v.Rules = []rules.Rule{}
	}
	if l.Err != nil {
		v.Error = l.Err.Error()
	}
	return v
}

// RulesLayers implements server.RulesClient.
func (a *rulesAdapter) RulesLayers() server.RulesView {
	layers := rules.ReadLayers(a.workflowPath())
	out := server.RulesView{SchemaURL: rules.SchemaURL, Layers: make([]server.RulesLayerView, 0, len(layers))}
	for _, l := range layers {
		out.Layers = append(out.Layers, layerView(l))
	}
	return out
}

// SaveRulesLayer implements server.RulesClient.
func (a *rulesAdapter) SaveRulesLayer(layer string, req server.RulesSaveRequest) (server.RulesLayerView, error) {
	if !slices.Contains(rules.LayerNames, layer) {
		return server.RulesLayerView{}, server.ErrUnknownRulesLayer
	}
	path := rules.LayerPath(a.workflowPath(), layer)
	if path == "" {
		return server.RulesLayerView{}, server.ErrUnknownRulesLayer
	}
	var err error
	if req.Raw != nil {
		err = rules.SaveRaw(path, []byte(*req.Raw), req.BaseVersion)
	} else {
		err = rules.SaveRules(path, *req.Rules, req.BaseVersion)
	}
	if err != nil {
		return server.RulesLayerView{}, err
	}
	slog.Info("rules: layer saved from the dashboard", "layer", layer, "path", path)
	return layerView(rules.ReadLayer(layer, path)), nil
}

// PreviewRules implements server.RulesClient: the rules a run of profile on
// the issue would get, resolved the way dispatch resolves them (the issue's
// profile and backend overrides, then the profile's backend).
func (a *rulesAdapter) PreviewRules(_ context.Context, profile, identifier string) (server.RulesPreview, error) {
	backend := ""
	if identifier != "" && a.orch != nil {
		snap := a.orch.Snapshot()
		if profile == "" {
			profile = snap.IssueProfiles[identifier]
		}
		backend = snap.IssueBackends[identifier]
	}
	var prof config.AgentProfile
	if profile != "" && a.orch != nil {
		prof, _ = a.orch.AgentProfileCfg(profile)
	}
	backend = cmp.Or(backend, config.ProfileRunsBackend(prof, a.cfg.Agent.Command), "claude")
	target := rules.Target{
		Projects: rules.ProjectNames(a.workflowPath(), a.cfg.Tracker.ProjectSlug),
		Profile:  profile,
		Backend:  backend,
	}
	set := rules.Resolve(a.workflowPath(), target)
	return server.RulesPreview{
		Profile:             cmp.Or(profile, rules.DefaultProfile),
		Backend:             backend,
		Identifier:          identifier,
		Projects:            nonNil(target.Projects),
		Rules:               nonNil(set.Ordered()),
		Conflicts:           nonNil(set.Conflicts),
		Errors:              nonNil(set.Errors),
		Block:               set.Block(),
		Hash:                set.Hash(),
		IDs:                 set.IDs(),
		TemplatePlacesRules: rules.TemplatePlacesRules(a.cfg.PromptTemplate, prof.Soul, prof.Instructions, prof.Prompt),
	}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// analyzerRulesBlock is the rules block for a dependency-analyzer pass
// (#157), read from the rules files when the job runs. Conflicts and broken
// layers are logged; the analyzer itself still runs.
func analyzerRulesBlock(cfg *config.Config, profileName string, prof config.AgentProfile, logger *slog.Logger) string {
	set := rules.Resolve(cfg.WorkflowPath, rules.Target{
		Projects: rules.ProjectNames(cfg.WorkflowPath, cfg.Tracker.ProjectSlug),
		Profile:  profileName,
		Backend:  cmp.Or(config.ProfileRunsBackend(prof, cfg.Agent.Command), "claude"),
	})
	for _, c := range set.Conflicts {
		logger.Warn("deps analyzer: rules conflict", "rule", c.ID, "layer", c.Layer, "kept_layer", c.KeptLayer)
	}
	for _, e := range set.Errors {
		logger.Warn("deps analyzer: rules layer ignored", "layer", e.Layer, "path", e.Path, "error", e.Message)
	}
	if rec := set.Record(); rec.Hash != "" {
		logger.Info("deps analyzer: rules", "profile", profileName, "rules_hash", rec.Hash, "rule_ids", rec.IDs)
	}
	return set.Block()
}
