package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
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
	nodes, edges := dependencyGraphRows(state, nil)
	require.Len(t, edges, 1)
	assert.Equal(t, "tracker", edges[0].Origin, "tracker-declared edges must be tagged tracker")
	assert.True(t, edges[0].Resolved, "ENG-1 is Done so the edge is resolved")
	assert.Len(t, nodes, 2)
}

func TestDependencyGraphRows_AddsInferredEdgesFromSidecar(t *testing.T) {
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
	}
	sidecar := &depsanalysis.Sidecar{
		Version: depsanalysis.SidecarSchemaVersion,
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-5", Target: "ENG-6", Evidence: "title says depends on ENG-5"},
			{Source: "ENG-1", Target: "ENG-2", Evidence: "should be dropped — duplicates tracker edge"},
		},
	}
	nodes, edges := dependencyGraphRows(state, sidecar)
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

func TestDependencyGraphRows_EmptyAuditAndSidecarReturnsNil(t *testing.T) {
	nodes, edges := dependencyGraphRows(orchestrator.State{}, nil)
	assert.Nil(t, nodes)
	assert.Nil(t, edges)
}

func TestDependencyGraphRows_SidecarOnlyEmitsInferredEdges(t *testing.T) {
	sidecar := &depsanalysis.Sidecar{
		Version: depsanalysis.SidecarSchemaVersion,
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-5", Target: "ENG-6", Evidence: "only inferred"},
		},
	}
	nodes, edges := dependencyGraphRows(orchestrator.State{}, sidecar)
	require.Len(t, edges, 1)
	assert.Equal(t, "inferred", edges[0].Origin)
	assert.Len(t, nodes, 2)
}
