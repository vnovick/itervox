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
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/server"
)

// Lessons round 2 (#217) wiring: the reverted-PR scan the orchestrator runs
// off its event loop (#201's revert detection, read through
// internal/gitexec), lesson-to-rule promotion (a rules-layer write through
// the comment-preserving writer, then the lesson archived with a pointer)
// and the enforcement suggestion's accept / dismiss.

// lessonRevertScan is Orchestrator.RevertScan: the PRs the base branch of
// the hotspots repository reverted, newest revert first. A repository
// without the base branch has nothing to scan (no error: it would be
// logged every scan).
func lessonRevertScan(cfg *config.Config, workflowPath string) func(context.Context) ([]orchestrator.RevertedPR, error) {
	src := hotspotSourceFor(cfg, workflowPath)
	return func(ctx context.Context) ([]orchestrator.RevertedPR, error) {
		ctx, cancel := context.WithTimeout(ctx, hotspotsRefreshTimeout)
		defer cancel()
		baseRef := resolveBaseRef(ctx, gitexecHotspotGit, src)
		if baseRef == "" {
			slog.Debug("lessons: revert scan skipped: base branch not found", "repo", src.repo, "base", src.base)
			return nil, nil
		}
		reverted := revertedPRs(ctx, gitexecHotspotGit, src.repo, baseRef)
		out := make([]orchestrator.RevertedPR, 0, len(reverted))
		for n, subject := range reverted {
			out = append(out, orchestrator.RevertedPR{Number: n, Subject: subject})
		}
		slices.SortFunc(out, func(a, b orchestrator.RevertedPR) int { return cmp.Compare(a.Number, b.Number) })
		return out, nil
	}
}

// lessonEnforcer accepts and dismisses enforcement suggestions
// (*orchestrator.Orchestrator; a fake in tests).
type lessonEnforcer interface {
	AcceptLessonEnforcement(ctx context.Context, id string) (memory.Entry, *domain.Issue, error)
	DismissLessonEnforcement(id string) (memory.Entry, error)
}

// lessonRuleID is the id a promoted lesson's rule gets.
func lessonRuleID(entryID string) string {
	id := "lesson-" + entryID
	return id[:min(len(id), 64)]
}

// PromoteLessonToRule implements server.MemoryClient: an approved lesson
// becomes a rule in the user or project layer (scoped to the lesson's
// profiles), and the lesson is archived pointing at it. A retry after the
// rule was written but the lesson not archived finds its own rule and only
// archives.
func (a *memoryAdapter) PromoteLessonToRule(id string, req server.LessonRuleRequest) (server.LessonRuleResult, error) {
	if req.Layer != rules.LayerUser && req.Layer != rules.LayerProject {
		return server.LessonRuleResult{}, fmt.Errorf("%w: a lesson is promoted to the user or project layer, not %q", server.ErrUnknownRulesLayer, req.Layer)
	}
	path := rules.LayerPath(a.cfg.WorkflowPath, req.Layer)
	if path == "" {
		return server.LessonRuleResult{}, fmt.Errorf("%w: the %s layer is off", server.ErrUnknownRulesLayer, req.Layer)
	}
	sev := cmp.Or(req.Severity, rules.SeverityShould)
	if sev != rules.SeverityMust && sev != rules.SeverityShould {
		return server.LessonRuleResult{}, fmt.Errorf(`%w: severity must be "must" or "should", not %q`, rules.ErrInvalid, sev)
	}
	e, err := a.store().Get(id)
	if err != nil {
		return server.LessonRuleResult{}, err
	}
	if e.Type != memory.TypeLesson || e.Status != memory.StatusActive {
		return server.LessonRuleResult{}, fmt.Errorf("%w: only an approved lesson can become a rule (%q is a %s %s entry)", memory.ErrConflict, id, e.Status, e.Type)
	}
	text := e.Text
	if req.Text != nil {
		text = strings.TrimSpace(*req.Text)
	}
	if text == "" {
		return server.LessonRuleResult{}, fmt.Errorf("%w: the rule text must not be empty", rules.ErrInvalid)
	}
	desc := "Promoted from lesson " + e.ID
	if e.Source.Issue != "" {
		desc += " (first seen on " + e.Source.Issue + ")"
	}
	rule := rules.Rule{ID: lessonRuleID(e.ID), Severity: sev, Text: text, Description: desc + ".", Scope: rules.Scope{Profiles: e.Profiles}}
	layer := rules.ReadLayer(req.Layer, path)
	if layer.Err != nil {
		return server.LessonRuleResult{}, fmt.Errorf("%w: fix the %s rules layer first: %w", rules.ErrInvalid, req.Layer, layer.Err)
	}
	list := slices.Clone(layer.Rules)
	switch i := slices.IndexFunc(list, func(r rules.Rule) bool { return strings.EqualFold(r.ID, rule.ID) }); {
	case i < 0:
		list = append(list, rule)
		if err := rules.SaveRules(path, list, layer.Version); err != nil {
			return server.LessonRuleResult{}, err
		}
	case list[i].Text != rule.Text:
		return server.LessonRuleResult{}, fmt.Errorf("%w: the %s layer already has a different rule %q", memory.ErrConflict, req.Layer, rule.ID)
	default:
		rule = list[i] // written by an earlier attempt
	}
	archived, err := a.store().ArchiveLessonAsRule(id, memory.RuleLink{Layer: req.Layer, ID: rule.ID})
	if err != nil {
		return server.LessonRuleResult{}, err
	}
	slog.Info("memory: lesson promoted to a rule", "id", id, "layer", req.Layer, "rule", rule.ID, "path", path)
	return server.LessonRuleResult{Entry: a.view(archived, a.runRefs()), Layer: req.Layer, Path: path, Rule: rule}, nil
}

// AcceptLessonEnforcement implements server.MemoryClient.
func (a *memoryAdapter) AcceptLessonEnforcement(ctx context.Context, id string) (server.LessonEnforceResult, error) {
	if a.enforcement == nil {
		return server.LessonEnforceResult{}, fmt.Errorf("%w: no tracker is wired", server.ErrEnforcementIssue)
	}
	out, issue, err := a.enforcement.AcceptLessonEnforcement(ctx, id)
	if err != nil {
		if issue == nil && !isMemoryError(err) {
			return server.LessonEnforceResult{}, fmt.Errorf("%w: %w", server.ErrEnforcementIssue, err)
		}
		return server.LessonEnforceResult{}, err
	}
	res := server.LessonEnforceResult{Entry: a.view(out, a.runRefs())}
	if issue != nil {
		res.Issue = server.LessonEnforceIssue{Identifier: issue.Identifier, Title: issue.Title}
		if issue.URL != nil {
			res.Issue.URL = *issue.URL
		}
	}
	return res, nil
}

// DismissLessonEnforcement implements server.MemoryClient.
func (a *memoryAdapter) DismissLessonEnforcement(id string) (server.MemoryEntryView, error) {
	if a.enforcement == nil {
		return server.MemoryEntryView{}, fmt.Errorf("%w: no orchestrator is wired", server.ErrEnforcementIssue)
	}
	out, err := a.enforcement.DismissLessonEnforcement(id)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	return a.view(out, a.runRefs()), nil
}

// isMemoryError reports whether err is one of the memory store's errors
// (mapped to their own status codes).
func isMemoryError(err error) bool {
	for _, target := range []error{memory.ErrNotFound, memory.ErrConflict, memory.ErrInvalid, memory.ErrTooLarge, memory.ErrScopeOff} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
