package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// defaultLogsDir returns a per-project logs directory under ~/.itervox/logs/
// derived from the tracker kind and project slug in the WORKFLOW.md at path.
// Falls back to ~/.itervox/logs if the config can't be read or has no slug.
func defaultLogsDir(workflowPath string) string {
	base := filepath.Join("~", ".itervox", "logs")
	if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".itervox", "logs")
	}
	cfg, err := config.Load(workflowPath)
	if err != nil || cfg.Tracker.Kind == "" || cfg.Tracker.ScopeName() == "" {
		// No slug to namespace by — fall back to a directory derived from the
		// workflow's own path rather than the shared base.
		//
		// The shared base was a cross-project state collision: this directory
		// holds automation_queue.json, which carries the DEPENDENCY AUDIT, so
		// every project without a project_slug read and wrote one another's
		// audit rows. Observed live — a Linear project inherited ten
		// `demo-id-*` rows written by an unrelated `kind: memory` run, then
		// asked Linear about issues that had never existed there on every
		// refresh cycle.
		return filepath.Join(base, "workflow", config.WorkspaceProjectKey(workflowPath))
	}
	// Encode the slug so it is safe as a directory name component, and append
	// the workflow key.
	//
	// The slug ALONE is not a project identity: two checkouts of one repo (a
	// git worktree dev setup), or several repos driven by one Linear project,
	// share a slug and would share this directory — which holds
	// automation_queue.json and therefore the dependency audit, plus
	// history.json, paused.json and input_required.json. The PID guard does
	// not stop it: it keys on the workflow's directory, so both checkouts pass.
	//
	// The slug stays in the path so the directory remains recognisable to a
	// human browsing ~/.itervox/logs.
	// #182: an assignee scope is named "assignee-<who>[-<team>]".
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(cfg.Tracker.ScopeName())
	return filepath.Join(base, cfg.Tracker.Kind, safe+"-"+config.WorkspaceProjectKey(workflowPath))
}
