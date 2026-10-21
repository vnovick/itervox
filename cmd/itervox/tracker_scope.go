package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker"
)

// assigneeResolveTimeout bounds resolving an assignee (#182).
const assigneeResolveTimeout = 15 * time.Second

// resolveTrackerAssignee resolves the scope's assignee (#182) once per
// generation — at startup and on every WORKFLOW.md reload, since run()
// builds a new tracker each time — before the store and the orchestrator
// read through it, so the first poll costs no extra request and a user
// nobody can see is reported at once. It runs on run()'s goroutine, never
// the event loop. A failure is logged, not fatal: every read retries the
// resolution and fails (it never widens) until it succeeds, and the failing
// poll surfaces as the tracker error on the dashboard. `itervox doctor`
// reports the same check.
func resolveTrackerAssignee(ctx context.Context, cfg *config.Config, tr tracker.Tracker) {
	spec := strings.TrimSpace(cfg.Tracker.Assignee)
	sm, ok := tr.(tracker.ScopeManager)
	if spec == "" || !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, assigneeResolveTimeout)
	defer cancel()
	u, err := sm.ResolveAssignee(cctx)
	if err != nil {
		slog.Error("tracker: the assignee did not resolve; polls fail until it does (run `itervox doctor`)",
			"assignee", spec, "error", logging.RedactString(err.Error()))
		return
	}
	slog.Info("tracker: assignee scope", "assignee", spec, "user", u.Label(), "user_id", u.ID, "team", cfg.Tracker.Team)
}

// scopeView is what the dashboard and TUI show about the tracker's scope
// (#182).
type scopeView struct {
	label        string
	assignee     string
	assigneeName string
	labels       []string
}

// trackerScopeView reads the tracker's scope for the snapshot; zero for
// trackers without a combinable scope.
func trackerScopeView(tr tracker.Tracker) scopeView {
	sm, ok := tr.(tracker.ScopeManager)
	if !ok {
		return scopeView{}
	}
	sc := sm.Scope()
	v := scopeView{label: sm.ScopeLabel(), assignee: sc.Assignee, labels: sc.Labels}
	if u, ok := sm.ResolvedAssignee(); ok {
		v.assigneeName = u.Label()
	}
	return v
}
