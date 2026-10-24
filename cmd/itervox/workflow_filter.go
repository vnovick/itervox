package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
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
// filter changes nothing), then the settings file is written as a fenced
// save (no reload; #239), then the filter is applied in memory. The legacy
// scope keys are removed, since they and tracker.filter cannot both
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

// updateWorkflowFilter saves tracker.filter to the settings file (#239:
// tracker.* is layered; persistSettings, which also removes the keys from
// WORKFLOW.md) and removes the legacy scope keys, since they and
// tracker.filter cannot both be set. An empty filter removes the key.
func updateWorkflowFilter(path string, f issuefilter.Filter, legacyProjectSlug bool) error {
	saved := []setting{{Key: "tracker.filter", Value: filterSetting(f)}}
	if legacyProjectSlug {
		saved = append(saved, setting{Key: "tracker.project_slug"})
	}
	for _, key := range []string{"assignee", "team", "labels"} {
		saved = append(saved, setting{Key: "tracker." + key})
	}
	if err := persistSettings(path, saved); err != nil {
		return fmt.Errorf("tracker filter: update %s: %w", path, err)
	}
	return nil
}

// filterSetting is tracker.filter as a settings file holds it (the list of
// groups its parser reads), nil for an empty filter.
func filterSetting(f issuefilter.Filter) any {
	if len(f) == 0 {
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	v, err := config.ParseJSONC(b)
	if err != nil {
		return nil
	}
	return v
}
