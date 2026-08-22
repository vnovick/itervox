package depsanalysis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identifiers(issues []AnalyzerIssue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = is.Identifier
	}
	return out
}

func TestPlanIncrementalModeResolution(t *testing.T) {
	issues := []AnalyzerIssue{{Identifier: "A", Title: "A title", Description: "A desc"}}

	t.Run("full requested forces full even with a usable prior sidecar", func(t *testing.T) {
		prev := &Sidecar{
			Profile:  "deps-analyzer",
			Analyzed: map[string]AnalyzedIssue{"A": {Fingerprint: IssueFingerprint("A title", "A desc")}},
		}
		plan := PlanIncremental(issues, prev, "deps-analyzer", "full")
		assert.Equal(t, IncrementalModeFull, plan.Mode)
		assert.Equal(t, issues, plan.ToAnalyze)
		assert.Empty(t, plan.Unchanged)
	})

	t.Run("auto with no prior sidecar falls back to full", func(t *testing.T) {
		plan := PlanIncremental(issues, nil, "deps-analyzer", "auto")
		assert.Equal(t, IncrementalModeFull, plan.Mode)
		assert.Equal(t, issues, plan.ToAnalyze)
	})

	t.Run("auto with an empty Analyzed map falls back to full", func(t *testing.T) {
		prev := &Sidecar{Profile: "deps-analyzer", Analyzed: map[string]AnalyzedIssue{}}
		plan := PlanIncremental(issues, prev, "deps-analyzer", "auto")
		assert.Equal(t, IncrementalModeFull, plan.Mode)
	})

	t.Run("auto with fingerprints and a matching profile resolves incremental", func(t *testing.T) {
		prev := &Sidecar{
			Profile:  "deps-analyzer",
			Analyzed: map[string]AnalyzedIssue{"A": {Fingerprint: IssueFingerprint("A title", "A desc")}},
		}
		plan := PlanIncremental(issues, prev, "deps-analyzer", "auto")
		assert.Equal(t, IncrementalModeIncremental, plan.Mode)
	})

	t.Run("empty string requested mode behaves like auto", func(t *testing.T) {
		prev := &Sidecar{
			Profile:  "deps-analyzer",
			Analyzed: map[string]AnalyzedIssue{"A": {Fingerprint: IssueFingerprint("A title", "A desc")}},
		}
		plan := PlanIncremental(issues, prev, "deps-analyzer", "")
		assert.Equal(t, IncrementalModeIncremental, plan.Mode)
	})

	t.Run("explicit incremental request still requires a matching profile", func(t *testing.T) {
		prev := &Sidecar{
			Profile:  "old-profile",
			Analyzed: map[string]AnalyzedIssue{"A": {Fingerprint: IssueFingerprint("A title", "A desc")}},
		}
		plan := PlanIncremental(issues, prev, "deps-analyzer", "incremental")
		assert.Equal(t, IncrementalModeFull, plan.Mode, "profile change must force a full pass")
	})
}

func TestPlanIncrementalPartition(t *testing.T) {
	prev := &Sidecar{
		Profile: "deps-analyzer",
		Analyzed: map[string]AnalyzedIssue{
			"CHANGED": {Fingerprint: IssueFingerprint("Changed title old", "desc")},
			"SAME":    {Fingerprint: IssueFingerprint("Same title", "same desc")},
		},
	}
	issues := []AnalyzerIssue{
		{Identifier: "CHANGED", Title: "Changed title new", Description: "desc"},
		{Identifier: "NEW", Title: "New issue", Description: "brand new"},
		{Identifier: "SAME", Title: "Same title", Description: "same desc"},
	}

	plan := PlanIncremental(issues, prev, "deps-analyzer", "incremental")

	require.Equal(t, IncrementalModeIncremental, plan.Mode)
	assert.ElementsMatch(t, []string{"CHANGED", "NEW"}, identifiers(plan.ToAnalyze), "changed fingerprint and brand-new issues must be scheduled for analysis")
	assert.Contains(t, plan.Unchanged, "SAME")
	assert.NotContains(t, plan.Unchanged, "CHANGED")
	assert.NotContains(t, plan.Unchanged, "NEW")
}

func TestMergeIncrementalRevalidatesUnchangedPairs(t *testing.T) {
	older := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B", Evidence: "still valid", InferredAt: older, Confidence: 0.7},
		},
		Analyzed: map[string]AnalyzedIssue{
			"A": {Fingerprint: IssueFingerprint("A title", "A desc"), AnalyzedAt: older},
			"B": {Fingerprint: IssueFingerprint("B title", "B desc"), AnalyzedAt: older},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		Unchanged: map[string]struct{}{"A": {}, "B": {}},
	}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc"},
	}

	result := MergeIncremental(prev, plan, nil, issues, "deps-analyzer", now)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "A", result.Edges[0].Source)
	assert.Equal(t, "B", result.Edges[0].Target)
	assert.True(t, now.Equal(result.Edges[0].InferredAt), "an edge whose endpoints are both unchanged must be revalidated (re-stamped) to now")
	assert.False(t, older.Equal(result.Edges[0].InferredAt))
}

// TestMergeIncrementalReplacesAnalyzedIssueEdges asserts the both-endpoints
// rule specifically: an edge with ONE endpoint unchanged and ONE endpoint
// analyzed this pass must be dropped entirely, not merely re-ranked by
// dedupe. No competing newEdges entry is supplied for the same pair, so a
// buggy "either endpoint suffices" implementation would leak the stale edge
// straight into the output — this fixture is built to catch exactly that.
func TestMergeIncrementalReplacesAnalyzedIssueEdges(t *testing.T) {
	older := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B", Evidence: "stale", InferredAt: older, Confidence: 0.9},
		},
		Analyzed: map[string]AnalyzedIssue{
			"A": {Fingerprint: IssueFingerprint("A title", "A desc"), AnalyzedAt: older},
			"B": {Fingerprint: IssueFingerprint("B title", "B desc old"), AnalyzedAt: older},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		ToAnalyze: []AnalyzerIssue{{Identifier: "B", Title: "B title", Description: "B desc new"}},
		Unchanged: map[string]struct{}{"A": {}}, // B is NOT unchanged: it was analyzed this pass
	}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc new"},
	}

	result := MergeIncremental(prev, plan, nil, issues, "deps-analyzer", now)

	for _, e := range result.Edges {
		if e.Source == "A" && e.Target == "B" {
			t.Fatalf("stale edge touching an issue analyzed this pass must be dropped, found: %+v", e)
		}
	}
}

func TestMergeIncrementalDropsGoneIssueEdges(t *testing.T) {
	older := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "GONE", Evidence: "old", InferredAt: older},
		},
		Analyzed: map[string]AnalyzedIssue{
			"A":    {Fingerprint: IssueFingerprint("A title", "A desc"), AnalyzedAt: older},
			"GONE": {Fingerprint: IssueFingerprint("Gone title", "Gone desc"), AnalyzedAt: older},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		Unchanged: map[string]struct{}{"A": {}}, // GONE absent entirely from the current fetch
	}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
	}

	result := MergeIncremental(prev, plan, nil, issues, "deps-analyzer", now)
	assert.Empty(t, result.Edges, "edge referencing an issue absent from the current fetch must be dropped")
}

func TestMergeIncrementalFullModeIgnoresPrevEdges(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B", Evidence: "old", InferredAt: now.Add(-time.Hour)},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeFull,
		ToAnalyze: []AnalyzerIssue{{Identifier: "A"}, {Identifier: "B"}},
		Unchanged: map[string]struct{}{},
	}
	newEdges := []InferredEdge{
		{Source: "C", Target: "D", Evidence: "new", Confidence: 0.5},
	}
	// C and D must appear in the fetch: edges are now validated against the
	// fetched issue set (#43's hallucinated-edge secondary), and this
	// fixture previously emitted an edge between issues it never declared.
	// That incoherence was invisible while nothing checked identifiers; it
	// is not what this test is about, which is that full mode ignores
	// prev.Edges.
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc"},
		{Identifier: "C", Title: "C title", Description: "C desc"},
		{Identifier: "D", Title: "D title", Description: "D desc"},
	}

	result := MergeIncremental(prev, plan, newEdges, issues, "deps-analyzer", now)

	require.Len(t, result.Edges, 1, "full mode must ignore prior edges entirely, including revalidation")
	assert.Equal(t, "C", result.Edges[0].Source)
	assert.Equal(t, "D", result.Edges[0].Target)
}

func TestMergeIncrementalRebuildsAnalyzedMap(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{Version: SidecarSchemaVersion, Profile: "deps-analyzer"}
	plan := IncrementalPlan{Mode: IncrementalModeFull, Unchanged: map[string]struct{}{}}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc"},
	}

	result := MergeIncremental(prev, plan, nil, issues, "deps-analyzer", now)

	require.Len(t, result.Analyzed, 2)
	a, ok := result.Analyzed["A"]
	require.True(t, ok)
	assert.Equal(t, IssueFingerprint("A title", "A desc"), a.Fingerprint)
	assert.True(t, now.Equal(a.AnalyzedAt))
	b, ok := result.Analyzed["B"]
	require.True(t, ok)
	assert.Equal(t, IssueFingerprint("B title", "B desc"), b.Fingerprint)
	assert.True(t, now.Equal(b.AnalyzedAt))

	assert.Equal(t, SidecarSchemaVersion, result.Version)
	assert.True(t, now.Equal(result.GeneratedAt))
	assert.Equal(t, "deps-analyzer", result.Profile)
}

// TestMergeIncrementalRecordsIssueState is the fix-round regression test for
// the scheduler scope-mismatch bug: MergeIncremental must record each
// fetched issue's tracker State into the rebuilt Analyzed map, so the
// auto-analyze scheduler can later tell "issue completed" (State recorded as
// terminal/backlog) apart from "issue still active" when the entry goes
// absent from the active-only candidate set.
func TestMergeIncrementalRecordsIssueState(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{Version: SidecarSchemaVersion, Profile: "deps-analyzer"}
	plan := IncrementalPlan{Mode: IncrementalModeFull, Unchanged: map[string]struct{}{}}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc", State: "In Progress"},
		{Identifier: "B", Title: "B title", Description: "B desc", State: "Done"},
		{Identifier: "C", Title: "C title", Description: "C desc"}, // no state
	}

	result := MergeIncremental(prev, plan, nil, issues, "deps-analyzer", now)

	require.Len(t, result.Analyzed, 3)
	assert.Equal(t, "In Progress", result.Analyzed["A"].State)
	assert.Equal(t, "Done", result.Analyzed["B"].State)
	assert.Equal(t, "", result.Analyzed["C"].State)
}

// TestMergeIncrementalTiePrefersFreshOverRevalidated (#50 fix-round item 2)
// pins the equal-confidence collision case: a prior edge is revalidated
// (both endpoints Unchanged) AND the agent independently emits the exact
// same pair this pass (possible — convertParsedEdges does not restrict
// emitted edges to the chunk's own issue set) at the SAME confidence. Before
// this fix, MergeIncremental re-stamped the revalidated copy's InferredAt to
// the post-pass "now" before deduping, which made it look newer than the
// genuinely fresh agent edge (stamped earlier, during the pass, by
// RunAgentPass) and always won the tie — backwards from
// DedupeInferredEdges's documented "fresher evidence wins ties" rule. The
// fix dedupes on ORIGINAL stamps first, so the fresh edge's evidence must
// survive; the surviving edge must still carry InferredAt == now per the
// revalidation contract (the pair WAS re-confirmed this pass, one way or
// the other).
func TestMergeIncrementalTiePrefersFreshOverRevalidated(t *testing.T) {
	older := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B", Evidence: "stale revalidated copy", InferredAt: older, Confidence: 0.6},
		},
		Analyzed: map[string]AnalyzedIssue{
			"A": {Fingerprint: IssueFingerprint("A title", "A desc"), AnalyzedAt: older},
			"B": {Fingerprint: IssueFingerprint("B title", "B desc"), AnalyzedAt: older},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		Unchanged: map[string]struct{}{"A": {}, "B": {}},
	}
	// The agent's freshly-parsed edge for this pass — RunAgentPass stamps
	// InferredAt at parse time, strictly before the caller's post-pass "now".
	freshStamp := now.Add(-time.Minute)
	newEdges := []InferredEdge{
		{Source: "A", Target: "B", Evidence: "fresh agent re-confirmation", InferredAt: freshStamp, Confidence: 0.6},
	}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc"},
	}

	result := MergeIncremental(prev, plan, newEdges, issues, "deps-analyzer", now)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "fresh agent re-confirmation", result.Edges[0].Evidence,
		"equal confidence: the genuinely fresher agent edge must win over the stale revalidated copy")
	assert.True(t, now.Equal(result.Edges[0].InferredAt),
		"the surviving edge for a revalidated pair must still carry InferredAt == now (the revalidation contract)")
}

// TestMergeIncrementalDedupeInteraction covers the requested dedupe-interaction
// case: a revalidated prior edge and a freshly analyzed edge collide on the
// same (source, target) pair. DedupeInferredEdges' existing highest-confidence
// rule must decide the winner exactly as it would for any other collision.
func TestMergeIncrementalDedupeInteraction(t *testing.T) {
	older := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prev := &Sidecar{
		Version: SidecarSchemaVersion,
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B", Evidence: "revalidated", InferredAt: older, Confidence: 0.3},
		},
		Analyzed: map[string]AnalyzedIssue{
			"A": {Fingerprint: IssueFingerprint("A title", "A desc"), AnalyzedAt: older},
			"B": {Fingerprint: IssueFingerprint("B title", "B desc"), AnalyzedAt: older},
		},
	}
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		Unchanged: map[string]struct{}{"A": {}, "B": {}},
	}
	newEdges := []InferredEdge{
		{Source: "A", Target: "B", Evidence: "fresh", Confidence: 0.9},
	}
	issues := []AnalyzerIssue{
		{Identifier: "A", Title: "A title", Description: "A desc"},
		{Identifier: "B", Title: "B title", Description: "B desc"},
	}

	result := MergeIncremental(prev, plan, newEdges, issues, "deps-analyzer", now)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "fresh", result.Edges[0].Evidence, "the higher-confidence new edge must win over the revalidated old copy")
	assert.Equal(t, 0.9, result.Edges[0].Confidence)
}

// TestPlanIncrementalIncludesPriorEdgePeersOfChangedIssues pins the fix for
// the incremental pass eroding its own graph.
//
// MergeIncremental carries a prior edge forward only when BOTH endpoints are
// unchanged, so an edge spanning a changed and an unchanged issue is dropped
// and must be re-derived by the fresh agent pass. But the pass only ever
// sees plan.ToAnalyze — so if the unchanged endpoint is not in that set, the
// analyzer cannot emit the edge even when it still holds, and it is lost
// permanently. Auto-analysis is on by default, incremental is the default
// mode, and nothing schedules a periodic full pass, so repeated runs erode
// the graph toward changed-to-changed edges only. Silently dropped edges
// stop gating dispatch.
//
// The fix keeps the unchanged peer in Unchanged (so its OWN both-unchanged
// edges still revalidate) while also handing it to the analyzer as context,
// so a dropped edge is a real analyzer judgement rather than an artifact of
// what the prompt happened to contain.
func TestPlanIncrementalIncludesPriorEdgePeersOfChangedIssues(t *testing.T) {
	prev := &Sidecar{
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "CHANGED", Target: "PEER", Evidence: "spans the boundary"},
			{Source: "SAME", Target: "OTHER", Evidence: "wholly unchanged"},
		},
		Analyzed: map[string]AnalyzedIssue{
			"CHANGED": {Fingerprint: IssueFingerprint("old title", "desc")},
			"PEER":    {Fingerprint: IssueFingerprint("peer title", "peer desc")},
			"SAME":    {Fingerprint: IssueFingerprint("same title", "same desc")},
			"OTHER":   {Fingerprint: IssueFingerprint("other title", "other desc")},
		},
	}
	issues := []AnalyzerIssue{
		{Identifier: "CHANGED", Title: "new title", Description: "desc"},
		{Identifier: "PEER", Title: "peer title", Description: "peer desc"},
		{Identifier: "SAME", Title: "same title", Description: "same desc"},
		{Identifier: "OTHER", Title: "other title", Description: "other desc"},
	}

	plan := PlanIncremental(issues, prev, "deps-analyzer", "incremental")

	require.Equal(t, IncrementalModeIncremental, plan.Mode)
	assert.ElementsMatch(t, []string{"CHANGED", "PEER"}, identifiers(plan.ToAnalyze),
		"the unchanged peer of a boundary-spanning prior edge must reach the analyzer")

	// The peer stays in Unchanged so MergeIncremental still revalidates any
	// edge of its own whose endpoints both went untouched.
	assert.Contains(t, plan.Unchanged, "PEER")
	assert.Contains(t, plan.Unchanged, "SAME")
	assert.Contains(t, plan.Unchanged, "OTHER")
	assert.NotContains(t, plan.Unchanged, "CHANGED")
}

// TestPlanIncrementalDoesNotPullInUnrelatedUnchangedIssues bounds the
// expansion above: only the peers of prior edges that actually touch a
// changed issue are added. Without this, "include context" degrades into a
// full pass on every run and the incremental mode stops paying for itself.
func TestPlanIncrementalDoesNotPullInUnrelatedUnchangedIssues(t *testing.T) {
	prev := &Sidecar{
		Profile: "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "SAME", Target: "OTHER", Evidence: "nowhere near the change"},
		},
		Analyzed: map[string]AnalyzedIssue{
			"CHANGED": {Fingerprint: IssueFingerprint("old title", "desc")},
			"SAME":    {Fingerprint: IssueFingerprint("same title", "same desc")},
			"OTHER":   {Fingerprint: IssueFingerprint("other title", "other desc")},
		},
	}
	issues := []AnalyzerIssue{
		{Identifier: "CHANGED", Title: "new title", Description: "desc"},
		{Identifier: "SAME", Title: "same title", Description: "same desc"},
		{Identifier: "OTHER", Title: "other title", Description: "other desc"},
	}

	plan := PlanIncremental(issues, prev, "deps-analyzer", "incremental")

	assert.ElementsMatch(t, []string{"CHANGED"}, identifiers(plan.ToAnalyze),
		"an unchanged pair with no edge to the changed issue must stay out of the pass")
}

// TestMergeIncrementalDropsEdgesToUnknownIssues closes issue #43's secondary
// defect: nothing validated that analyzer-emitted source/target identifiers
// correspond to real issues, so a hallucinated pair landed in
// .itervox/dependencies.json and was reloaded verbatim on every start.
//
// The analyzer is an LLM. Its output is untrusted input, and the fetched
// issue set is the authority on which identifiers exist. Downstream,
// inferredGatingFor already refuses to gate on an unknown source, so a
// hallucinated edge could not block dispatch — but it still polluted the
// sidecar, the dashboard's dependency graph, and every later incremental
// pass that revalidated against it.
func TestMergeIncrementalDropsEdgesToUnknownIssues(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issues := []AnalyzerIssue{
		{Identifier: "ENG-1", Title: "a", Description: "a"},
		{Identifier: "ENG-2", Title: "b", Description: "b"},
	}
	plan := IncrementalPlan{Mode: IncrementalModeFull, ToAnalyze: issues, Unchanged: map[string]struct{}{}}

	newEdges := []InferredEdge{
		{Source: "ENG-1", Target: "ENG-2", Evidence: "real", Confidence: 0.9},
		{Source: "GHOST-999", Target: "ENG-2", Evidence: "hallucinated source", Confidence: 0.95},
		{Source: "ENG-1", Target: "PHANTOM-1", Evidence: "hallucinated target", Confidence: 0.95},
		{Source: "NOPE-1", Target: "NOPE-2", Evidence: "both invented", Confidence: 0.99},
	}

	sc := MergeIncremental(nil, plan, newEdges, issues, "deps-analyzer", now)

	require.Len(t, sc.Edges, 1, "only the edge between two real issues may survive")
	assert.Equal(t, "ENG-1", sc.Edges[0].Source)
	assert.Equal(t, "ENG-2", sc.Edges[0].Target)
}

// TestMergeIncrementalKeepsEdgesAcrossTheAnalyzedBoundary guards the filter
// from over-reaching. Under chunking the analyzer only sees plan.ToAnalyze,
// but a legitimate edge may point at an issue that is real and fetched yet
// not in this chunk — those must survive. The authority is the FETCH, not
// the analyzed subset.
func TestMergeIncrementalKeepsEdgesAcrossTheAnalyzedBoundary(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issues := []AnalyzerIssue{
		{Identifier: "ENG-1", Title: "a", Description: "a"},
		{Identifier: "ENG-2", Title: "b", Description: "b"},
		{Identifier: "ENG-3", Title: "c", Description: "c"},
	}
	// Only ENG-1 was analyzed this pass; ENG-3 is fetched but out of chunk.
	plan := IncrementalPlan{
		Mode:      IncrementalModeIncremental,
		ToAnalyze: []AnalyzerIssue{issues[0]},
		Unchanged: map[string]struct{}{"ENG-2": {}, "ENG-3": {}},
	}
	newEdges := []InferredEdge{{Source: "ENG-1", Target: "ENG-3", Evidence: "cross-chunk", Confidence: 0.8}}

	sc := MergeIncremental(nil, plan, newEdges, issues, "deps-analyzer", now)

	require.Len(t, sc.Edges, 1, "an edge to a fetched-but-unanalyzed issue is legitimate")
	assert.Equal(t, "ENG-3", sc.Edges[0].Target)
}
