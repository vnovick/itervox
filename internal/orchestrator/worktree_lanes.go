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

	// Every changed path the audit saw (tracked and untracked, not cut at
	// workspace.DiffMaxFiles: classifying a path is cheap), so Lane is the
	// highest lane of the whole change; the listed files when the audit
	// failed.
	paths := make([]string, 0, len(sum.Files))
	for _, f := range sum.Files {
		paths = append(paths, f.Path)
	}
	audit, err := guardrails.AuditRange(ctx, path, sum.MergeBase, "")
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return LanesView{}, ctx.Err()
		}
		v.FindingsTruncated = true // the files still classify; their findings are unknown
	case len(audit.Paths) > 0 || len(paths) == 0:
		paths = audit.Paths
		v.Truncated = v.Truncated || audit.PathsTruncated
	}
	found := laneViewFindings(audit.Findings, paths)
	v.FindingsTruncated = v.FindingsTruncated || audit.Truncated || found.truncated

	res := lanes.Classify(paths)
	v.Lane = res.Lane
	v.Files = make([]LaneFile, 0, len(sum.Files))
	for _, f := range sum.Files {
		lane, rule := lanes.Match(f.Path)
		lf := LaneFile{DiffFile: f, Lane: lane, Rule: rule}
		mine := found.byFile[f.Path]
		reasons := make([]guardrails.LaneReason, 0, len(mine))
		for _, fd := range mine {
			reasons = append(reasons, guardrails.FindingReason(fd))
		}
		lf.Findings, _ = guardrails.SanitizeLaneReasons(reasons, found.fileTotal[f.Path])
		v.Files = append(v.Files, lf)
	}
	v.Reasons, v.ReasonsTotal = guardrails.LaneReasons(res, found.ordered, found.total)

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

// laneFindings are the Lanes view's findings, each (rule, path, line)
// once: ordered (file order, then line) up to maxLaneFindings, byFile up
// to maxLaneFileFindings per file (by line), and how many there were in
// all and per file.
type laneFindings struct {
	ordered   []guardrails.AuditFinding
	byFile    map[string][]guardrails.AuditFinding
	fileTotal map[string]int
	total     int
	// truncated: a finding was dropped over a cap.
	truncated bool
}

type laneFindingKey struct {
	rule, path string
	line       int
}

// laneViewFindings keeps the findings on paths. Linear in the findings
// (a map de-duplicates, a precomputed path index orders) and bounded: a
// file keeps at most maxLaneFileFindings, so a change with hundreds of
// thousands of findings costs one pass, never a scan of what was kept.
func laneViewFindings(found []guardrails.AuditFinding, paths []string) laneFindings {
	index := make(map[string]int, len(paths))
	for i, p := range paths {
		if _, ok := index[p]; !ok {
			index[p] = i
		}
	}
	out := laneFindings{byFile: map[string][]guardrails.AuditFinding{}, fileTotal: map[string]int{}}
	seen := map[laneFindingKey]struct{}{}
	for _, f := range found {
		if _, ok := index[f.Path]; !ok {
			continue
		}
		k := laneFindingKey{f.Rule, f.Path, f.Line}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out.total++
		out.fileTotal[f.Path]++
		if len(out.byFile[f.Path]) < maxLaneFileFindings {
			out.byFile[f.Path] = append(out.byFile[f.Path], f)
		} else {
			out.truncated = true
		}
	}
	files := make([]string, 0, len(out.byFile))
	for p, fs := range out.byFile {
		files = append(files, p)
		slices.SortStableFunc(fs, func(a, b guardrails.AuditFinding) int { return a.Line - b.Line })
	}
	slices.SortFunc(files, func(a, b string) int { return index[a] - index[b] })
	for _, p := range files {
		if len(out.ordered) >= maxLaneFindings {
			break
		}
		fs := out.byFile[p]
		out.ordered = append(out.ordered, fs[:min(len(fs), maxLaneFindings-len(out.ordered))]...)
	}
	out.truncated = out.truncated || out.total > len(out.ordered)
	return out
}
