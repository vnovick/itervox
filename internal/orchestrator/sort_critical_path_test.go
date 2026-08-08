package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func criticalPathIssue(identifier string) domain.Issue {
	return domain.Issue{Identifier: identifier}
}

// TestCriticalPathFanoutBeatsChainHead: same priority; X directly and
// transitively unblocks 3 issues (P, Q, R). A heads a chain A->B->C with
// only 2 transitive dependents. X's larger fan-out must win the sort even
// though the input order and identifiers would otherwise favor A.
func TestCriticalPathFanoutBeatsChainHead(t *testing.T) {
	m := GraphMetrics{
		TransitiveDependents: map[string]int{"ENG-X": 3, "ENG-A": 2},
		LongestChain:         map[string]int{"ENG-X": 1, "ENG-A": 2},
	}
	issues := []domain.Issue{criticalPathIssue("ENG-A"), criticalPathIssue("ENG-X")}

	got := SortForDispatchCriticalPath(issues, m)

	require.Equal(t, "ENG-X", got[0].Identifier, "higher transitive-dependent count must sort first")
	require.Equal(t, "ENG-A", got[1].Identifier)
}

// TestCriticalPathChainTiebreak: equal TransitiveDependents but different
// LongestChain -> the issue heading the longer downstream chain sorts first.
func TestCriticalPathChainTiebreak(t *testing.T) {
	m := GraphMetrics{
		TransitiveDependents: map[string]int{"ENG-SHORT": 2, "ENG-LONG": 2},
		LongestChain:         map[string]int{"ENG-SHORT": 1, "ENG-LONG": 3},
	}
	issues := []domain.Issue{criticalPathIssue("ENG-SHORT"), criticalPathIssue("ENG-LONG")}

	got := SortForDispatchCriticalPath(issues, m)

	require.Equal(t, "ENG-LONG", got[0].Identifier, "longer chain must win the tiebreak when transitive-dependent counts are equal")
	require.Equal(t, "ENG-SHORT", got[1].Identifier)
}

// TestCriticalPathPriorityBandDominates: a P1 leaf (zero dependents) must
// sort ahead of a P2 foundation issue with a huge fan-out — priority band is
// the dominant key, evaluated before any graph metric.
func TestCriticalPathPriorityBandDominates(t *testing.T) {
	p1, p2 := 1, 2
	foundation := domain.Issue{Identifier: "ENG-FOUNDATION", Priority: &p2}
	leaf := domain.Issue{Identifier: "ENG-LEAF", Priority: &p1}
	m := GraphMetrics{
		TransitiveDependents: map[string]int{"ENG-FOUNDATION": 50, "ENG-LEAF": 0},
	}
	issues := []domain.Issue{foundation, leaf}

	got := SortForDispatchCriticalPath(issues, m)

	require.Equal(t, "ENG-LEAF", got[0].Identifier, "higher-priority-band (P1) issue must sort before lower-priority-band (P2) issue regardless of fan-out")
	require.Equal(t, "ENG-FOUNDATION", got[1].Identifier)
}

// TestCriticalPathFallsBackToCreatedAtAndIdentifier: with zero graph metrics
// (an empty GraphMetrics, as if the issue set has no dependency edges at
// all), SortForDispatchCriticalPath must produce byte-identical order to
// SortForDispatch on the same input — it is a strict superset, not a
// different comparator.
func TestCriticalPathFallsBackToCreatedAtAndIdentifier(t *testing.T) {
	p1, p2 := 1, 2
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{Identifier: "ENG-Z", Priority: &p2, CreatedAt: &t1},
		{Identifier: "ENG-B", Priority: &p1, CreatedAt: &t2},
		{Identifier: "ENG-A", Priority: &p1, CreatedAt: &t2}, // same priority+createdAt as ENG-B, tiebreak by identifier
		{Identifier: "ENG-NOPRIO"},                           // nil priority sorts last
		{Identifier: "ENG-NODATE", Priority: &p1},            // nil createdAt sorts last within its priority band
	}

	legacy := SortForDispatch(issues)
	criticalPath := SortForDispatchCriticalPath(issues, GraphMetrics{})

	require.Equal(t, identifiersOf(legacy), identifiersOf(criticalPath))
}

// TestSimpleOrderingMatchesLegacy has two parts: a direct unit assertion that
// SortForDispatchCriticalPath with empty metrics equals SortForDispatch
// (restated here for a name-anchored regression, on top of the equivalent
// coverage in TestCriticalPathFallsBackToCreatedAtAndIdentifier), plus an
// event-loop-level assertion that dependencies.ordering: simple actually
// routes dispatch through legacy SortForDispatch and not the critical-path
// comparator.
func TestSimpleOrderingMatchesLegacy(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		p1 := 1
		issues := []domain.Issue{
			{Identifier: "ENG-B", Priority: &p1},
			{Identifier: "ENG-A", Priority: &p1},
		}
		require.Equal(t,
			identifiersOf(SortForDispatch(issues)),
			identifiersOf(SortForDispatchCriticalPath(issues, GraphMetrics{})),
		)
	})

	t.Run("event_loop", func(t *testing.T) {
		cfg, mt := fanoutFixture(t, config.DependenciesOrderingSimple)
		o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
		state := NewState(cfg)

		out := o.onTick(t.Context(), state)

		// Legacy order (priority tie, createdAt ASC) picks the older leaf L
		// first — X's huge fan-out has no effect under "simple" ordering.
		require.Contains(t, out.Running, "issue-l", "simple ordering should dispatch the older leaf first, matching legacy SortForDispatch")
		require.NotContains(t, out.Running, "issue-x")
	})
}

// TestEventLoopOrdersByCriticalPath is the integration counterpart: a real
// orchestrator with a fake tracker, a fan-out fixture (X unblocks P, Q, R;
// unrelated leaf L has the same priority but an EARLIER createdAt, so legacy
// ordering would pick L), MaxConcurrentAgents=1. With critical-path ordering
// (the default), X must dispatch first even though it is younger than L.
func TestEventLoopOrdersByCriticalPath(t *testing.T) {
	cfg, mt := fanoutFixture(t, config.DependenciesOrderingCriticalPath)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	state := NewState(cfg)

	out := o.onTick(t.Context(), state)

	require.Contains(t, out.Running, "issue-x", "critical-path ordering should dispatch the fan-out foundation issue first")
	require.NotContains(t, out.Running, "issue-l")
}

// fanoutFixture builds the shared fixture for the ordering-mode integration
// tests: X (unblocked, blocks P/Q/R) and unrelated leaf L, both priority-less
// (equal band), with L created strictly before X so legacy ordering and
// critical-path ordering disagree on which one dispatches first.
// MaxConcurrentAgents is pinned to 1 so exactly one issue can dispatch per
// tick, making the winner observable.
func fanoutFixture(t *testing.T, ordering string) (*config.Config, *tracker.MemoryTracker) {
	t.Helper()

	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.Command = "codex"
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Dependencies.Ordering = ordering

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	younger := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	x := domain.Issue{ID: "issue-x", Identifier: "ENG-X", Title: "Foundation", State: "Todo", CreatedAt: &younger}
	p := domain.Issue{ID: "issue-p", Identifier: "ENG-P", Title: "P", State: "Todo",
		BlockedBy: []domain.BlockerRef{{Identifier: strPtr("ENG-X")}}}
	q := domain.Issue{ID: "issue-q", Identifier: "ENG-Q", Title: "Q", State: "Todo",
		BlockedBy: []domain.BlockerRef{{Identifier: strPtr("ENG-X")}}}
	r := domain.Issue{ID: "issue-r", Identifier: "ENG-R", Title: "R", State: "Todo",
		BlockedBy: []domain.BlockerRef{{Identifier: strPtr("ENG-X")}}}
	l := domain.Issue{ID: "issue-l", Identifier: "ENG-L", Title: "Unrelated leaf", State: "Todo", CreatedAt: &older}

	mt := tracker.NewMemoryTracker([]domain.Issue{x, p, q, r, l}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	return cfg, mt
}

func identifiersOf(issues []domain.Issue) []string {
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = issue.Identifier
	}
	return out
}
