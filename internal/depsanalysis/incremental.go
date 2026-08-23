package depsanalysis

import (
	"log/slog"
	"time"
)

// Incremental pass modes. Mirrors the requestedMode strings accepted by
// PlanIncremental's callers ("full", "incremental", "auto", "") but only
// "full" and "incremental" are ever the *resolved* Mode on an
// IncrementalPlan — "auto"/"" always resolve to one of these two.
const (
	IncrementalModeFull        = "full"
	IncrementalModeIncremental = "incremental"
)

// IncrementalPlan is the outcome of PlanIncremental: which issues need a
// fresh analyzer pass and which can be carried forward from the prior
// sidecar unchanged.
type IncrementalPlan struct {
	// Mode is the resolved mode ("full" or "incremental"), not necessarily
	// what the caller requested — an unusable prior sidecar or a profile
	// change forces "full" regardless of request.
	Mode string
	// ToAnalyze is the issue set the caller must actually run through the
	// analyzer. Full mode: every current issue. Incremental mode: issues
	// that are new or whose content fingerprint changed.
	ToAnalyze []AnalyzerIssue
	// Unchanged holds the identifiers of issues whose content fingerprint
	// matched the prior sidecar exactly, keyed for O(1) membership checks.
	// Always non-nil; empty in full mode.
	Unchanged map[string]struct{}
}

// PlanIncremental decides whether a deps-analysis pass runs full or
// incremental over issues, given the caller's requested mode and the prior
// sidecar (nil when none exists yet).
//
// requestedMode == "full" always forces a full pass: every issue goes to
// ToAnalyze and Unchanged is empty.
//
// Any other requestedMode ("incremental", "auto", "", or anything else)
// resolves to incremental IFF a usable prior sidecar exists — prev != nil,
// prev has at least one Analyzed entry, and prev.Profile matches profile.
// A profile change always forces full: a different analyzer invalidates
// prior conclusions. Otherwise it falls back to full, same as an explicit
// "full" request.
//
// In incremental mode, each issue partitions by content fingerprint
// (IssueFingerprint(title, description)) against prev.Analyzed[identifier]:
// a match goes to Unchanged; a mismatch or missing entry (new issue) goes to
// ToAnalyze. The partition considers only issues in the fetch — an
// Analyzed entry for an issue no longer present is simply not consulted.
func PlanIncremental(issues []AnalyzerIssue, prev *Sidecar, profile string, requestedMode string) IncrementalPlan {
	if requestedMode != IncrementalModeFull && canPlanIncremental(prev, profile) {
		return partitionIncremental(issues, prev)
	}
	return IncrementalPlan{
		Mode:      IncrementalModeFull,
		ToAnalyze: append([]AnalyzerIssue(nil), issues...),
		Unchanged: map[string]struct{}{},
	}
}

// canPlanIncremental reports whether prev is a usable baseline for an
// incremental pass against profile.
func canPlanIncremental(prev *Sidecar, profile string) bool {
	return prev != nil && len(prev.Analyzed) > 0 && prev.Profile == profile
}

func partitionIncremental(issues []AnalyzerIssue, prev *Sidecar) IncrementalPlan {
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		Unchanged: map[string]struct{}{},
	}
	present := make(map[string]AnalyzerIssue, len(issues))
	changed := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		present[issue.Identifier] = issue
		fp := IssueFingerprint(issue.Title, issue.Description)
		if entry, ok := prev.Analyzed[issue.Identifier]; ok && entry.Fingerprint == fp {
			plan.Unchanged[issue.Identifier] = struct{}{}
			continue
		}
		plan.ToAnalyze = append(plan.ToAnalyze, issue)
		changed[issue.Identifier] = struct{}{}
	}
	plan.ToAnalyze = append(plan.ToAnalyze, priorEdgePeers(prev, changed, present)...)
	return plan
}

// priorEdgePeers returns the unchanged issues that sit on the far side of a
// prior edge whose other endpoint changed this pass, so the analyzer can see
// both ends of every edge it is being asked to re-derive.
//
// MergeIncremental carries a prior edge forward only when BOTH endpoints are
// unchanged, so a boundary-spanning edge is dropped and must come back from
// the fresh agent pass. The pass only ever receives plan.ToAnalyze — so
// without the peer in that set the analyzer cannot emit the edge even when
// it still holds, and it is gone for good. Auto-analysis is on by default,
// incremental is the default mode, and nothing schedules a periodic full
// pass, so repeated runs eroded the graph toward changed-to-changed edges
// only, silently un-gating dispatch as they went.
//
// Peers stay in plan.Unchanged: their own both-endpoints-unchanged edges
// must still revalidate. Being in ToAnalyze as well only means "visible to
// the analyzer this pass", which is exactly the context that makes a drop a
// real judgement rather than an artifact of the prompt's contents.
//
// The expansion is deliberately one hop and edge-driven, not transitive: it
// adds only the far endpoints of edges that actually touch a changed issue.
// A transitive closure would collapse into a full pass on any well-connected
// backlog, and incremental mode would stop paying for itself.
func priorEdgePeers(prev *Sidecar, changed map[string]struct{}, present map[string]AnalyzerIssue) []AnalyzerIssue {
	if prev == nil || len(changed) == 0 {
		return nil
	}
	var peers []AnalyzerIssue
	seen := make(map[string]struct{})
	consider := func(peer string) {
		if _, isChanged := changed[peer]; isChanged {
			return // already in ToAnalyze in its own right
		}
		if _, added := seen[peer]; added {
			return
		}
		issue, ok := present[peer]
		if !ok {
			return // absent from this fetch; nothing to show the analyzer
		}
		seen[peer] = struct{}{}
		peers = append(peers, issue)
	}
	// Iterated in sidecar order so the resulting ToAnalyze is deterministic.
	for _, e := range prev.Edges {
		_, srcChanged := changed[e.Source]
		_, tgtChanged := changed[e.Target]
		switch {
		case srcChanged && !tgtChanged:
			consider(e.Target)
		case tgtChanged && !srcChanged:
			consider(e.Source)
		}
	}
	return peers
}

// MergeIncremental builds the next sidecar to persist from a completed pass.
//
// Incremental mode: prior edges (prev.Edges) whose Source AND Target are
// both in plan.Unchanged are revalidated — carried forward with their
// ORIGINAL InferredAt intact — then combined with newEdges and deduplicated
// via DedupeInferredEdges (highest confidence wins on a collision; on an
// equal-confidence collision the newer InferredAt wins, which is why the
// re-stamp to "now" happens AFTER dedupe, not before — see #50 fix-round:
// re-stamping first made every revalidated edge look artificially newer
// than a genuinely fresh agent edge for the same pair, always winning ties
// backwards). Any surviving edge whose pair had a revalidated candidate
// (whether the revalidated copy won, or a fresh agent edge for the same
// pair won) is then stamped InferredAt = now — the revalidation contract:
// the pair is confirmed true as of this pass either way. Prior edges
// touching any issue analyzed this pass, or absent from the current fetch
// entirely, are dropped: they are not in Unchanged either way, so the
// both-endpoints check excludes them.
//
// Full mode: Edges is DedupeInferredEdges(newEdges) only — prev.Edges is
// ignored entirely, including revalidation.
//
// Both modes: Analyzed is rebuilt from scratch for every issue in the
// current fetch (fingerprint + now + the issue's tracker State), and
// Version/GeneratedAt/Profile are stamped the same way the existing sidecar
// writers do. Recording State is what lets the auto-analyze scheduler
// (cmd/itervox/deps_auto_analyze.go) distinguish "issue completed" (State
// moved to a terminal/backlog state, correctly stops signaling once absent
// from the active-only candidate set) from "issue still active but the
// tracker fetch missed it" (would incorrectly signal forever without this).
func MergeIncremental(prev *Sidecar, plan IncrementalPlan, newEdges []InferredEdge, issues []AnalyzerIssue, profile string, now time.Time) *Sidecar {
	var revalidated []InferredEdge
	if plan.Mode == IncrementalModeIncremental && prev != nil {
		for _, e := range prev.Edges {
			_, srcUnchanged := plan.Unchanged[e.Source]
			_, tgtUnchanged := plan.Unchanged[e.Target]
			if !srcUnchanged || !tgtUnchanged {
				continue
			}
			// Keep the ORIGINAL InferredAt for now — do not re-stamp before
			// dedupe. See the fix-round note below.
			revalidated = append(revalidated, e)
		}
	}

	// Dedupe BEFORE re-stamping revalidated edges to "now" (#50 fix-round —
	// a revalidated edge's InferredAt still reflects when it was ORIGINALLY
	// inferred at this point, so DedupeInferredEdges's "fresher InferredAt
	// wins an equal-confidence tie" rule correctly prefers a genuinely fresh
	// agent edge (stamped during THIS pass by RunAgentPass) over a stale
	// revalidated one. The previous order re-stamped every revalidated edge
	// to the post-pass "now" first, which made it look newer than a truly
	// fresh agent edge on any equal-confidence collision — backwards from
	// the documented tie-break rationale.
	combined := make([]InferredEdge, 0, len(newEdges)+len(revalidated))
	combined = append(combined, filterEdgesToKnownIssues(newEdges, issues)...)
	combined = append(combined, revalidated...)
	deduped := DedupeInferredEdges(combined)

	// The revalidation contract (4b): a pair with a revalidated candidate
	// that survives dedupe is considered re-confirmed as of this pass and
	// carries InferredAt == now, regardless of whether the winning struct
	// was the revalidated copy itself or a fresh agent edge for the same
	// pair (both mean "this pair is still true as of this pass").
	revalidatedKeys := make(map[edgeKey]struct{}, len(revalidated))
	for _, e := range revalidated {
		revalidatedKeys[edgeKey{Source: e.Source, Target: e.Target}] = struct{}{}
	}
	// Preserve deduped's nil-vs-empty shape (nil when there's nothing at
	// all) rather than always allocating, matching MergeIncremental's prior
	// behavior when no edges survive.
	var edges []InferredEdge
	if len(deduped) > 0 {
		edges = make([]InferredEdge, len(deduped))
		copy(edges, deduped)
		for i := range edges {
			if _, ok := revalidatedKeys[edgeKey{Source: edges[i].Source, Target: edges[i].Target}]; ok {
				edges[i].InferredAt = now
			}
		}
	}

	analyzed := make(map[string]AnalyzedIssue, len(issues))
	for _, issue := range issues {
		analyzed[issue.Identifier] = AnalyzedIssue{
			Fingerprint: IssueFingerprint(issue.Title, issue.Description),
			AnalyzedAt:  now,
			State:       issue.State,
		}
	}

	return &Sidecar{
		Version:     SidecarSchemaVersion,
		GeneratedAt: now,
		Profile:     profile,
		// Already deduped above (before the re-stamp); DedupeInferredEdges
		// is idempotent on an already-deduped slice, but calling it again
		// here would be redundant work with no behavior change.
		Edges:    edges,
		Analyzed: analyzed,
	}
}

// filterEdgesToKnownIssues drops analyzer-emitted edges whose Source or
// Target names an issue that is not in the current fetch.
//
// Issue #43's secondary defect: nothing validated the analyzer's identifiers,
// so a hallucinated pair was written to .itervox/dependencies.json and
// reloaded verbatim on every start. The analyzer is an LLM and its output is
// untrusted input; the fetched issue set is the authority on what exists.
//
// The authority is deliberately the whole FETCH, not plan.ToAnalyze. Under
// chunking the analyzer only sees a subset, but an edge pointing at a real
// issue outside this chunk is legitimate and must survive — validating
// against the analyzed subset would silently delete correct cross-chunk
// edges, trading a hallucination bug for a data-loss bug.
//
// Dropping rather than keeping-and-flagging is the right default: an edge to
// a nonexistent issue can never become satisfiable, so retaining it would
// hold its target forever. inferredGatingFor already refuses to gate on an
// unknown source, so this closes the sidecar/dashboard half of the same hole.
func filterEdgesToKnownIssues(edges []InferredEdge, issues []AnalyzerIssue) []InferredEdge {
	if len(edges) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		known[issue.Identifier] = struct{}{}
	}
	out := make([]InferredEdge, 0, len(edges))
	for _, e := range edges {
		_, srcOK := known[e.Source]
		_, tgtOK := known[e.Target]
		if !srcOK || !tgtOK {
			slog.Warn("deps analyzer: dropping inferred edge naming an unknown issue",
				"source", e.Source, "target", e.Target,
				"source_known", srcOK, "target_known", tgtOK)
			continue
		}
		out = append(out, e)
	}
	return out
}
