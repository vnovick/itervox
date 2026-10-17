package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

// #182 — the combined tracker scope (projects AND assignee AND labels): the
// dashboard's scope filter and the TUI picker save it here.

// linearScopeManager adapts tracker.ScopeManager to server.ScopeManager.
type linearScopeManager struct {
	sm           tracker.ScopeManager
	workflowPath string
	// settingsGen fences saves from a superseded generation (CORE-160).
	settingsGen uint64
}

// GetScope implements server.ScopeManager.
func (m *linearScopeManager) GetScope() server.TrackerScope {
	sc := m.sm.Scope()
	return server.TrackerScope{Projects: nonNil(sc.Projects), Assignee: sc.Assignee, Labels: nonNil(sc.Labels)}
}

// SetScope implements server.ScopeManager.
func (m *linearScopeManager) SetScope(ctx context.Context, sc server.TrackerScope) error {
	return saveTrackerScope(ctx, m.sm, m.workflowPath, m.settingsGen,
		tracker.Scope{Projects: sc.Projects, Assignee: sc.Assignee, Labels: sc.Labels}, "dashboard")
}

// FetchLabels implements server.ScopeManager.
func (m *linearScopeManager) FetchLabels(ctx context.Context) ([]string, error) {
	return m.sm.FetchLabels(ctx)
}

// FetchUsers implements server.ScopeManager.
func (m *linearScopeManager) FetchUsers(ctx context.Context) ([]server.TrackerUser, error) {
	users, err := m.sm.FetchUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.TrackerUser, len(users))
	for i, u := range users {
		out[i] = server.TrackerUser{ID: u.ID, Name: u.Name, Email: u.Email}
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
// first (an unknown user is refused, nothing changes), then WORKFLOW.md is
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
// combinable scope (the endpoints then answer 501).
func newScopeManager(tr tracker.Tracker, workflowPath string, gen uint64) server.ScopeManager {
	sm, ok := tr.(tracker.ScopeManager)
	if !ok {
		return nil
	}
	return &linearScopeManager{sm: sm, workflowPath: workflowPath, settingsGen: gen}
}

// tuiProjectFilterSaver is the TUI picker's save: with a combinable scope
// its "assigned to" row ANDs with the projects (labels kept as they are);
// otherwise the plain project filter save.
func tuiProjectFilterSaver(tpm tracker.ProjectManager, cfg *config.Config, path string, gen uint64) func([]string) {
	return func(slugs []string) {
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

// updateWorkflowScope writes the combined scope to WORKFLOW.md in one
// locked read-modify-write (#182): project_slug as the project filter save
// writes it (commented out for every project), tracker.assignee set or
// commented out, tracker.labels set as a flow list or commented out.
func updateWorkflowScope(path string, sc tracker.Scope) error {
	err := workflow.ApplyAndWriteFrontMatter(path, func(frontLines []string) ([]string, error) {
		frontLines, err := setProjectSlugLine(frontLines, sc.Projects)
		if err != nil {
			return nil, err
		}
		if frontLines, err = setTrackerKey(frontLines, "assignee", sc.Assignee); err != nil {
			return nil, err
		}
		labels := ""
		if len(sc.Labels) > 0 {
			quoted := make([]string, len(sc.Labels))
			for i, l := range sc.Labels {
				quoted[i] = strconv.Quote(l)
			}
			labels = "[" + strings.Join(quoted, ", ") + "]"
		}
		return setTrackerKey(frontLines, "labels", labels)
	})
	if err != nil {
		return fmt.Errorf("tracker scope: update %s: %w", path, err)
	}
	return nil
}

// setTrackerKey sets tracker.<key> to value, reusing its line (commented or
// not) or inserting one at the end of the tracker block; an empty value
// comments an existing line out (keeping its value for a person to see) and
// adds nothing.
func setTrackerKey(frontLines []string, key, value string) ([]string, error) {
	i, indent := trackerKeyLine(frontLines, key)
	switch {
	case value != "" && i >= 0:
		frontLines[i] = indent + key + ": " + value
		return frontLines, nil
	case value != "":
		return insertTrackerKey(frontLines, key+": "+value)
	case i >= 0:
		if body := strings.TrimSpace(frontLines[i]); !strings.HasPrefix(body, "#") {
			frontLines[i] = indent + "# " + body
		}
	}
	return frontLines, nil
}

// trackerKeyLine finds key's line, commented or not, among the direct
// children of the top-level tracker: block (not tracker.sync's). It returns
// the line index (-1 when absent) and the line's indentation.
func trackerKeyLine(frontLines []string, key string) (int, string) {
	start := slices.IndexFunc(frontLines, trackerBlockRE.MatchString)
	if start < 0 {
		return -1, ""
	}
	child := ""
	for j := start + 1; j < len(frontLines); j++ {
		line := frontLines[j]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead == "" {
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			break // the next top-level key
		}
		if child == "" && !strings.HasPrefix(trimmed, "#") {
			child = lead
		}
		if child != "" && lead != child {
			continue // nested deeper (tracker.sync and the like)
		}
		if strings.HasPrefix(strings.TrimLeft(trimmed, "# "), key+":") {
			return j, lead
		}
	}
	return -1, ""
}
