package main

import (
	"log/slog"
	"path/filepath"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
)

// advertiseMissingDepsSidecar logs a one-shot slog.Info line at daemon
// startup when `agent.deps_analyzer_profile` is set but no sidecar exists.
// The Deps tab still renders the tracker-only graph; the line tells the
// operator how to populate the inferred-edge layer without scrolling through
// the docs site.
//
// No-op when:
//
//	deps_analyzer_profile is empty (analyzer disabled by design)
//	the sidecar already exists (init's one-shot pass succeeded, or a
//	previous dashboard click populated it)
//
// Pure helper — no orchestrator state mutation.
func advertiseMissingDepsSidecar(cfg *config.Config, workflowPath string) {
	if cfg == nil || cfg.Agent.DepsAnalyzerProfile == "" {
		return
	}
	dir := filepath.Dir(workflowPath)
	if dir == "" {
		dir = "."
	}
	sidecarPath := depsanalysis.SidecarPath(dir)
	if depsSidecarExists(sidecarPath) {
		return
	}
	slog.Info("itervox: dependency-analyzer sidecar missing — Deps tab will show tracker-only relations until you populate it",
		"sidecar_path", sidecarPath,
		"profile", cfg.Agent.DepsAnalyzerProfile,
		"hint", `run "itervox deps analyze" or click "Analyze dependencies" on the dashboard`,
	)
}

// depsSidecarExists returns true when the file at path is readable as a
// valid sidecar (schema-aware). Missing file, parse error, and schema
// mismatch all return false — the advisory line fires in each case because
// the operator needs to re-run the pass anyway.
func depsSidecarExists(path string) bool {
	sc, err := depsanalysis.LoadSidecar(path)
	if err != nil {
		return false
	}
	return sc != nil
}
