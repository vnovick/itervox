package depsanalysis

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
