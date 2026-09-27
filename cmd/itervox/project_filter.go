package main

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

// SetProjectFilter implements server.ProjectManager: persist the filter to
// WORKFLOW.md, then apply it to the tracker client (CORE-160). M4-close D4:
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
// The in-memory filter is set to exactly what WORKFLOW.md now says: the
// listed slugs, or — for an empty list AND for a reset (nil), both of which
// comment project_slug out on disk — the explicit "all issues" filter. A nil
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
// WORKFLOW.md.
func saveProjectFilter(pm tracker.ProjectManager, path string, gen uint64, slugs []string, who string) error {
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

// updateWorkflowProjectSlug rewrites the project_slug line in the YAML frontmatter
// of the given WORKFLOW.md path. If slugs is nil or empty, the line is commented out.
// T-55: returns an error so callers can decide whether to surface a persistence
// failure to the user.
//
// The read-modify-write holds the same per-path lock as every settings
// patcher (M0-close G3, CORE-006). The line keeps its own indentation. It is
// a self-write (CORE-116/CORE-160): the caller applies the filter in memory.
func updateWorkflowProjectSlug(path string, slugs []string) error {
	err := workflow.ApplyAndWriteFrontMatter(path, func(frontLines []string) ([]string, error) {
		found := false
		for i, line := range frontLines {
			// Match both commented and uncommented project_slug lines.
			stripped := strings.TrimLeft(line, " #")
			if !strings.HasPrefix(stripped, "project_slug:") {
				continue
			}
			indent := strings.Repeat(" ", len(line)-len(strings.TrimLeft(line, " ")))
			if len(slugs) == 0 {
				frontLines[i] = indent + "# project_slug:  # Optional — select interactively via TUI (p) or web dashboard"
			} else {
				frontLines[i] = indent + "project_slug: " + strings.Join(slugs, ", ")
			}
			found = true
			break
		}
		if found || len(slugs) == 0 {
			// No key and "all issues": an absent project_slug already means
			// no filter, so the file agrees without a write.
			return frontLines, nil
		}
		// M4-close D4: no project_slug line at all — insert one at the end
		// of the top-level tracker: block, with the block's indentation.
		return insertTrackerKey(frontLines, "project_slug: "+strings.Join(slugs, ", "))
	})
	if err != nil {
		return fmt.Errorf("project_slug: update %s: %w", path, err)
	}
	return nil
}

// errNoTrackerBlock is returned when a project filter must be written into a
// front matter that has no top-level tracker: block to hold it.
var errNoTrackerBlock = errors.New("no top-level tracker: block in the front matter to hold project_slug")

var trackerBlockRE = regexp.MustCompile(`^tracker:\s*(#.*)?$`)

// insertTrackerKey appends "<indent><kv>" after the last child line of the
// top-level tracker: block (M4-close D4). The indentation is the block's
// first child's, defaulting to two spaces.
func insertTrackerKey(frontLines []string, kv string) ([]string, error) {
	start := -1
	for i, line := range frontLines {
		if trackerBlockRE.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, errNoTrackerBlock
	}
	indent, last := "  ", start
	for j := start + 1; j < len(frontLines); j++ {
		line := frontLines[j]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead == "" {
			if strings.HasPrefix(trimmed, "#") {
				continue // a top-level comment does not end the block
			}
			break // the next top-level key
		}
		if last == start && !strings.HasPrefix(trimmed, "#") {
			indent = lead
		}
		last = j
	}
	out := make([]string, 0, len(frontLines)+1)
	out = append(out, frontLines[:last+1]...)
	out = append(out, indent+kv)
	return append(out, frontLines[last+1:]...), nil
}
