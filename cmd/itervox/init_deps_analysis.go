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
// `itervox init` (and the `itervox deps analyze` CLI). On any failure
// (missing credentials, tracker down, agent timeout, JSON parse error) it
// returns an error so the caller can warn and continue — exiting init
// successfully regardless. On success it persists `.itervox/dependencies.json`
// for the dashboard to surface immediately on first daemon launch.
//
// requestedMode is the incremental-pass mode ("auto" | "full" |
// "incremental"); empty behaves like "auto" — see
// depsanalysis.PlanIncremental for resolution rules.
//
// emptyFetchGuarded reports whether the mandatory empty-fetch guard fired
// (docs/superpowers/specs/2026-08-04-analyzer-autonomy-design.md
// "Empty-fetch guard"): the fetch returned zero issues while the prior
// sidecar still carried inferred edges, so nothing was written. When true,
// edgeCount reports the edge count of the UNCHANGED on-disk sidecar (not
// "0 new edges") so the caller's notice can name what was protected.
//
// analyzedCount is the #52 IssuesScanned-honesty count: the number of issues
// actually sent to the analyzer agent (plan.ToAnalyze's length), which under
// incremental mode can be far smaller than issueCount (the raw fetch count)
// — a revalidation-only run reports issueCount > 0 with analyzedCount == 0.
func runInitDepsAnalysis(workflowPath, requestedMode string) (issueCount, analyzedCount, edgeCount int, sidecarPath string, emptyFetchGuarded bool, err error) {
	cfg, err := config.Load(workflowPath)
	if err != nil {
		return 0, 0, 0, "", false, fmt.Errorf("load workflow: %w", err)
	}
	profileName := strings.TrimSpace(cfg.Agent.DepsAnalyzerProfile)
	if profileName == "" {
		return 0, 0, 0, "", false, errors.New("agent.deps_analyzer_profile is unset")
	}
	profile, ok := cfg.Agent.Profiles[profileName]
	if !ok {
		return 0, 0, 0, "", false, fmt.Errorf("profile %q not found in agent.profiles", profileName)
	}
	if !config.ProfileEnabled(profile) {
		return 0, 0, 0, "", false, fmt.Errorf("profile %q is disabled", profileName)
	}
	if cfg.Tracker.Kind != "memory" && strings.TrimSpace(cfg.Tracker.APIKey) == "" {
		return 0, 0, 0, "", false, errors.New("tracker.api_key is empty — fill in .itervox/.env then run \"Analyze dependencies\" from the dashboard")
	}

	tr, err := buildTracker(cfg)
	if err != nil {
		return 0, 0, 0, "", false, fmt.Errorf("build tracker: %w", err)
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
		return 0, 0, 0, "", false, fmt.Errorf("fetch issues: %w", err)
	}

	path := depsanalysis.SidecarPath(filepath.Dir(workflowPath))
	prev, err := depsanalysis.LoadSidecar(path)
	if err != nil {
		return 0, 0, 0, "", false, fmt.Errorf("load sidecar: %w", err)
	}

	// Empty-fetch guard (mandatory; see docs/superpowers/specs/2026-08-04-
	// analyzer-autonomy-design.md "Empty-fetch guard"). Deliberately changes
	// the prior behavior of this branch, which used to write an empty
	// sidecar unconditionally "so DepsLastAnalyzedAt populates correctly" —
	// that was silent scheduled data destruction on any transient tracker
	// failure once auto-analysis (Task 4) starts calling this same pass
	// unattended. When there IS a prior sidecar with edges to protect, skip
	// writing entirely. When there is nothing to protect (no prior sidecar,
	// or one with zero edges — e.g. first run against a genuinely empty
	// backlog), fall through to the normal plan/merge pipeline below, which
	// still writes a (empty) sidecar so DepsLastAnalyzedAt populates —
	// nothing is lost by doing so.
	if len(issues) == 0 && prev != nil && len(prev.Edges) > 0 {
		slog.Default().Warn(fmt.Sprintf(
			"deps analyzer empty-fetch guard: refusing to overwrite %d inferred edges with an empty fetch",
			len(prev.Edges)),
			"profile", profileName)
		return 0, 0, len(prev.Edges), path, true, nil
	}

	plan := depsanalysis.PlanIncremental(issues, prev, profileName, requestedMode)
	slog.Default().Info("deps analyzer plan",
		"profile", profileName, "requested_mode", requestedMode, "resolved_mode", plan.Mode,
		"to_analyze", len(plan.ToAnalyze), "unchanged", len(plan.Unchanged))

	// The chunk/scope/fail-atomically/dedupe loop lives in
	// depsanalysis.RunChunkedAgentPass (see internal/depsanalysis/chunked_pass.go)
	// — shared with deps_analyzer_service.go's depsAnalyzerService.run (the
	// daemon's async /api/v1/deps/analyze path) since #47. This one-shot
	// `itervox init`/`itervox deps analyze` path previously carried its own
	// copy of the loop, which is what let it miss chunking (and the chunked
	// run's observability) originally. Do not re-inline a copy here; extend
	// the shared helper instead.
	chunkSize := cfg.Agent.DepsAnalyzerChunkSize
	if chunkSize <= 0 {
		chunkSize = config.DefaultDepsAnalyzerChunkSize
	}
	all, err := depsanalysis.RunChunkedAgentPass(ctx, depsanalysis.AgentPassInput{
		Runner:        runner,
		Profile:       profile,
		ProfileName:   profileName,
		WorkspacePath: cfg.Workspace.Root,
		LogDir:        "",
		Logger:        slog.Default(),
		ReadTimeoutMs: cfg.Agent.ReadTimeoutMs,
		TurnTimeoutMs: cfg.Agent.TurnTimeoutMs,
	}, plan.ToAnalyze, trackerEdges, chunkSize, nil)
	if err != nil {
		return 0, 0, 0, "", false, fmt.Errorf("agent pass: %w", err)
	}
	sc := depsanalysis.MergeIncremental(prev, plan, all, issues, profileName, time.Now().UTC())
	if err := depsanalysis.SaveSidecar(path, sc); err != nil {
		return 0, 0, 0, "", false, fmt.Errorf("save sidecar: %w", err)
	}
	return len(issues), len(plan.ToAnalyze), len(sc.Edges), path, false, nil
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
