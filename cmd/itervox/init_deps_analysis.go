package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
)

// initOneShotAnalysisTimeout caps the synchronous analysis pass run inside
// `itervox init`. Longer than the typical agent turn but short enough that
// the operator does not feel the binary has hung.
const initOneShotAnalysisTimeout = 5 * time.Minute

// initDepsAnalysisRunner is the runner-builder seam used by
// `runInitDepsAnalysis`. Tests swap this to inject a fake runner that emits
// canned JSON, avoiding a real claude/codex subprocess invocation. The
// production default constructs the same MultiRunner the daemon uses.
var initDepsAnalysisRunner = func() agent.Runner {
	return agent.NewMultiRunner(
		agent.NewClaudeRunner(),
		map[string]agent.Runner{"codex": agent.NewCodexRunner()},
	)
}

// runInitDepsAnalysis attempts one analyzer pass synchronously inside
// `itervox init`. On any failure (missing credentials, tracker down, agent
// timeout, JSON parse error) it returns an error so the caller can warn and
// continue — exiting init successfully regardless. On success it persists
// `.itervox/dependencies.json` for the dashboard to surface immediately on
// first daemon launch.
func runInitDepsAnalysis(workflowPath string) (issueCount, edgeCount int, sidecarPath string, err error) {
	cfg, err := config.Load(workflowPath)
	if err != nil {
		return 0, 0, "", fmt.Errorf("load workflow: %w", err)
	}
	profileName := strings.TrimSpace(cfg.Agent.DepsAnalyzerProfile)
	if profileName == "" {
		return 0, 0, "", errors.New("agent.deps_analyzer_profile is unset")
	}
	profile, ok := cfg.Agent.Profiles[profileName]
	if !ok {
		return 0, 0, "", fmt.Errorf("profile %q not found in agent.profiles", profileName)
	}
	if !config.ProfileEnabled(profile) {
		return 0, 0, "", fmt.Errorf("profile %q is disabled", profileName)
	}
	if cfg.Tracker.Kind != "memory" && strings.TrimSpace(cfg.Tracker.APIKey) == "" {
		return 0, 0, "", errors.New("tracker.api_key is empty — fill in .itervox/.env then run \"Analyze dependencies\" from the dashboard")
	}

	tr, err := buildTracker(cfg)
	if err != nil {
		return 0, 0, "", fmt.Errorf("build tracker: %w", err)
	}
	runner := initDepsAnalysisRunner()

	ctx, cancel := context.WithTimeout(context.Background(), initOneShotAnalysisTimeout)
	defer cancel()

	states := depsanalysis.DedupeStateNames(
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
		cfg.Tracker.BacklogStates,
	)
	issues, trackerEdges, err := depsanalysis.FetchIssues(ctx, tr, states)
	if err != nil {
		return 0, 0, "", fmt.Errorf("fetch issues: %w", err)
	}
	if len(issues) == 0 {
		// Still write a sidecar so DepsLastAnalyzedAt populates correctly.
		path := depsanalysis.SidecarPath(filepath.Dir(workflowPath))
		sc := &depsanalysis.Sidecar{
			Version:     depsanalysis.SidecarSchemaVersion,
			GeneratedAt: time.Now().UTC(),
			Profile:     profileName,
		}
		if err := depsanalysis.SaveSidecar(path, sc); err != nil {
			return 0, 0, "", fmt.Errorf("save sidecar: %w", err)
		}
		return 0, 0, path, nil
	}
	edges, err := depsanalysis.RunAgentPass(ctx, depsanalysis.AgentPassInput{
		Runner:        runner,
		Profile:       profile,
		ProfileName:   profileName,
		Issues:        issues,
		TrackerEdges:  trackerEdges,
		WorkspacePath: cfg.Workspace.Root,
		LogDir:        "",
		Logger:        slog.Default(),
		ReadTimeoutMs: cfg.Agent.ReadTimeoutMs,
		TurnTimeoutMs: cfg.Agent.TurnTimeoutMs,
	})
	if err != nil {
		return 0, 0, "", fmt.Errorf("agent pass: %w", err)
	}
	sc := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Now().UTC(),
		Profile:     profileName,
		Edges:       edges,
	}
	path := depsanalysis.SidecarPath(filepath.Dir(workflowPath))
	if err := depsanalysis.SaveSidecar(path, sc); err != nil {
		return 0, 0, "", fmt.Errorf("save sidecar: %w", err)
	}
	return len(issues), len(edges), path, nil
}

// initEnvWasJustWritten returns true when the .env file at envPath looks
// like a freshly-scaffolded stub (contains placeholder hex `x` chars). Used
// by the init flow to suppress the analysis attempt when credentials clearly
// haven't been filled in yet — the alternative would print a warning every
// time, which is noise.
func initEnvLooksLikePlaceholder(envPath string) bool {
	data, err := os.ReadFile(envPath)
	if err != nil {
		return true // missing env counts as "no credentials"
	}
	return strings.Contains(string(data), "xxxxxxxxxxxxxxxxxxxxxxxxxxxx")
}
