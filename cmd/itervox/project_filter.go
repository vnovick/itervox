package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// SetProjectFilter implements server.ProjectManager: persist the filter to
// the settings file (#239), then apply it to the tracker client (CORE-160). M4-close D4:
// a refused or failed persist is returned (the handler answers 503
// settings_reloading for the fence) and leaves the in-memory filter alone.
func (m *linearProjectManager) SetProjectFilter(slugs []string) error {
	return saveProjectFilter(m.pm, m.workflowPath, m.settingsGen, slugs, "dashboard")
}

// saveProjectFilter is the one save path for the Linear project filter
// (dashboard and TUI). CORE-160: it used to write through
// workflow.WriteAndReload, so every filter change reloaded WORKFLOW.md and
// stopped every in-flight turn. The tracker client already applies the
// filter in memory, so the write is now a self-write (no reload), fenced by
// the settings generation like every other settings save.
//
// The in-memory filter is set to exactly what the config now says: the
// listed slugs, or — for an empty list AND for a reset (nil), both of which
// remove project_slug on disk — the explicit "all issues" filter. A nil
// in-memory filter would fall back to the project_slug this generation was
// loaded with, which the file no longer carries.
//
// The per-generation consumers of tracker.project_slug that are NOT the
// tracker client (the history key, the header label) keep the load-time value
// until the next restart or operator reload.
//
// M4-close D4: memory changes only when the file did. A fence refusal
// (server.ErrSettingsReloading, wrapped) or a persist failure is returned and
// the tracker client keeps its current filter, so memory never disagrees with
// the saved config.
func saveProjectFilter(pm tracker.ProjectManager, path string, gen uint64, slugs []string, who string) error {
	// #198: project_slug and tracker.filter cannot both be set.
	if fm, ok := pm.(tracker.FilterManager); ok && fm.FilterMode() {
		slog.Warn(who + ": project filter not saved; tracker.filter is in effect")
		return server.ErrTrackerFilterInEffect
	}
	if path != "" {
		unlock, err := beginSettingsSave(path, gen)
		if err != nil {
			slog.Warn(who+": project filter not saved; WORKFLOW.md is reloading — retry", "error", err)
			return err
		}
		defer unlock()
		if err := updateWorkflowProjectSlug(path, slugs); err != nil {
			slog.Warn(who+": project filter not saved; the runtime filter is unchanged",
				"error", err, "path", path)
			return err
		}
	}
	if len(slugs) == 0 {
		slugs = []string{}
	}
	pm.SetProjectFilter(slugs)
	return nil
}

// updateWorkflowProjectSlug saves tracker.project_slug (the slugs joined
// with ", ", as WORKFLOW.md wrote them) to the settings file (#239:
// tracker.* is layered), and removes it from WORKFLOW.md. An empty list
// removes the key: no project_slug means no project filter. T-55: returns an
// error so callers can decide whether to surface a persistence failure. It
// holds the same per-path lock as every settings save, and is a self-write
// (CORE-116/CORE-160): the caller applies the filter in memory.
func updateWorkflowProjectSlug(path string, slugs []string) error {
	if err := persistSettings(path, []setting{{Key: "tracker.project_slug", Value: projectSlugSetting(slugs)}}); err != nil {
		return fmt.Errorf("project_slug: update %s: %w", path, err)
	}
	return nil
}

// projectSlugSetting is tracker.project_slug for slugs, nil (remove) for none.
func projectSlugSetting(slugs []string) any {
	if len(slugs) == 0 {
		return nil
	}
	return strings.Join(slugs, ", ")
}
