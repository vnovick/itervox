package depsanalysis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeEdges_TrackerWinsOnDuplicate(t *testing.T) {
	tracker := []TrackerEdge{
		{Source: "ENG-1", Target: "ENG-2"},
	}
	inferred := []InferredEdge{
		{Source: "ENG-1", Target: "ENG-2", Evidence: "should be discarded"},
		{Source: "ENG-3", Target: "ENG-4", Evidence: "kept"},
	}
	out := MergeEdges(tracker, inferred)
	require := assert.New(t)
	require.Len(out, 2)
	require.Equal("ENG-1", out[0].Source)
	require.Equal("ENG-2", out[0].Target)
	require.Equal(OriginTracker, out[0].Origin)
	require.Empty(out[0].Evidence, "tracker edges carry no evidence")
	require.Equal(OriginInferred, out[1].Origin)
	require.Equal("kept", out[1].Evidence)
}

func TestMergeEdges_DropsEmptySides(t *testing.T) {
	tracker := []TrackerEdge{
		{Source: "", Target: "ENG-2"},
		{Source: "ENG-3", Target: ""},
		{Source: "ENG-1", Target: "ENG-2"},
	}
	inferred := []InferredEdge{
		{Source: "", Target: "X"},
		{Source: "Y", Target: ""},
	}
	out := MergeEdges(tracker, inferred)
	assert.Len(t, out, 1)
	assert.Equal(t, "ENG-1", out[0].Source)
}

func TestMergeEdges_DedupesWithinSameSource(t *testing.T) {
	inferred := []InferredEdge{
		{Source: "ENG-1", Target: "ENG-2", Evidence: "first"},
		{Source: "ENG-1", Target: "ENG-2", Evidence: "duplicate, drop"},
	}
	out := MergeEdges(nil, inferred)
	assert.Len(t, out, 1)
	assert.Equal(t, "first", out[0].Evidence)
}

func TestMergeEdges_StableSortBySourceThenTarget(t *testing.T) {
	tracker := []TrackerEdge{
		{Source: "B", Target: "C"},
		{Source: "A", Target: "C"},
		{Source: "A", Target: "B"},
	}
	out := MergeEdges(tracker, nil)
	assert.Equal(t, []MergedEdge{
		{Source: "A", Target: "B", Origin: OriginTracker},
		{Source: "A", Target: "C", Origin: OriginTracker},
		{Source: "B", Target: "C", Origin: OriginTracker},
	}, out)
}

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
