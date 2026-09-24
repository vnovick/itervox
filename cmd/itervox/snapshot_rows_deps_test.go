package main

import (
	"time"

	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

func TestDependencyGraphRows_EmitsTrackerEdgesWithOriginTag(t *testing.T) {
	s := func(v string) *string { return &v }
	state := orchestrator.State{
		TerminalStates: []string{"Done"},
		DependencyAudit: map[string]*orchestrator.DependencyAuditEntry{
			"ENG-2": {
				Identifier: "ENG-2",
				IssueState: "Todo",
				BlockedBy: []domain.BlockerRef{
					{Identifier: s("ENG-1"), State: s("Done")},
				},
			},
		},
	}
	nodes, edges := dependencyGraphRows(state)
	require.Len(t, edges, 1)
	assert.Equal(t, "tracker", edges[0].Origin, "tracker-declared edges must be tagged tracker")
	assert.True(t, edges[0].Resolved, "ENG-1 is Done so the edge is resolved")
	assert.Len(t, nodes, 2)
}

// TestDependencyGraphRows_AddsInferredEdgesFromState pins the unified-
// dependency-graph Task 7 behaviour: inferred edges come solely from
// State.InferredDeps (keyed by target identifier), not a sidecar. This
// replaces the pre-Task-7 sidecar-derived test of the same shape.
func TestDependencyGraphRows_AddsInferredEdgesFromState(t *testing.T) {
	s := func(v string) *string { return &v }
	state := orchestrator.State{
		DependencyAudit: map[string]*orchestrator.DependencyAuditEntry{
			"ENG-2": {
				Identifier: "ENG-2",
				IssueState: "Todo",
				BlockedBy: []domain.BlockerRef{
					{Identifier: s("ENG-1"), State: s("Todo")},
				},
			},
		},
		InferredDeps: map[string][]orchestrator.InferredDepEntry{
			"ENG-6": {
				{Source: "ENG-5", Evidence: "title says depends on ENG-5", SourceKnown: true},
			},
			// Duplicates the tracker edge (ENG-1 -> ENG-2) and must be
			// dropped in favor of the tracker-derived row.
			"ENG-2": {
				{Source: "ENG-1", Evidence: "should be dropped — duplicates tracker edge"},
			},
		},
	}
	nodes, edges := dependencyGraphRows(state)
	// 1 tracker edge + 1 surviving inferred edge (the duplicate dropped).
	require.Len(t, edges, 2)

	originCounts := map[string]int{}
	for _, e := range edges {
		originCounts[e.Origin]++
	}
	assert.Equal(t, 1, originCounts["tracker"])
	assert.Equal(t, 1, originCounts["inferred"])

	// Inferred edge must carry its evidence.
	for _, e := range edges {
		if e.Origin == "inferred" {
			assert.Equal(t, "title says depends on ENG-5", e.Evidence)
			assert.Equal(t, "ENG-5", e.SourceIdentifier)
			assert.Equal(t, "ENG-6", e.TargetIdentifier)
			assert.True(t, e.SourceKnown)
			assert.Equal(t, "inferred", e.Origin)
		}
	}

	// Inferred-edge endpoints must show up as nodes.
	identifiers := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		identifiers[n.Identifier] = true
	}
	assert.True(t, identifiers["ENG-5"])
	assert.True(t, identifiers["ENG-6"])
}

func TestDependencyGraphRows_EmptyAuditAndInferredReturnsNil(t *testing.T) {
	nodes, edges := dependencyGraphRows(orchestrator.State{})
	assert.Nil(t, nodes)
	assert.Nil(t, edges)
}

// TestDependencyCycleRows locks the state → row mapping for
// critical-path-ordering Task 5: order is preserved from
// state.DependencyCycles, fields are carried faithfully, and each row's
// Members slice is an independent copy — mutating a returned row must never
// reach back into orchestrator.State.
func TestDependencyCycleRows(t *testing.T) {
	now := time.Now()
	state := orchestrator.State{
		DependencyCycles: []orchestrator.DependencyCycle{
			{Members: []string{"ENG-1", "ENG-2"}, Kind: "tracker", DetectedAt: now},
			{Members: []string{"ENG-3", "ENG-4"}, Kind: "inferred", DetectedAt: now.Add(time.Hour)},
		},
	}

	rows := dependencyCycleRows(state)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, rows[0].Members)
	assert.Equal(t, "tracker", rows[0].Kind)
	assert.True(t, now.Equal(rows[0].DetectedAt))
	assert.Equal(t, []string{"ENG-3", "ENG-4"}, rows[1].Members)
	assert.Equal(t, "inferred", rows[1].Kind)

	rows[0].Members[0] = "MUTATED"
	assert.Equal(t, "ENG-1", state.DependencyCycles[0].Members[0],
		"dependencyCycleRows must copy Members independently, not share the state's slice")
}

// TestDependencyCycleRows_EmptyStateReturnsNil matches the nil-on-empty
// convention already used by dependencyGraphRows / dependencyAuditRows.
func TestDependencyCycleRows_EmptyStateReturnsNil(t *testing.T) {
	assert.Nil(t, dependencyCycleRows(orchestrator.State{}))
}

// TestDependencyAttentionRows locks the state → row mapping for
// critical-path-ordering Task 5: order is preserved from
// state.DependencyAttention, fields are carried faithfully, and each row's
// Blockers slice is an independent copy.
func TestDependencyAttentionRows(t *testing.T) {
	now := time.Now()
	state := orchestrator.State{
		DependencyAttention: []orchestrator.DependencyAttentionEntry{
			{Identifier: "ENG-1", Blockers: []string{"ENG-0"}, BlockedSince: now, Kind: "cycle"},
			{Identifier: "ENG-2", Blockers: []string{"ENG-5", "ENG-6"}, BlockedSince: now.Add(-time.Hour), Kind: "stale_blocker"},
		},
	}

	rows := dependencyAttentionRows(state)
	require.Len(t, rows, 2)
	assert.Equal(t, "ENG-1", rows[0].Identifier)
	assert.Equal(t, []string{"ENG-0"}, rows[0].Blockers)
	assert.Equal(t, "cycle", rows[0].Kind)
	assert.True(t, now.Equal(rows[0].BlockedSince))
	assert.Equal(t, "ENG-2", rows[1].Identifier)
	assert.Equal(t, []string{"ENG-5", "ENG-6"}, rows[1].Blockers)
	assert.Equal(t, "stale_blocker", rows[1].Kind)

	rows[1].Blockers[0] = "MUTATED"
	assert.Equal(t, "ENG-5", state.DependencyAttention[1].Blockers[0],
		"dependencyAttentionRows must copy Blockers independently, not share the state's slice")
}

// TestDependencyAttentionRows_EmptyStateReturnsNil matches the nil-on-empty
// convention already used elsewhere in this file.
func TestDependencyAttentionRows_EmptyStateReturnsNil(t *testing.T) {
	assert.Nil(t, dependencyAttentionRows(orchestrator.State{}))
}

func TestDependencyGraphRows_InferredOnlyEmitsInferredEdges(t *testing.T) {
	state := orchestrator.State{
		InferredDeps: map[string][]orchestrator.InferredDepEntry{
			"ENG-6": {
				{Source: "ENG-5", Evidence: "only inferred"},
			},
		},
	}
	nodes, edges := dependencyGraphRows(state)
	require.Len(t, edges, 1)
	assert.Equal(t, "inferred", edges[0].Origin)
	assert.Len(t, nodes, 2)
}

// TestDependencyGraphRowsInferredProvenance is the Task 7 acceptance test:
// one tracker audit entry plus two InferredDeps entries for different
// targets (one gating, one stale non-gating). Tracker edge rows must carry
// Origin "tracker"; inferred rows must carry Origin "inferred" with
// Confidence/Stale/Gating/Overridden faithfully copied from the
// InferredDepEntry, and both inferred endpoints must appear in the node set.
func TestDependencyGraphRowsInferredProvenance(t *testing.T) {
	s := func(v string) *string { return &v }
	now := time.Now()
	state := orchestrator.State{
		TerminalStates: []string{"Done"},
		DependencyAudit: map[string]*orchestrator.DependencyAuditEntry{
			"ENG-9": {
				Identifier: "ENG-9",
				IssueState: "Todo",
				BlockedBy: []domain.BlockerRef{
					{Identifier: s("ENG-8"), State: s("In Progress")},
				},
			},
		},
		InferredDeps: map[string][]orchestrator.InferredDepEntry{
			// Gating inferred edge: high confidence, fresh, not overridden,
			// source known and non-terminal.
			"ENG-11": {
				{
					Source:      "ENG-10",
					Evidence:    "ENG-11 mentions ENG-10 as a prerequisite",
					Confidence:  0.92,
					InferredAt:  now,
					SourceKnown: true,
					Gating:      true,
				},
			},
			// Stale, non-gating inferred edge.
			"ENG-13": {
				{
					Source:      "ENG-12",
					Evidence:    "old inference, past the staleness window",
					Confidence:  0.81,
					InferredAt:  now.Add(-72 * time.Hour),
					Stale:       true,
					SourceKnown: true,
					Gating:      false,
				},
			},
		},
	}

	nodes, edges := dependencyGraphRows(state)
	require.Len(t, edges, 3, "1 tracker edge + 2 inferred edges")

	var trackerEdge, gatingEdge, staleEdge *server.DependencyGraphEdgeRow
	for i := range edges {
		e := &edges[i]
		switch {
		case e.SourceIdentifier == "ENG-8" && e.TargetIdentifier == "ENG-9":
			trackerEdge = e
		case e.SourceIdentifier == "ENG-10" && e.TargetIdentifier == "ENG-11":
			gatingEdge = e
		case e.SourceIdentifier == "ENG-12" && e.TargetIdentifier == "ENG-13":
			staleEdge = e
		}
	}
	require.NotNil(t, trackerEdge, "tracker edge must be present")
	require.NotNil(t, gatingEdge, "gating inferred edge must be present")
	require.NotNil(t, staleEdge, "stale inferred edge must be present")

	assert.Equal(t, "tracker", trackerEdge.Origin)
	assert.True(t, trackerEdge.Gating, "unresolved tracker blocker must gate")

	assert.Equal(t, "inferred", gatingEdge.Origin)
	assert.Equal(t, 0.92, gatingEdge.Confidence)
	assert.False(t, gatingEdge.Stale)
	assert.False(t, gatingEdge.Overridden)
	assert.True(t, gatingEdge.Gating)

	assert.Equal(t, "inferred", staleEdge.Origin)
	assert.Equal(t, 0.81, staleEdge.Confidence)
	assert.True(t, staleEdge.Stale)
	assert.False(t, staleEdge.Gating)

	identifiers := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		identifiers[n.Identifier] = true
	}
	for _, id := range []string{"ENG-8", "ENG-9", "ENG-10", "ENG-11", "ENG-12", "ENG-13"} {
		assert.True(t, identifiers[id], "expected node %s in graph", id)
	}
}
