package depsanalysis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedupeInferredEdgesCollapsesDuplicatePairs(t *testing.T) {
	got := DedupeInferredEdges([]InferredEdge{
		{Source: "A", Target: "B", Evidence: "first"},
		{Source: "A", Target: "B", Evidence: "second"},
		{Source: "B", Target: "C"},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "first", got[0].Evidence, "equal (zero) confidence ties keep the first occurrence")
}

// #50 dedupe tie — on EQUAL confidence, the newer InferredAt must win
// (fresher evidence wins ties), regardless of arrival order. Before this
// fix, an equal-confidence tie always kept the first occurrence, which could
// keep stale evidence over a fresher re-confirmation from a later chunk or
// pass.
func TestDedupeInferredEdgesTieBreakPrefersNewerInferredAt(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)

	// Older arrives first, newer second — order alone must not decide.
	got := DedupeInferredEdges([]InferredEdge{
		{Source: "A", Target: "B", Evidence: "stale", Confidence: 0.6, InferredAt: older},
		{Source: "A", Target: "B", Evidence: "fresh", Confidence: 0.6, InferredAt: newer},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "fresh", got[0].Evidence, "equal confidence must prefer the NEWER InferredAt")
	assert.True(t, got[0].InferredAt.Equal(newer))

	// Newer arrives first this time — the result must be the same fresh
	// edge, proving the tie-break isn't accidentally order-dependent.
	got2 := DedupeInferredEdges([]InferredEdge{
		{Source: "A", Target: "B", Evidence: "fresh", Confidence: 0.6, InferredAt: newer},
		{Source: "A", Target: "B", Evidence: "stale", Confidence: 0.6, InferredAt: older},
	})
	require.Len(t, got2, 1)
	assert.Equal(t, "fresh", got2[0].Evidence)
}

func TestDedupeKeepsHighestConfidence(t *testing.T) {
	got := DedupeInferredEdges([]InferredEdge{
		{Source: "A", Target: "B", Evidence: "low", Confidence: 0.3},
		{Source: "A", Target: "B", Evidence: "high", Confidence: 0.9},
		{Source: "A", Target: "B", Evidence: "middle", Confidence: 0.6},
		{Source: "B", Target: "C", Evidence: "solo", Confidence: 0.5},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "A", got[0].Source)
	assert.Equal(t, "B", got[0].Target)
	assert.Equal(t, "high", got[0].Evidence, "highest-confidence duplicate wins")
	assert.Equal(t, 0.9, got[0].Confidence)
	assert.Equal(t, "solo", got[1].Evidence)
}
