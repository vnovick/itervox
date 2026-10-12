package main

import (
	"context"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// startLocalIssueWatch wakes the orchestrator as soon as a local issue file
// changes (#133) instead of at the next poll, until ctx ends. A no-op for
// other trackers.
func startLocalIssueWatch(ctx context.Context, tr tracker.Tracker, refresh func()) {
	lt, ok := tr.(*local.Tracker)
	if !ok {
		return
	}
	go func() {
		defer failFastOnPanic("local-issues-watch")
		lt.Watch(ctx, 0, refresh)
	}()
}
