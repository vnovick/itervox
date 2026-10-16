package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

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
// write project_slug: "*" on disk (#182) — the explicit "all issues" filter.
// A nil in-memory filter would fall back to the scope this generation was
// loaded with, which the file no longer carries. The assignee entry
// (tracker.AssigneeScopeFilter, #182) writes tracker.assignee instead and
// comments project_slug out; a project filter comments assignee out, so the
// file always names exactly one scope.
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
	// #182: the assignee scope is a filter of its own, never combined with
	// projects. Its user is resolved before anything is written, so a scope
	// nobody can read is refused with the file and memory unchanged.
	assignee := ""
	if slices.Contains(slugs, tracker.AssigneeScopeFilter) {
		as, ok := pm.(tracker.AssigneeScoper)
		if !ok {
			return errors.New("project filter: this tracker has no assignee scope")
		}
		ctx, cancel := context.WithTimeout(context.Background(), assigneeResolveTimeout)
		_, err := as.ResolveAssignee(ctx)
		cancel()
		if err != nil {
			slog.Warn(who+": assignee scope not saved; the assignee did not resolve", "error", err)
			return fmt.Errorf("assignee scope: %w", err)
		}
		assignee = as.AssigneeSpec()
		slugs = []string{tracker.AssigneeScopeFilter}
	}
	if path != "" {
		unlock, err := beginSettingsSave(path, gen)
		if err != nil {
			slog.Warn(who+": project filter not saved; WORKFLOW.md is reloading — retry", "error", err)
			return err
		}
		defer unlock()
		write := func() error { return updateWorkflowProjectSlug(path, slugs) }
		if assignee != "" {
			write = func() error { return updateWorkflowAssignee(path, assignee) }
		}
		if err := write(); err != nil {
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

// assigneeResolveTimeout bounds resolving an assignee scope's user (#182).
const assigneeResolveTimeout = 15 * time.Second

// updateWorkflowProjectSlug rewrites the project_slug line in the YAML frontmatter
// of the given WORKFLOW.md path. If slugs is nil or empty, the line names every
// project (project_slug: "*", #182: a Linear scope must be named). An
// uncommented tracker.assignee line is commented out: the scope is a project
// now, and exactly one of the two may be set.
// T-55: returns an error so callers can decide whether to surface a persistence
// failure to the user.
//
// The read-modify-write holds the same per-path lock as every settings
// patcher (M0-close G3, CORE-006). The line keeps its own indentation. It is
// a self-write (CORE-116/CORE-160): the caller applies the filter in memory.
func updateWorkflowProjectSlug(path string, slugs []string) error {
	value := strings.Join(slugs, ", ")
	if len(slugs) == 0 {
		value = `"*"  # every project; select interactively via TUI (p) or web dashboard`
	}
	err := workflow.ApplyAndWriteFrontMatter(path, func(frontLines []string) ([]string, error) {
		frontLines = commentTrackerKey(frontLines, "assignee")
		found := false
		for i, line := range frontLines {
			// Match both commented and uncommented project_slug lines.
			stripped := strings.TrimLeft(line, " #")
			if !strings.HasPrefix(stripped, "project_slug:") {
				continue
			}
			indent := strings.Repeat(" ", len(line)-len(strings.TrimLeft(line, " ")))
			frontLines[i] = indent + "project_slug: " + value
			found = true
			break
		}
		if found {
			return frontLines, nil
		}
		// M4-close D4: no project_slug line at all — insert one at the end
		// of the top-level tracker: block, with the block's indentation.
		return insertTrackerKey(frontLines, "project_slug: "+value)
	})
	if err != nil {
		return fmt.Errorf("project_slug: update %s: %w", path, err)
	}
	return nil
}

// updateWorkflowAssignee writes the assignee scope (#182): tracker.assignee
// set to spec (an existing line, commented or not, is reused; else one is
// inserted) and project_slug commented out, so exactly one scope is set.
func updateWorkflowAssignee(path, spec string) error {
	err := workflow.ApplyAndWriteFrontMatter(path, func(frontLines []string) ([]string, error) {
		frontLines = commentTrackerKey(frontLines, "project_slug")
		if i, indent := trackerKeyLine(frontLines, "assignee"); i >= 0 {
			frontLines[i] = indent + "assignee: " + spec
			return frontLines, nil
		}
		return insertTrackerKey(frontLines, "assignee: "+spec)
	})
	if err != nil {
		return fmt.Errorf("assignee: update %s: %w", path, err)
	}
	return nil
}

// trackerKeyLine finds key's line, commented or not, among the direct
// children of the top-level tracker: block. It returns the line index (-1
// when absent) and the line's indentation.
func trackerKeyLine(frontLines []string, key string) (int, string) {
	start := -1
	for i, line := range frontLines {
		if trackerBlockRE.MatchString(line) {
			start = i
			break
		}
	}
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

// commentTrackerKey comments out an uncommented tracker.<key> line, keeping
// its value so a person can see (and restore) what it was.
func commentTrackerKey(frontLines []string, key string) []string {
	i, indent := trackerKeyLine(frontLines, key)
	if i < 0 {
		return frontLines
	}
	if body := strings.TrimSpace(frontLines[i]); !strings.HasPrefix(body, "#") {
		frontLines[i] = indent + "# " + body
	}
	return frontLines
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
