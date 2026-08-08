package depsanalysis

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func issuesN(n int) []AnalyzerIssue {
	out := make([]AnalyzerIssue, 0, n)
	for i := range n {
		out = append(out, AnalyzerIssue{
			Identifier: fmt.Sprintf("ENG-%03d", i),
			Title:      "t",
			State:      "Todo",
		})
	}
	return out
}

func TestChunkIssuesSplitsAtBoundary(t *testing.T) {
	got := ChunkIssues(issuesN(10), 4)

	require.Len(t, got, 3)
	assert.Len(t, got[0], 4)
	assert.Len(t, got[1], 4)
	assert.Len(t, got[2], 2)

	// No issue lost, none duplicated.
	seen := map[string]int{}
	for _, c := range got {
		for _, i := range c {
			seen[i.Identifier]++
		}
	}
	assert.Len(t, seen, 10)
	for id, n := range seen {
		assert.Equal(t, 1, n, "issue %s appeared %d times", id, n)
	}
}

func TestChunkIssuesHandlesEdgeSizes(t *testing.T) {
	assert.Empty(t, ChunkIssues(nil, 10))
	assert.Len(t, ChunkIssues(issuesN(3), 10), 1, "fewer issues than the size is one chunk")
	// A non-positive size must not divide by zero or produce infinite chunks.
	assert.Len(t, ChunkIssues(issuesN(3), 0), 1)
}

// Sorting clusters related work so a cross-chunk edge is less likely.
func TestChunkIssuesSortsByStateThenIdentifier(t *testing.T) {
	in := []AnalyzerIssue{
		{Identifier: "ENG-1", State: "Todo"},
		{Identifier: "ENG-9", State: "Backlog"},
		{Identifier: "ENG-3", State: "Todo"},
		{Identifier: "ENG-7", State: "Backlog"},
	}
	got := ChunkIssues(in, 10)
	require.Len(t, got, 1)
	// state-then-identifier: ENG-7(Backlog), ENG-9(Backlog), ENG-1(Todo), ENG-3(Todo)
	// identifier-only (wrong): ENG-1, ENG-3, ENG-7, ENG-9
	assert.Equal(t,
		[]string{"ENG-7", "ENG-9", "ENG-1", "ENG-3"},
		[]string{got[0][0].Identifier, got[0][1].Identifier, got[0][2].Identifier, got[0][3].Identifier})
	assert.Equal(t, "Backlog", got[0][0].State)
	assert.Equal(t, "Backlog", got[0][1].State)
	assert.Equal(t, "Todo", got[0][2].State)
	assert.Equal(t, "Todo", got[0][3].State)
}

// The tracker-edge list is unbounded for the same reason the issue list was.
// Each chunk gets only the edges that touch it.
func TestScopeTrackerEdgesKeepsOnlyEdgesTouchingTheChunk(t *testing.T) {
	chunk := []AnalyzerIssue{{Identifier: "A"}, {Identifier: "B"}}
	edges := []TrackerEdge{
		{Source: "A", Target: "B"}, // both in chunk
		{Source: "A", Target: "Z"}, // source in chunk
		{Source: "Y", Target: "B"}, // target in chunk
		{Source: "Y", Target: "Z"}, // neither — dropped
	}

	got := ScopeTrackerEdges(chunk, edges)

	assert.Len(t, got, 3)
	assert.NotContains(t, got, TrackerEdge{Source: "Y", Target: "Z"})
}
