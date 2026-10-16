package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker"
)

// resolveTrackerAssignee resolves an assignee scope's user (#182) once per
// generation — at startup and on every WORKFLOW.md reload, since run()
// builds a new tracker each time — before the store and the orchestrator
// read through it, so the first poll costs no extra request and a user
// nobody can see is reported at once. It runs on run()'s goroutine, never
// the event loop. A failure is logged, not fatal: every read retries the
// resolution and fails (it never widens to every issue) until it succeeds,
// and the failing poll surfaces as the tracker error on the dashboard.
// `itervox doctor` reports the same check.
func resolveTrackerAssignee(ctx context.Context, cfg *config.Config, tr tracker.Tracker) {
	spec := strings.TrimSpace(cfg.Tracker.Assignee)
	if spec == "" {
		return
	}
	as, ok := tr.(tracker.AssigneeScoper)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, assigneeResolveTimeout)
	defer cancel()
	u, err := as.ResolveAssignee(cctx)
	if err != nil {
		slog.Error("tracker: the assignee scope did not resolve; polls fail until it does (run `itervox doctor`)",
			"assignee", spec, "error", logging.RedactString(err.Error()))
		return
	}
	slog.Info("tracker: assignee scope", "assignee", spec, "user", u.Label(), "user_id", u.ID, "team", cfg.Tracker.Team)
}

// trackerScopeView is what the dashboard and TUI show about the tracker's
// scope (#182): the active scope's label and the picker's "assigned to"
// option label ("" when the tracker has no assignee scope).
func trackerScopeView(tr tracker.Tracker) (scope, assigneeOption string) {
	as, ok := tr.(tracker.AssigneeScoper)
	if !ok {
		return "", ""
	}
	return as.ScopeLabel(), assigneeOptionLabel(as)
}

// assigneeOptionLabel names who the picker's "assigned to" option means.
func assigneeOptionLabel(as tracker.AssigneeScoper) string {
	if u, ok := as.ResolvedAssignee(); ok {
		return u.Label()
	}
	return as.AssigneeSpec()
}
