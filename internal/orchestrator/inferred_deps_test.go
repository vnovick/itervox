package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// inferredDepsFixture returns the shared candidate set + State used across
// TestReconcileInferredDeps cases: ENG-1 is a known, non-terminal candidate;
// ENG-9 is a known candidate in a terminal state ("Done").
func inferredDepsFixture() (candidates []domain.Issue, state State) {
	candidates = []domain.Issue{
		{Identifier: "ENG-1", State: "Todo"},
		{Identifier: "ENG-9", State: "Done"},
	}
	state = State{TerminalStates: []string{"Done"}}
	return candidates, state
}

func inferredDepsCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Dependencies = config.DependenciesConfig{
		InferredGating:      true,
		ConfidenceThreshold: 0.7,
		StalenessHours:      168,
	}
	return cfg
}

func TestReconcileInferredDeps(t *testing.T) {
	candidates, state := inferredDepsFixture()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	type want struct {
		stale, belowThreshold, sourceKnown, sourceTerminal, overridden, gating bool
	}

	tests := []struct {
		name      string
		edge      depsanalysis.InferredEdge
		overrides map[string]time.Time
		cfgMut    func(*config.Config)
		want      want
	}{
		{
			name: "gates at exactly threshold (0.7 vs 0.7)",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.7, InferredAt: now,
			},
			want: want{sourceKnown: true, gating: true},
		},
		{
			name: "below threshold (0.69)",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.69, InferredAt: now,
			},
			want: want{belowThreshold: true, sourceKnown: true, gating: false},
		},
		{
			// Clamp mirror: a hand-constructed config with an out-of-range
			// threshold must fall back to the loader default (0.7), so an
			// edge at 0.7 gates rather than being compared against 1.5.
			name: "out-of-range threshold falls back to default",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.7, InferredAt: now,
			},
			cfgMut: func(cfg *config.Config) { cfg.Dependencies.ConfidenceThreshold = 1.5 },
			want:   want{sourceKnown: true, gating: true},
		},
		{
			// Clamp mirror: non-positive staleness falls back to 168h, so a
			// week-old edge still gates instead of being instantly stale.
			name: "non-positive staleness falls back to default",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.9,
				InferredAt: now.Add(-100 * time.Hour),
			},
			cfgMut: func(cfg *config.Config) { cfg.Dependencies.StalenessHours = -5 },
			want:   want{sourceKnown: true, gating: true},
		},
		{
			name: "gates at exactly staleness boundary (age == 168h)",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.9,
				InferredAt: now.Add(-168 * time.Hour),
			},
			want: want{sourceKnown: true, gating: true},
		},
		{
			name: "stale past the staleness boundary",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.9,
				InferredAt: now.Add(-168*time.Hour - time.Second),
			},
			want: want{stale: true, sourceKnown: true, gating: false},
		},
		{
			name: "unknown source not gating",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-UNKNOWN", Target: "ENG-2", Confidence: 0.9, InferredAt: now,
			},
			want: want{sourceKnown: false, gating: false},
		},
		{
			name: "terminal source not gating",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-9", Target: "ENG-2", Confidence: 0.9, InferredAt: now,
			},
			want: want{sourceKnown: true, sourceTerminal: true, gating: false},
		},
		{
			name: "override present not gating",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.9, InferredAt: now,
			},
			overrides: map[string]time.Time{"ENG-2": now},
			want:      want{sourceKnown: true, overridden: true, gating: false},
		},
		{
			name: "kill-switch InferredGating=false: entry present, Gating=false",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0.9, InferredAt: now,
			},
			cfgMut: func(c *config.Config) { c.Dependencies.InferredGating = false },
			want:   want{sourceKnown: true, gating: false},
		},
		{
			name: "v1 edge confidence 0 present-not-gating",
			edge: depsanalysis.InferredEdge{
				Source: "ENG-1", Target: "ENG-2", Confidence: 0, InferredAt: now,
			},
			want: want{belowThreshold: true, sourceKnown: true, gating: false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := inferredDepsCfg()
			if tc.cfgMut != nil {
				tc.cfgMut(cfg)
			}

			result := ReconcileInferredDeps(
				[]depsanalysis.InferredEdge{tc.edge},
				candidates,
				tc.overrides,
				cfg,
				state,
				now,
			)

			entries, ok := result["ENG-2"]
			require.True(t, ok, "expected an entry for target ENG-2")
			require.Len(t, entries, 1)
			entry := entries[0]

			require.Equal(t, tc.edge.Source, entry.Source)
			require.Equal(t, tc.edge.Confidence, entry.Confidence)
			require.Equal(t, tc.edge.InferredAt, entry.InferredAt)
			require.Equal(t, tc.want.stale, entry.Stale, "Stale")
			require.Equal(t, tc.want.belowThreshold, entry.BelowThreshold, "BelowThreshold")
			require.Equal(t, tc.want.sourceKnown, entry.SourceKnown, "SourceKnown")
			require.Equal(t, tc.want.sourceTerminal, entry.SourceTerminal, "SourceTerminal")
			require.Equal(t, tc.want.overridden, entry.Overridden, "Overridden")
			require.Equal(t, tc.want.gating, entry.Gating, "Gating")
		})
	}
}

// TestReconcileInferredDepsDropsEmptyTargetEdges locks the documented
// contract that edges with no Target are discarded rather than surfacing
// under a "" key.
func TestReconcileInferredDepsDropsEmptyTargetEdges(t *testing.T) {
	candidates, state := inferredDepsFixture()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := inferredDepsCfg()

	edges := []depsanalysis.InferredEdge{
		{Source: "ENG-1", Target: "", Confidence: 0.9, InferredAt: now},
		{Source: "ENG-1", Target: "ENG-2", Confidence: 0.9, InferredAt: now},
	}

	result := ReconcileInferredDeps(edges, candidates, nil, cfg, state, now)

	require.NotContains(t, result, "")
	require.Len(t, result, 1)
	require.Len(t, result["ENG-2"], 1)
}

// TestReconcileInferredDepsPreservesEdgeOrderPerTarget locks the documented
// contract that multiple edges landing on the same target keep input order.
func TestReconcileInferredDepsPreservesEdgeOrderPerTarget(t *testing.T) {
	candidates, state := inferredDepsFixture()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := inferredDepsCfg()

	edges := []depsanalysis.InferredEdge{
		{Source: "ENG-1", Target: "ENG-2", Evidence: "first", Confidence: 0.9, InferredAt: now},
		{Source: "ENG-9", Target: "ENG-2", Evidence: "second", Confidence: 0.8, InferredAt: now},
	}

	result := ReconcileInferredDeps(edges, candidates, nil, cfg, state, now)

	require.Len(t, result["ENG-2"], 2)
	require.Equal(t, "first", result["ENG-2"][0].Evidence)
	require.Equal(t, "second", result["ENG-2"][1].Evidence)
}

// TestReconcileInferredDepsNoEdgesReturnsEmptyMap locks the nil-sidecar path
// (no sidecar configured, or an empty sidecar) — must be an empty map, never
// nil, so downstream consumers can range over it unconditionally.
func TestReconcileInferredDepsNoEdgesReturnsEmptyMap(t *testing.T) {
	candidates, state := inferredDepsFixture()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := inferredDepsCfg()

	result := ReconcileInferredDeps(nil, candidates, nil, cfg, state, now)

	require.NotNil(t, result)
	require.Empty(t, result)
}

// TestEventLoopPopulatesInferredDeps drives a real orchestrator + real State
// through one onTick with a sidecar file on disk, and asserts the published
// snapshot carries the reconciled InferredDeps entries. Never mocks State
// (repo rule) — uses tracker.NewMemoryTracker and NewState like the existing
// dependency-audit event-loop tests (see dependency_audit_test.go).
func TestEventLoopPopulatesInferredDeps(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Dependencies = config.DependenciesConfig{
		InferredGating:      true,
		ConfidenceThreshold: 0.5,
		StalenessHours:      168,
	}

	blocked := domain.Issue{ID: "issue-2", Identifier: "ENG-2", State: "Todo"}
	blocker := domain.Issue{ID: "issue-1", Identifier: "ENG-1", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{blocked, blocker}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	o := New(cfg, mt, nil, nil)

	tmp := t.TempDir()
	sidecarPath := depsanalysis.SidecarPath(tmp)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	sc := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: now,
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "issue body mentions ENG-1", Confidence: 0.9, InferredAt: now},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, sc))

	o.SetDepsSidecarPath(sidecarPath)

	state := NewState(cfg)
	out := o.onTick(t.Context(), state)

	entries := out.InferredDeps["ENG-2"]
	require.Len(t, entries, 1, "onTick should have populated InferredDeps from the sidecar")
	require.Equal(t, "ENG-1", entries[0].Source)
	require.True(t, entries[0].Gating, "non-terminal, known, fresh, above-threshold source should gate")

	o.storeSnap(out)
	snap := o.Snapshot()
	snapEntries := snap.InferredDeps["ENG-2"]
	require.Len(t, snapEntries, 1, "snapshot must carry InferredDeps through storeSnap/Snapshot")
	require.Equal(t, "ENG-1", snapEntries[0].Source)
	require.True(t, snapEntries[0].Gating)
}

// TestSnapshotDeepCopiesInferredDeps mirrors ORCH-3
// (TestSnapshotDeepCopiesPRDispatchLedgers in snapshot_test.go): storeSnap /
// Snapshot must not alias the live State.InferredDeps map or its per-target
// slices. Without the deep copy in storeSnap, a mutation on the event-loop's
// State after storeSnap would leak into an already-published snapshot,
// racing with concurrent HTTP-handler reads of Snapshot().
func TestSnapshotDeepCopiesInferredDeps(t *testing.T) {
	cfg := &config.Config{}
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	state.InferredDeps["ENG-2"] = []InferredDepEntry{{Source: "ENG-1", Evidence: "orig"}}

	o.storeSnap(state)
	snap := o.Snapshot()

	// Mutate the live state's map and slice after publishing the snapshot.
	state.InferredDeps["ENG-3"] = []InferredDepEntry{{Source: "ENG-9"}}
	state.InferredDeps["ENG-2"][0].Evidence = "mutated"

	require.NotContains(t, snap.InferredDeps, "ENG-3", "snapshot aliases the live InferredDeps map")
	require.Equal(t, "orig", snap.InferredDeps["ENG-2"][0].Evidence, "snapshot aliases the live InferredDeps slice")
}

// TestStoreSnapDeepCopiesInferredDeps checks storeSnap's own deep copy in
// isolation from Snapshot()'s independent copy (which would otherwise mask a
// missing copy inside storeSnap — Snapshot() always re-copies whatever it
// finds in lastSnap, so a bug that lets storeSnap alias lastSnap.InferredDeps
// to the live event-loop map would not surface through the Snapshot()
// round-trip alone). Reads o.lastSnap directly, same whitebox pattern as
// dependency_audit_persistence_test.go's "must not alias" check.
func TestStoreSnapDeepCopiesInferredDeps(t *testing.T) {
	o := &Orchestrator{}
	state := State{InferredDeps: map[string][]InferredDepEntry{
		"ENG-2": {{Source: "ENG-1", Evidence: "orig"}},
	}}

	o.storeSnap(state)

	state.InferredDeps["ENG-2"][0].Evidence = "mutated"
	state.InferredDeps["ENG-3"] = []InferredDepEntry{{Source: "ENG-9"}}

	o.snapMu.RLock()
	got := o.lastSnap.InferredDeps
	o.snapMu.RUnlock()

	require.NotContains(t, got, "ENG-3", "storeSnap aliases the live InferredDeps map")
	require.Equal(t, "orig", got["ENG-2"][0].Evidence, "storeSnap aliases the live InferredDeps slice")
}

// TestEventLoopInferredDepsEmptyWithNoSidecarConfigured locks the nil-safety
// requirement: an orchestrator that never called SetDepsSidecarPath must not
// panic on tick and must publish an empty (not nil-panicking) InferredDeps.
func TestEventLoopInferredDepsEmptyWithNoSidecarConfigured(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Dependencies = config.DependenciesConfig{InferredGating: true, ConfidenceThreshold: 0.5, StalenessHours: 168}
	issue := domain.Issue{ID: "issue-2", Identifier: "ENG-2", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)

	state := NewState(cfg)
	out := o.onTick(t.Context(), state)

	require.Empty(t, out.InferredDeps)
}
