package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// #182 — the combined tracker scope (projects AND assignee AND labels): the
// dashboard's scope filter and the TUI picker save it here.

// trackerScopeManager adapts the tracker's scope to server.ScopeManager:
// the #182 combined scope (Linear) and the #198 tracker filter (Linear and
// GitHub).
type trackerScopeManager struct {
	sm           tracker.ScopeManager // nil: no legacy scope (GitHub)
	fm           tracker.FilterManager
	workflowPath string
	// settingsGen fences saves from a superseded generation (CORE-160).
	settingsGen uint64
}

// GetScope implements server.ScopeManager.
func (m *trackerScopeManager) GetScope() server.TrackerScope {
	var out server.TrackerScope
	if m.sm != nil {
		sc := m.sm.Scope()
		out = server.TrackerScope{Projects: nonNil(sc.Projects), Assignee: sc.Assignee, Labels: nonNil(sc.Labels)}
	} else {
		out = server.TrackerScope{Projects: []string{}, Labels: []string{}}
	}
	out.Filter = m.fm.Filter()
	if out.Filter == nil {
		out.Filter = issuefilter.Filter{}
	}
	out.FilterMode = m.fm.FilterMode()
	out.Conditions = m.fm.FilterConditions()
	return out
}

// SetScope implements server.ScopeManager.
func (m *trackerScopeManager) SetScope(ctx context.Context, sc server.TrackerScope) error {
	if m.sm == nil {
		return fmt.Errorf("%w: this tracker's scope is a filter; send \"filter\"", server.ErrFilterInvalid)
	}
	return saveTrackerScope(ctx, m.sm, m.workflowPath, m.settingsGen,
		tracker.Scope{Projects: sc.Projects, Assignee: sc.Assignee, Labels: sc.Labels}, "dashboard")
}

// SetFilter implements server.ScopeManager.
func (m *trackerScopeManager) SetFilter(ctx context.Context, f issuefilter.Filter) error {
	return saveTrackerFilter(ctx, m.fm, m.workflowPath, m.settingsGen, f, m.sm != nil, "dashboard")
}

// filterPreviewLimit is how many identifiers a preview lists.
const filterPreviewLimit = 10

// PreviewFilter implements server.ScopeManager: validated and resolved like
// a save, then counted by the tracker; nothing changes.
func (m *trackerScopeManager) PreviewFilter(ctx context.Context, f issuefilter.Filter) (server.FilterPreview, error) {
	f = f.Normalize()
	if err := checkFilter(m.fm, f); err != nil {
		return server.FilterPreview{}, err
	}
	if err := resolveFilterAssignees(ctx, m.fm, f); err != nil {
		return server.FilterPreview{}, err
	}
	p, err := m.fm.PreviewFilter(ctx, f, filterPreviewLimit)
	if err != nil {
		return server.FilterPreview{}, err
	}
	return server.FilterPreview{Count: p.Count, Identifiers: p.Identifiers}, nil
}

// FetchLabels implements server.ScopeManager.
func (m *trackerScopeManager) FetchLabels(ctx context.Context) ([]string, error) {
	return m.fm.FetchLabels(ctx)
}

// FetchTeams implements server.ScopeManager.
func (m *trackerScopeManager) FetchTeams(ctx context.Context) ([]server.TrackerTeam, error) {
	teams, err := m.fm.FetchTeams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.TrackerTeam, len(teams))
	for i, t := range teams {
		out[i] = server.TrackerTeam{ID: t.ID, Key: t.Key, Name: t.Name}
	}
	return out, nil
}

// FetchUsers implements server.ScopeManager.
func (m *trackerScopeManager) FetchUsers(ctx context.Context) ([]server.TrackerUser, error) {
	users, err := m.fm.FetchUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.TrackerUser, len(users))
	for i, u := range users {
		out[i] = server.TrackerUser{ID: u.ID, Name: u.Name, Email: u.Email}
	}
	return out, nil
}

// normalizeScope trims the scope and drops empty and duplicate entries.
func normalizeScope(sc tracker.Scope) tracker.Scope {
	clean := func(in []string, fold bool) []string {
		out := []string{}
		for _, v := range in {
			v = strings.TrimSpace(v)
			if v == "" || slices.ContainsFunc(out, func(o string) bool {
				return o == v || (fold && strings.EqualFold(o, v))
			}) {
				continue
			}
			out = append(out, v)
		}
		return out
	}
	return tracker.Scope{Projects: clean(sc.Projects, false), Assignee: strings.TrimSpace(sc.Assignee), Labels: clean(sc.Labels, true)}
}

// saveTrackerScope is the one save path for the combined scope (dashboard
// and TUI), with saveProjectFilter's discipline: the assignee is resolved
// first (an unknown user is refused, nothing changes), then the settings file is
// written as a fenced self-write (no reload), and only then is the scope
// applied in memory, so memory never disagrees with the file.
func saveTrackerScope(ctx context.Context, sm tracker.ScopeManager, path string, gen uint64, sc tracker.Scope, who string) error {
	sc = normalizeScope(sc)
	if sc.Assignee != "" {
		rctx, cancel := context.WithTimeout(ctx, assigneeResolveTimeout)
		_, err := sm.ResolveUser(rctx, sc.Assignee)
		cancel()
		if err != nil {
			slog.Warn(who+": tracker scope not saved; the assignee did not resolve", "assignee", sc.Assignee, "error", err)
			if errors.Is(err, tracker.ErrAssigneeNotFound) {
				return fmt.Errorf("%w: %q matches no user the tracker key can see", server.ErrScopeAssigneeNotFound, sc.Assignee)
			}
			return fmt.Errorf("assignee: %w", err)
		}
	}
	if path != "" {
		unlock, err := beginSettingsSave(path, gen)
		if err != nil {
			slog.Warn(who+": tracker scope not saved; WORKFLOW.md is reloading — retry", "error", err)
			return err
		}
		defer unlock()
		if err := updateWorkflowScope(path, sc); err != nil {
			slog.Warn(who+": tracker scope not saved; the runtime scope is unchanged", "error", err, "path", path)
			return err
		}
	}
	sm.SetScope(sc)
	return nil
}

// newScopeManager is the server's scope API for tr, nil when tr has no
// tracker filter (the endpoints then answer 501).
func newScopeManager(tr tracker.Tracker, workflowPath string, gen uint64) server.ScopeManager {
	fm, ok := tr.(tracker.FilterManager)
	if !ok {
		return nil
	}
	sm, _ := tr.(tracker.ScopeManager)
	return &trackerScopeManager{sm: sm, fm: fm, workflowPath: workflowPath, settingsGen: gen}
}

// tuiProjectFilterSaver is the TUI picker's save: with a combinable scope
// its "assigned to" row ANDs with the projects (labels kept as they are);
// otherwise the plain project filter save.
func tuiProjectFilterSaver(tpm tracker.ProjectManager, cfg *config.Config, path string, gen uint64) func([]string) {
	return func(slugs []string) {
		// #198: the picker edits projects; with tracker.filter in effect
		// the filter is edited in the dashboard or WORKFLOW.md instead.
		if fm, ok := tpm.(tracker.FilterManager); ok && fm.FilterMode() {
			slog.Warn("tui: project filter not saved; tracker.filter is in effect — edit it in Settings → Tracker Scope or WORKFLOW.md")
			return
		}
		if sm, ok := tpm.(tracker.ScopeManager); ok {
			ctx, cancel := context.WithTimeout(context.Background(), assigneeResolveTimeout)
			defer cancel()
			_ = saveTrackerScope(ctx, sm, path, gen, tuiPickerScope(sm, cfg, slugs), "tui") // logged inside
			return
		}
		_ = saveProjectFilter(tpm, path, gen, slugs, "tui") // logged inside
	}
}

// tuiPickerScope turns the TUI picker's selection into a scope: the project
// slugs, plus "assigned to" when its row is selected (the current assignee,
// else the configured one, else "me"), keeping the current labels.
func tuiPickerScope(sm tracker.ScopeManager, cfg *config.Config, slugs []string) tracker.Scope {
	cur := sm.Scope()
	sc := tracker.Scope{Projects: []string{}, Labels: cur.Labels}
	assigned := false
	for _, s := range slugs {
		if s == "__assignee__" {
			assigned = true
			continue
		}
		sc.Projects = append(sc.Projects, s)
	}
	if assigned {
		sc.Assignee = cmp.Or(cur.Assignee, strings.TrimSpace(cfg.Tracker.Assignee), "me")
	}
	return sc
}

// updateWorkflowScope saves the combined scope (#182) to the settings file
// in one write (#239: tracker.* is layered): project_slug as the project
// filter save writes it, tracker.assignee and tracker.labels set or removed,
// and tracker.filter removed (#198: the legacy keys and tracker.filter cannot
// both be set). The keys leave WORKFLOW.md in the same save.
func updateWorkflowScope(path string, sc tracker.Scope) error {
	var labels any
	if len(sc.Labels) > 0 {
		labels = sc.Labels
	}
	err := persistSettings(path, []setting{
		{Key: "tracker.project_slug", Value: projectSlugSetting(sc.Projects)},
		{Key: "tracker.assignee", Value: nonEmpty(sc.Assignee)},
		{Key: "tracker.filter"},
		{Key: "tracker.labels", Value: labels},
	})
	if err != nil {
		return fmt.Errorf("tracker scope: update %s: %w", path, err)
	}
	return nil
}
