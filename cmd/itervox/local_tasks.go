package main

import (
	"log/slog"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// withLocalTasks joins a Linear or GitHub tracker with the project's
// Itervox-only tasks (#251): issue files with the TASK prefix in the local
// tracker directory, which New task creates and the board, dispatch and API
// then treat like tracker issues. Other trackers are returned unchanged: the
// local tracker holds tasks itself, and the memory tracker has no project.
func withLocalTasks(cfg *config.Config, tr tracker.Tracker) tracker.Tracker {
	if cfg.Tracker.Kind != "linear" && cfg.Tracker.Kind != "github" {
		return tr
	}
	tasks := local.New(local.Config{
		Dir:            config.LocalIssuesDir(cfg),
		Prefix:         local.TaskPrefix,
		ActiveStates:   cfg.Tracker.ActiveStates,
		TerminalStates: cfg.Tracker.TerminalStates,
	})
	slog.Debug("local tasks: joined with the tracker", "dir", tasks.Dir(), "prefix", local.TaskPrefix)
	return local.WithTasks(tr, tasks)
}

// trackerBehindTasks is the tracker a local-tasks join wraps, or tr: for
// what concerns the tracker alone (its sync status, GitHub comment
// commands).
func trackerBehindTasks(tr tracker.Tracker) tracker.Tracker {
	if j, ok := tr.(interface{ Main() tracker.Tracker }); ok {
		return j.Main()
	}
	return tr
}
