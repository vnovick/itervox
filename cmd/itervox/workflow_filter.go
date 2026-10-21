package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

// #198 — the tracker filter: groups of conditions, ANDed inside a group and
// ORed between groups. The dashboard's group builder saves it here through
// the same fenced self-write as the #182 scope save.

// checkFilter refuses a filter the tracker cannot read: a malformed group
// or a condition the tracker has no equivalent for.
func checkFilter(fm tracker.FilterManager, f issuefilter.Filter) error {
	if err := f.Validate("filter"); err != nil {
		return fmt.Errorf("%w: %w", server.ErrFilterInvalid, err)
	}
	if c := f.Unsupported(fm.FilterConditions()); c != "" {
		return fmt.Errorf("%w: the %q condition is not supported by this tracker (supported: %s)",
			server.ErrFilterInvalid, c, strings.Join(fm.FilterConditions(), ", "))
	}
	return nil
}

// resolveFilterAssignees resolves every assignee f names, mapping an
// unknown user to server.ErrScopeAssigneeNotFound.
func resolveFilterAssignees(ctx context.Context, fm tracker.FilterManager, f issuefilter.Filter) error {
	rctx, cancel := context.WithTimeout(ctx, assigneeResolveTimeout)
	defer cancel()
	if err := fm.ResolveFilter(rctx, f); err != nil {
		if errors.Is(err, tracker.ErrAssigneeNotFound) {
			return fmt.Errorf("%w: %v", server.ErrScopeAssigneeNotFound, err)
		}
		return fmt.Errorf("assignee: %w", err)
	}
	return nil
}

// saveTrackerFilter is the one save path for the tracker filter, with
// saveTrackerScope's discipline: validated and resolved first (a refused
// filter changes nothing), then WORKFLOW.md is written as a fenced
// self-write (no reload), then the filter is applied in memory. The legacy
// scope keys are commented out, since they and tracker.filter cannot both
// be set; legacyProjectSlug says whether project_slug is one of them (it is
// the repository on GitHub).
func saveTrackerFilter(ctx context.Context, fm tracker.FilterManager, path string, gen uint64, f issuefilter.Filter, legacyProjectSlug bool, who string) error {
	f = f.Normalize()
	if err := checkFilter(fm, f); err != nil {
		return err
	}
	if err := resolveFilterAssignees(ctx, fm, f); err != nil {
		slog.Warn(who+": tracker filter not saved; an assignee did not resolve", "error", err)
		return err
	}
	if path != "" {
		unlock, err := beginSettingsSave(path, gen)
		if err != nil {
			slog.Warn(who+": tracker filter not saved; WORKFLOW.md is reloading — retry", "error", err)
			return err
		}
		defer unlock()
		if err := updateWorkflowFilter(path, f, legacyProjectSlug); err != nil {
			slog.Warn(who+": tracker filter not saved; the runtime filter is unchanged", "error", err, "path", path)
			return err
		}
	}
	fm.SetFilter(f)
	return nil
}

// updateWorkflowFilter writes tracker.filter as one flow-style line (or
// comments it out when empty) and comments the legacy scope keys out, in
// one locked read-modify-write.
func updateWorkflowFilter(path string, f issuefilter.Filter, legacyProjectSlug bool) error {
	err := workflow.ApplyAndWriteFrontMatter(path, func(frontLines []string) ([]string, error) {
		var err error
		value := ""
		if len(f) > 0 {
			value = f.YAMLFlow()
		}
		if frontLines, err = setTrackerBlock(frontLines, "filter", value); err != nil {
			return nil, err
		}
		if legacyProjectSlug {
			if frontLines, err = setProjectSlugLine(frontLines, nil); err != nil {
				return nil, err
			}
		}
		for _, key := range []string{"assignee", "team", "labels"} {
			if frontLines, err = setTrackerKey(frontLines, key, ""); err != nil {
				return nil, err
			}
		}
		return frontLines, nil
	})
	if err != nil {
		return fmt.Errorf("tracker filter: update %s: %w", path, err)
	}
	return nil
}

// setTrackerBlock is setTrackerKey for a key whose value may span lines
// (tracker.filter written in block style): the key line and its
// continuation lines are replaced by "key: value", or all commented out
// when value is empty. A commented-out block's continuation lines are the
// following commented lines whose text is indented or a "- " list item.
func setTrackerBlock(frontLines []string, key, value string) ([]string, error) {
	i, indent := trackerKeyLine(frontLines, key)
	if i < 0 {
		if value == "" {
			return frontLines, nil
		}
		return insertTrackerKey(frontLines, key+": "+value)
	}
	commented := strings.HasPrefix(strings.TrimSpace(frontLines[i]), "#")
	end := i + 1
	for ; end < len(frontLines); end++ {
		line := frontLines[end]
		if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, indent) {
			break
		}
		rest := line[len(indent):]
		if commented {
			body, ok := strings.CutPrefix(rest, "# ")
			if !ok || (!strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "-")) {
				break
			}
			continue
		}
		if !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") && !strings.HasPrefix(rest, "- ") {
			break
		}
	}
	out := append([]string{}, frontLines[:i]...)
	switch {
	case value != "":
		out = append(out, indent+key+": "+value)
	case commented:
		out = append(out, frontLines[i:end]...)
	default:
		for _, line := range frontLines[i:end] {
			out = append(out, indent+"# "+line[len(indent):])
		}
	}
	return append(out, frontLines[end:]...), nil
}
