package depsanalysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertParsedEdgesDropsSelfEdges pins issue #63 / #43's secondary defect
// in the form that can be rejected without false positives.
//
// Identifier validation (filterEdgesToKnownIssues) proves both endpoints exist,
// but nothing checked the claimed RELATIONSHIP was possible. A self-edge is a
// single-node SCC, which the tick graph reports as a DependencyCycle, and cycle
// members stay blocked with nothing to auto-release them — so one hallucinated
// "ENG-7 blocks ENG-7" parks a real issue indefinitely.
func TestConvertParsedEdgesDropsSelfEdges(t *testing.T) {
	got := convertParsedEdges([]parsedAnalyzerEdge{
		{Source: "ENG-7", Target: "ENG-7", Evidence: "invented", Confidence: json.RawMessage(`0.95`)},
		{Source: "ENG-1", Target: "ENG-2", Evidence: "real", Confidence: json.RawMessage(`0.9`)},
	})

	require.Len(t, got, 1, "the self-edge must be dropped and the genuine edge kept")
	assert.Equal(t, "ENG-1", got[0].Source)
	assert.Equal(t, "ENG-2", got[0].Target)
}

// TestConvertParsedEdgesDropsSelfEdgesAfterTrimming pins that whitespace cannot
// smuggle a self-edge past the guard: source and target are trimmed before the
// comparison, so " ENG-7 " and "ENG-7" are the same issue.
func TestConvertParsedEdgesDropsSelfEdgesAfterTrimming(t *testing.T) {
	got := convertParsedEdges([]parsedAnalyzerEdge{
		{Source: "  ENG-7  ", Target: "ENG-7", Confidence: json.RawMessage(`0.9`)},
	})

	assert.Empty(t, got, "trimming must happen before the self-edge comparison, not after")
}

// TestConvertParsedEdgesKeepsHighConfidenceCrossIssueEdges is the control: the
// guard must reject ONLY self-reference. A fabricated relationship between two
// different real issues is deliberately still accepted here — it is soft-gated
// downstream rather than dropped, because no check distinguishes it from a
// genuine one without also discarding genuine edges.
func TestConvertParsedEdgesKeepsHighConfidenceCrossIssueEdges(t *testing.T) {
	got := convertParsedEdges([]parsedAnalyzerEdge{
		{Source: "ENG-1", Target: "ENG-2", Confidence: json.RawMessage(`0.99`)},
		{Source: "ENG-2", Target: "ENG-3", Confidence: json.RawMessage(`0.5`)},
	})

	assert.Len(t, got, 2, "only self-reference is rejected at this boundary")
}

// TestLoadSidecarDropsPreexistingSelfEdges pins that a sidecar written before
// the analyzer-boundary guard is repaired on load. Guarding only new analyzer
// output would leave an already-persisted self-edge blocking its issue until
// the next full analysis — and a blocked issue is exactly what stops that
// analysis from mattering.
func TestLoadSidecarDropsPreexistingSelfEdges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")

	body, err := json.Marshal(Sidecar{
		Version: SidecarSchemaVersion,
		Edges: []InferredEdge{
			{Source: "ENG-7", Target: "ENG-7", Confidence: 0.9},
			{Source: "ENG-1", Target: "ENG-2", Confidence: 0.8},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, sc)

	require.Len(t, sc.Edges, 1, "the persisted self-edge must not survive the load")
	assert.Equal(t, "ENG-1", sc.Edges[0].Source)
	assert.Equal(t, "ENG-2", sc.Edges[0].Target)
	assert.InDelta(t, 0.8, sc.Edges[0].Confidence, 0.0001,
		"surviving edges must still be confidence-clamped — the filter must not skip that")
}

func TestIsSelfEdge(t *testing.T) {
	assert.True(t, isSelfEdge("ENG-1", "ENG-1"))
	assert.False(t, isSelfEdge("ENG-1", "ENG-2"))
	assert.False(t, isSelfEdge("", ""),
		"two empty identifiers are not a self-edge — empties are rejected earlier as missing")
}
