package main

import (
	"context"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// startLocalIssueWatch wakes the orchestrator as soon as a local issue file
// changes (#133) instead of at the next poll, until ctx ends; with
// tracker.sync it also starts a sync cycle (#118). A no-op for other
// trackers.
func startLocalIssueWatch(ctx context.Context, tr tracker.Tracker, refresh func()) {
	var lt *local.Tracker
	switch t := tr.(type) {
	case *local.Tracker:
		lt = t
	case *syncedLocal:
		lt = t.Tracker
		orchRefresh := refresh
		refresh = func() { orchRefresh(); t.syncer.Nudge() }
	case interface{ TasksTracker() *local.Tracker }: // #251: the tasks next to Linear/GitHub
		lt = t.TasksTracker()
	default:
		return
	}
	go func() {
		defer failFastOnPanic("local-issues-watch")
		lt.Watch(ctx, 0, refresh)
	}()
}
