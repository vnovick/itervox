package orchestrator

import (
	"context"
	"slices"

	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/workspace"
)

// worktree_lanes.go — the Lanes view (GET /api/v1/issues/{identifier}/lanes):
// an issue's current branch against its base, every changed file in its
// guardrails.lanes lane with the pattern that put it there, the audit
// findings on each file (with line), why the change needs a person, and
// the issue's stack split record. Like WorktreeDiff it runs in the CALLER's
// goroutine (an HTTP handler): git through workspace / guardrails
// (gitexec) under worktreeDiffTimeout, and State only through Snapshot().
// It never sends an event or waits for the loop.

// Lanes view limits: findings kept per file and in all.
const (
	maxLaneFileFindings = 10
	maxLaneFindings     = 200
)

// LaneFile is one changed file in the Lanes view.
type LaneFile struct {
	workspace.DiffFile
	// Lane is the file's lane (auto, second_opinion or human); Rule the
	// pattern that put it there ("" for auto).
	Lane string
	Rule string
	// Findings are the audit findings on the file (kind finding, with the
	// head-version line), sanitized, at most maxLaneFileFindings.
	Findings []guardrails.LaneReason
}

// LanesView is an issue's Lanes view.
type LanesView struct {
	Base      string
	MergeBase string
	// Enforced: the guardrails are on and a lane has patterns, so a push
	// of these files would be held or reported. The files are classified
	// by the configured patterns either way.
	Enforced bool
	// Lane is the highest lane of any file.
	Lane  string
	Files []LaneFile
	// Truncated: the file list hit workspace.DiffMaxFiles;
	// FindingsTruncated: the diff was audited only in part, or findings
	// were dropped over maxLaneFindings.
	Truncated         bool
	FindingsTruncated bool
	// Reasons say why the change needs a person (or a second opinion):
	// findings first, then lane rules; ReasonsTotal is how many there were.
	Reasons      []guardrails.LaneReason
	ReasonsTotal int
	// Split is the issue's stack split decision (nil without one);
	// PushLane the lane its pushes reached (nil when none above auto).
	Split    *StackSplitRecord
	PushLane *PushLaneRecord
	// ApprovalPending: a push of the issue waits for a person now.
	ApprovalPending bool
}

// WorktreeLanes builds identifier's Lanes view from its worktree (the
// live working tree against the merge base with its base branch, as the
// Changes tab's file list). Off the event loop; see the file comment.
func (o *Orchestrator) WorktreeLanes(ctx context.Context, identifier string) (LanesView, error) {
	ctx, cancel := context.WithTimeout(ctx, worktreeDiffTimeout)
	defer cancel()
	sum, err := o.worktreeDiff(ctx, identifier, "")
	if err != nil {
		return LanesView{}, err
	}
	path, err := o.worktreePath(identifier)
	if err != nil {
		return LanesView{}, err
	}
	var lanes guardrails.Lanes
	enabled := false
	if o.cfg != nil { // guardrails.* is read-only after startup (not in the cfgMu list)
		g := o.cfg.Guardrails
		lanes, enabled = guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion}, g.Enabled
	}
	v := LanesView{Base: sum.Base, MergeBase: sum.MergeBase, Truncated: sum.Truncated,
		Enforced: enabled && !lanes.Empty()}

	paths := make([]string, 0, len(sum.Files))
	for _, f := range sum.Files {
		paths = append(paths, f.Path)
	}
	audit, err := guardrails.AuditRange(ctx, path, sum.MergeBase, "")
	if err != nil {
		if ctx.Err() != nil {
			return LanesView{}, ctx.Err()
		}
		v.FindingsTruncated = true // the files still classify; their findings are unknown
	}
	findings := laneViewFindings(audit.Findings, paths)
	v.FindingsTruncated = v.FindingsTruncated || audit.Truncated || len(findings) > maxLaneFindings
	findings = findings[:min(len(findings), maxLaneFindings)]

	res := lanes.Classify(paths)
	v.Lane = res.Lane
	v.Files = make([]LaneFile, 0, len(sum.Files))
	for _, f := range sum.Files {
		lane, rule := lanes.Match(f.Path)
		lf := LaneFile{DiffFile: f, Lane: lane, Rule: rule}
		var mine []guardrails.LaneReason
		for _, fd := range findings {
			if fd.Path == f.Path {
				mine = append(mine, guardrails.FindingReason(fd))
			}
		}
		lf.Findings, _ = guardrails.SanitizeLaneReasons(mine[:min(len(mine), maxLaneFileFindings)], len(mine))
		v.Files = append(v.Files, lf)
	}
	v.Reasons, v.ReasonsTotal = guardrails.LaneReasons(res, findings)

	snap := o.Snapshot()
	if s, ok := snap.StackSplits[identifier]; ok {
		v.Split = &s
	}
	if r, ok := snap.PushLanes[identifier]; ok {
		v.PushLane = &r
	}
	if e := snap.InputRequiredIssues[identifier]; e != nil && e.GuardrailApprovalID != "" && e.GuardrailLanePaths != nil {
		v.ApprovalPending = true
	}
	return v, nil
}

// laneViewFindings keeps the findings on the listed files, in file order
// then line order, each (rule, path, line) once.
func laneViewFindings(found []guardrails.AuditFinding, paths []string) []guardrails.AuditFinding {
	out := make([]guardrails.AuditFinding, 0, len(found))
	for _, f := range found {
		if !slices.Contains(paths, f.Path) || slices.ContainsFunc(out, func(o guardrails.AuditFinding) bool {
			return o.Rule == f.Rule && o.Path == f.Path && o.Line == f.Line
		}) {
			continue
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b guardrails.AuditFinding) int {
		if a.Path != b.Path {
			return slices.Index(paths, a.Path) - slices.Index(paths, b.Path)
		}
		return a.Line - b.Line
	})
	return out
}
