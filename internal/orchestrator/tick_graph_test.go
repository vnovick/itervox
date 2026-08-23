package orchestrator

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
)

func mustEdgeKinds(t *testing.T, g TickGraph, from, to string) map[string]struct{} {
	t.Helper()
	kinds, ok := g.EdgeKinds[from+"->"+to]
	require.True(t, ok, "expected edge %s->%s", from, to)
	return kinds
}

func TestBuildTickGraphEdgeSelection(t *testing.T) {
	state := State{TerminalStates: []string{"Done"}}

	candidates := []domain.Issue{
		{Identifier: "A", State: "Todo"},
		// B is blocked by A (unresolved: A is not terminal) -> edge A->B kept.
		{Identifier: "B", State: "Todo", BlockedBy: []domain.BlockerRef{
			{Identifier: strPtr("A")},
		}},
		// T is a candidate node in a terminal tracker state.
		{Identifier: "T", State: "Done"},
		// C is blocked by T, and T's blocker-state snapshot is terminal ->
		// resolved -> no edge, even though T is itself a candidate node.
		{Identifier: "C", State: "Todo", BlockedBy: []domain.BlockerRef{
			{Identifier: strPtr("T"), State: strPtr("Done")},
		}},
		// D is blocked by an identifier outside the candidate set -> dropped
		// for graph purposes even though unresolved.
		{Identifier: "D", State: "Todo", BlockedBy: []domain.BlockerRef{
			{Identifier: strPtr("OUTSIDE")},
		}},
		// E has a nil-identifier blocker entry -> must not panic, no edge.
		{Identifier: "E", State: "Todo", BlockedBy: []domain.BlockerRef{
			{Identifier: nil},
		}},
		// F<-A duplicate: both tracker AND inferred assert A->F.
		{Identifier: "F", State: "Todo", BlockedBy: []domain.BlockerRef{
			{Identifier: strPtr("A")},
		}},
	}

	inferred := map[string][]InferredDepEntry{
		// F: gating inferred edge from A duplicates the tracker edge A->F.
		"F": {{Source: "A", Gating: true}},
		// B: non-gating inferred edge from C must NOT produce an edge.
		"B": {{Source: "C", Gating: false}},
		// C: gating inferred edge from a source outside the candidate set
		// must be dropped.
		"C": {{Source: "GHOST", Gating: true}},
	}

	g := BuildTickGraph(candidates, inferred, state)

	require.ElementsMatch(t, []string{"A", "B", "T", "C", "D", "E", "F"}, keysOf(g.Nodes))

	t.Run("unresolved tracker edge kept", func(t *testing.T) {
		require.Equal(t, []string{"B", "F"}, g.Out["A"])
		kinds := mustEdgeKinds(t, g, "A", "B")
		require.Equal(t, map[string]struct{}{edgeKindTracker: {}}, kinds)
	})

	t.Run("resolved tracker edge dropped", func(t *testing.T) {
		_, ok := g.EdgeKinds["T->C"]
		require.False(t, ok)
		require.NotContains(t, g.Out, "T")
	})

	t.Run("out-of-set tracker blocker dropped", func(t *testing.T) {
		_, ok := g.EdgeKinds["OUTSIDE->D"]
		require.False(t, ok)
	})

	t.Run("nil-identifier blocker does not panic and produces no edge", func(t *testing.T) {
		require.Empty(t, g.Out["E"])
	})

	t.Run("non-gating inferred edge dropped", func(t *testing.T) {
		_, ok := g.EdgeKinds["C->B"]
		require.False(t, ok)
	})

	t.Run("out-of-set inferred source dropped", func(t *testing.T) {
		_, ok := g.EdgeKinds["GHOST->C"]
		require.False(t, ok)
	})

	t.Run("tracker+inferred duplicate keeps both kinds", func(t *testing.T) {
		kinds := mustEdgeKinds(t, g, "A", "F")
		require.Equal(t, map[string]struct{}{edgeKindTracker: {}, edgeKindInferred: {}}, kinds)
	})

	t.Run("out lists are sorted", func(t *testing.T) {
		require.True(t, sortedStrings(g.Out["A"]))
	})
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// buildGraph is a small literal-TickGraph constructor for metrics/cycle
// tests that don't need to exercise BuildTickGraph's edge-selection logic.
// Every edge is tagged "tracker" unless overridden via kinds.
func buildGraph(nodes []string, edges [][2]string, kindOverrides map[[2]string]string) TickGraph {
	g := TickGraph{
		Nodes:     make(map[string]struct{}, len(nodes)),
		Out:       make(map[string][]string),
		EdgeKinds: make(map[string]map[string]struct{}),
	}
	for _, n := range nodes {
		g.Nodes[n] = struct{}{}
	}
	for _, e := range edges {
		from, to := e[0], e[1]
		kind := edgeKindTracker
		if kindOverrides != nil {
			if k, ok := kindOverrides[e]; ok {
				kind = k
			}
		}
		key := from + "->" + to
		kinds, ok := g.EdgeKinds[key]
		if !ok {
			kinds = make(map[string]struct{})
			g.EdgeKinds[key] = kinds
			g.Out[from] = append(g.Out[from], to)
		}
		kinds[kind] = struct{}{}
	}
	for from := range g.Out {
		sortInPlace(g.Out[from])
	}
	return g
}

func sortInPlace(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func TestComputeGraphMetricsChainAndFanout(t *testing.T) {
	t.Run("chain A->B->C->D", func(t *testing.T) {
		g := buildGraph(
			[]string{"A", "B", "C", "D"},
			[][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}},
			nil,
		)
		m := ComputeGraphMetrics(g)
		require.Equal(t, 3, m.TransitiveDependents["A"])
		require.Equal(t, 3, m.LongestChain["A"])
		require.Equal(t, 2, m.TransitiveDependents["B"])
		require.Equal(t, 2, m.LongestChain["B"])
		require.Equal(t, 1, m.TransitiveDependents["C"])
		require.Equal(t, 1, m.LongestChain["C"])
		require.Equal(t, 0, m.TransitiveDependents["D"])
		require.Equal(t, 0, m.LongestChain["D"])
		require.Empty(t, m.CycleMembers)
	})

	t.Run("fanout X->{P,Q,R}", func(t *testing.T) {
		g := buildGraph(
			[]string{"X", "P", "Q", "R"},
			[][2]string{{"X", "P"}, {"X", "Q"}, {"X", "R"}},
			nil,
		)
		m := ComputeGraphMetrics(g)
		require.Equal(t, 3, m.TransitiveDependents["X"])
		require.Equal(t, 1, m.LongestChain["X"])
		for _, leaf := range []string{"P", "Q", "R"} {
			require.Equal(t, 0, m.TransitiveDependents[leaf])
			require.Equal(t, 0, m.LongestChain[leaf])
		}
	})

	t.Run("diamond A->B, A->C, B->D, C->D: distinct dependents not double-counted", func(t *testing.T) {
		g := buildGraph(
			[]string{"A", "B", "C", "D"},
			[][2]string{{"A", "B"}, {"A", "C"}, {"B", "D"}, {"C", "D"}},
			nil,
		)
		m := ComputeGraphMetrics(g)
		require.Equal(t, 3, m.TransitiveDependents["A"], "A's distinct downstream nodes are B, C, D — not 4 (which would double-count D via both paths)")
		require.Equal(t, 2, m.LongestChain["A"])
	})

	t.Run("empty graph", func(t *testing.T) {
		g := buildGraph(nil, nil, nil)
		m := ComputeGraphMetrics(g)
		require.Empty(t, m.TransitiveDependents)
		require.Empty(t, m.LongestChain)
		require.Empty(t, m.CycleMembers)
	})

	t.Run("single node, no edges", func(t *testing.T) {
		g := buildGraph([]string{"A"}, nil, nil)
		m := ComputeGraphMetrics(g)
		require.Equal(t, 0, m.TransitiveDependents["A"])
		require.Equal(t, 0, m.LongestChain["A"])
		require.Empty(t, m.CycleMembers)
	})
}

func TestComputeGraphMetricsCycleCondensation(t *testing.T) {
	// A -> {B<->C} -> D: B and C form a 2-cycle embedded in a chain.
	done := make(chan struct{})
	var m GraphMetrics
	go func() {
		g := buildGraph(
			[]string{"A", "B", "C", "D"},
			[][2]string{{"A", "B"}, {"B", "C"}, {"C", "B"}, {"C", "D"}},
			nil,
		)
		m = ComputeGraphMetrics(g)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ComputeGraphMetrics did not terminate on a graph containing a cycle")
	}

	require.Equal(t, m.TransitiveDependents["B"], m.TransitiveDependents["C"],
		"cycle members must share the condensation's downstream count")
	require.Equal(t, m.LongestChain["B"], m.LongestChain["C"],
		"cycle members must share the condensation's longest chain")
	require.Equal(t, 1, m.TransitiveDependents["B"])
	require.Equal(t, 1, m.LongestChain["B"])
	require.Equal(t, 3, m.TransitiveDependents["A"])
	require.Equal(t, 2, m.LongestChain["A"])
	require.Equal(t, 0, m.TransitiveDependents["D"])

	require.Contains(t, m.CycleMembers, "B")
	require.Contains(t, m.CycleMembers, "C")
	require.Equal(t, m.CycleMembers["B"], m.CycleMembers["C"])
	require.NotContains(t, m.CycleMembers, "A")
	require.NotContains(t, m.CycleMembers, "D")
}

func TestExtractCyclesKindsAndStability(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("self-edge cycle", func(t *testing.T) {
		g := buildGraph([]string{"A"}, [][2]string{{"A", "A"}}, nil)
		cycles := ExtractCycles(g, nil, now)
		require.Len(t, cycles, 1)
		require.Equal(t, []string{"A"}, cycles[0].Members)
		require.Equal(t, edgeKindTracker, cycles[0].Kind)
		require.Equal(t, now, cycles[0].DetectedAt)
	})

	t.Run("2-cycle mixed kinds", func(t *testing.T) {
		g := buildGraph(
			[]string{"B", "C"},
			[][2]string{{"B", "C"}, {"C", "B"}},
			map[[2]string]string{{"B", "C"}: edgeKindTracker, {"C", "B"}: edgeKindInferred},
		)
		cycles := ExtractCycles(g, nil, now)
		require.Len(t, cycles, 1)
		require.Equal(t, []string{"B", "C"}, cycles[0].Members)
		require.Equal(t, "mixed", cycles[0].Kind)
	})

	t.Run("DetectedAt carried when member set unchanged, re-stamped when changed", func(t *testing.T) {
		earlier := now.Add(-24 * time.Hour)
		prev := []DependencyCycle{
			{Members: []string{"B", "C"}, Kind: edgeKindTracker, DetectedAt: earlier},
		}
		g := buildGraph([]string{"B", "C"}, [][2]string{{"B", "C"}, {"C", "B"}}, nil)
		cycles := ExtractCycles(g, prev, now)
		require.Len(t, cycles, 1)
		require.Equal(t, earlier, cycles[0].DetectedAt, "unchanged member set must carry DetectedAt forward")

		// Member set grows (D joins the cycle) -> must re-stamp with now.
		g2 := buildGraph(
			[]string{"B", "C", "D"},
			[][2]string{{"B", "C"}, {"C", "D"}, {"D", "B"}},
			nil,
		)
		cycles2 := ExtractCycles(g2, prev, now)
		require.Len(t, cycles2, 1)
		require.Equal(t, []string{"B", "C", "D"}, cycles2[0].Members)
		require.Equal(t, now, cycles2[0].DetectedAt, "changed member set must re-stamp DetectedAt")
	})

	t.Run("disjoint cycles sorted by first member", func(t *testing.T) {
		g := buildGraph(
			[]string{"Z", "Y", "B", "C"},
			[][2]string{{"Z", "Y"}, {"Y", "Z"}, {"B", "C"}, {"C", "B"}},
			nil,
		)
		cycles := ExtractCycles(g, nil, now)
		require.Len(t, cycles, 2)
		require.Equal(t, []string{"B", "C"}, cycles[0].Members)
		require.Equal(t, []string{"Y", "Z"}, cycles[1].Members)
	})

	t.Run("no cycles yields empty slice", func(t *testing.T) {
		g := buildGraph([]string{"A", "B"}, [][2]string{{"A", "B"}}, nil)
		cycles := ExtractCycles(g, nil, now)
		require.Empty(t, cycles)
	})
}

// TestCycleKindFromSetMalformedLogsWarn pins wave-2 polish Task 4's
// loud-default fix: an empty/malformed kind set (which ExtractCycles never
// actually produces — every real kind set carries edgeKindTracker and/or
// edgeKindInferred) must log a warning instead of silently returning
// "tracker" as if it were a legitimate tracker-only cycle.
func TestCycleKindFromSetMalformedLogsWarn(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	got := cycleKindFromSet(map[string]struct{}{})

	require.Equal(t, edgeKindTracker, got, "malformed set still falls back to tracker")
	require.Contains(t, buf.String(), "malformed kind set",
		"empty kind set must be logged loudly instead of silently mislabeled")
}

// TestComputeTickGraphAnalysisMatchesSeparateCalls pins wave-2 polish Task
// 4's Tarjan fusion: ComputeTickGraphAnalysis(g, prev, now) must produce
// results identical to calling ComputeGraphMetrics(g) and
// ExtractCycles(g, prev, now) separately, across graphs with fan-out,
// chains, diamonds, self-edges, and multi-member cycles — the shared SCC
// pass must not change either consumer's output. (Mutation check performed
// during implementation: skipping self-edge detection in the shared pass
// made TestExtractCyclesKindsAndStability/self-edge_cycle and this test both
// fail, confirming both consumers actually ride the fused SCC computation
// rather than silently falling back to independent passes.)
func TestComputeTickGraphAnalysisMatchesSeparateCalls(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		nodes []string
		edges [][2]string
	}{
		{"empty graph", nil, nil},
		{"chain", []string{"A", "B", "C", "D"}, [][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}}},
		{"fanout", []string{"X", "P", "Q", "R"}, [][2]string{{"X", "P"}, {"X", "Q"}, {"X", "R"}}},
		{"diamond", []string{"A", "B", "C", "D"}, [][2]string{{"A", "B"}, {"A", "C"}, {"B", "D"}, {"C", "D"}}},
		{"self-edge", []string{"A"}, [][2]string{{"A", "A"}}},
		{"2-cycle", []string{"B", "C"}, [][2]string{{"B", "C"}, {"C", "B"}}},
		{"disjoint cycles", []string{"Z", "Y", "B", "C"}, [][2]string{{"Z", "Y"}, {"Y", "Z"}, {"B", "C"}, {"C", "B"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildGraph(tc.nodes, tc.edges, nil)
			prev := []DependencyCycle{{Members: []string{"Z", "Y"}, Kind: "tracker", DetectedAt: now.Add(-time.Hour)}}

			wantMetrics := ComputeGraphMetrics(g)
			wantCycles := ExtractCycles(g, prev, now)

			gotMetrics, gotCycles := ComputeTickGraphAnalysis(g, prev, now)

			require.Equal(t, wantMetrics, gotMetrics)
			require.Equal(t, wantCycles, gotCycles)
		})
	}
}
