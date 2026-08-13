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

// dependencyAttentionGraph returns a minimal TickGraph whose only node is
// id — DeriveDependencyAttention's stale_blocker candidate loop only
// consults g.Nodes, so tests that don't need edges can use this instead of
// going through BuildTickGraph.
func dependencyAttentionGraph(ids ...string) TickGraph {
	g := TickGraph{
		Nodes:     make(map[string]struct{}, len(ids)),
		Out:       make(map[string][]string),
		EdgeKinds: make(map[string]map[string]struct{}),
	}
	for _, id := range ids {
		g.Nodes[id] = struct{}{}
	}
	return g
}

func dependencyAttentionCfg(escalateHours int) *config.Config {
	cfg := &config.Config{}
	cfg.Dependencies.EscalateBlockedAfterHours = escalateHours
	return cfg
}

// TestDeriveDependencyAttentionWindowBoundary locks the exactly-at-window
// boundary: now.Sub(BlockedSince) == window must NOT escalate; one second
// past must.
func TestDeriveDependencyAttentionWindowBoundary(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(48)
	g := dependencyAttentionGraph("ENG-2")

	makeState := func(firstBlockedAt time.Time) State {
		return State{
			DependencyAudit: map[string]*DependencyAuditEntry{
				"ENG-2": {
					UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-1", nil)},
					FirstBlockedAt:     firstBlockedAt,
				},
			},
		}
	}

	t.Run("exactly at window: absent", func(t *testing.T) {
		state := makeState(now.Add(-48 * time.Hour))
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Empty(t, got, "exactly-at-window must not escalate")
	})

	t.Run("one second past window: present", func(t *testing.T) {
		state := makeState(now.Add(-48*time.Hour - time.Second))
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Len(t, got, 1)
		require.Equal(t, "ENG-2", got[0].Identifier)
		require.Equal(t, "stale_blocker", got[0].Kind)
		require.Equal(t, []string{"ENG-1"}, got[0].Blockers)
	})
}

// TestDeriveDependencyAttentionDisabled locks that an escalation window of 0
// disables stale_blocker entries entirely (even for a blocker stuck for a
// very long time) while cycle entries are unaffected — cycles are
// structural and window-independent.
func TestDeriveDependencyAttentionDisabled(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(0)
	g := dependencyAttentionGraph("ENG-2")

	state := State{
		DependencyAudit: map[string]*DependencyAuditEntry{
			"ENG-2": {
				UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-1", nil)},
				FirstBlockedAt:     now.Add(-1000 * time.Hour),
			},
		},
	}
	cycles := []DependencyCycle{
		{Members: []string{"A", "B"}, Kind: "tracker", DetectedAt: now.Add(-time.Hour)},
	}

	got := DeriveDependencyAttention(g, cycles, state, cfg, now)

	require.Len(t, got, 2, "window disabled: only the cycle entries should be present")
	require.Equal(t, "A", got[0].Identifier)
	require.Equal(t, "cycle", got[0].Kind)
	require.Equal(t, []string{"B"}, got[0].Blockers)
	require.Equal(t, "B", got[1].Identifier)
	require.Equal(t, "cycle", got[1].Kind)
	require.Equal(t, []string{"A"}, got[1].Blockers)
}

// TestDeriveDependencyAttentionCycleKindWins locks that an identifier which
// is both a cycle member AND has a stale tracker blocker past the
// escalation window produces exactly one entry, with Kind "cycle" — cycle
// membership wins, and Blockers/BlockedSince come from the cycle, not the
// stale-blocker computation.
func TestDeriveDependencyAttentionCycleKindWins(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(48)
	g := dependencyAttentionGraph("ENG-2", "ENG-3")

	state := State{
		DependencyAudit: map[string]*DependencyAuditEntry{
			"ENG-2": {
				UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-9", nil)},
				FirstBlockedAt:     now.Add(-100 * time.Hour), // well past the 48h window
			},
		},
	}
	cycleDetectedAt := now.Add(-10 * time.Hour)
	cycles := []DependencyCycle{
		{Members: []string{"ENG-2", "ENG-3"}, Kind: "inferred", DetectedAt: cycleDetectedAt},
	}

	got := DeriveDependencyAttention(g, cycles, state, cfg, now)

	require.Len(t, got, 2, "ENG-2 must produce exactly one entry, not one per Kind")
	require.Equal(t, "ENG-2", got[0].Identifier)
	require.Equal(t, "cycle", got[0].Kind)
	require.Equal(t, []string{"ENG-3"}, got[0].Blockers, "cycle Blockers must be the other cycle members, not ENG-9")
	require.True(t, got[0].BlockedSince.Equal(cycleDetectedAt), "BlockedSince must be the cycle's DetectedAt, not the audit FirstBlockedAt")
	require.Equal(t, "ENG-3", got[1].Identifier)
	require.Equal(t, "cycle", got[1].Kind)
}

// TestDeriveDependencyAttentionBlockedSinceSources locks the three
// BlockedSince derivations: tracker-only uses the audit row's
// FirstBlockedAt, inferred-only uses the earliest gating InferredAt, and an
// issue with both sources uses whichever is earlier.
func TestDeriveDependencyAttentionBlockedSinceSources(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(48)

	t.Run("tracker-only uses FirstBlockedAt", func(t *testing.T) {
		g := dependencyAttentionGraph("ENG-2")
		trackerSince := now.Add(-100 * time.Hour)
		state := State{
			DependencyAudit: map[string]*DependencyAuditEntry{
				"ENG-2": {
					UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-1", nil)},
					FirstBlockedAt:     trackerSince,
				},
			},
		}
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Len(t, got, 1)
		require.True(t, got[0].BlockedSince.Equal(trackerSince))
	})

	t.Run("inferred-only uses earliest InferredAt", func(t *testing.T) {
		g := dependencyAttentionGraph("ENG-2")
		earliest := now.Add(-90 * time.Hour)
		later := now.Add(-80 * time.Hour)
		state := State{
			InferredDeps: map[string][]InferredDepEntry{
				"ENG-2": {
					{Source: "ENG-5", Gating: true, InferredAt: later},
					{Source: "ENG-6", Gating: true, InferredAt: earliest},
				},
			},
		}
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Len(t, got, 1)
		require.True(t, got[0].BlockedSince.Equal(earliest))
		require.Equal(t, []string{"ENG-5", "ENG-6"}, got[0].Blockers)
	})

	t.Run("both sources: earlier wins (tracker earlier)", func(t *testing.T) {
		g := dependencyAttentionGraph("ENG-2")
		trackerSince := now.Add(-100 * time.Hour) // earlier
		inferredSince := now.Add(-60 * time.Hour)
		state := State{
			DependencyAudit: map[string]*DependencyAuditEntry{
				"ENG-2": {
					UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-1", nil)},
					FirstBlockedAt:     trackerSince,
				},
			},
			InferredDeps: map[string][]InferredDepEntry{
				"ENG-2": {{Source: "ENG-5", Gating: true, InferredAt: inferredSince}},
			},
		}
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Len(t, got, 1)
		require.True(t, got[0].BlockedSince.Equal(trackerSince))
		require.Equal(t, []string{"ENG-1", "ENG-5"}, got[0].Blockers)
	})

	t.Run("both sources: earlier wins (inferred earlier)", func(t *testing.T) {
		g := dependencyAttentionGraph("ENG-2")
		trackerSince := now.Add(-60 * time.Hour)
		inferredSince := now.Add(-100 * time.Hour) // earlier
		state := State{
			DependencyAudit: map[string]*DependencyAuditEntry{
				"ENG-2": {
					UnresolvedBlockers: []domain.BlockerRef{blockerRef("ENG-1", nil)},
					FirstBlockedAt:     trackerSince,
				},
			},
			InferredDeps: map[string][]InferredDepEntry{
				"ENG-2": {{Source: "ENG-5", Gating: true, InferredAt: inferredSince}},
			},
		}
		got := DeriveDependencyAttention(g, nil, state, cfg, now)
		require.Len(t, got, 1)
		require.True(t, got[0].BlockedSince.Equal(inferredSince))
	})
}

// TestEventLoopDerivesCyclesAndAttention drives a real orchestrator + real
// State through one onTick with a sidecar file that asserts a 2-cycle
// between two candidates (ENG-1 -> ENG-2 -> ENG-1, both gating inferred
// edges), and asserts the published snapshot carries exactly one
// DependencyCycle and two DependencyAttention entries, both Kind "cycle".
// Mirrors TestEventLoopPopulatesInferredDeps; never mocks State.
func TestEventLoopDerivesCyclesAndAttention(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Dependencies = config.DependenciesConfig{
		InferredGating:            true,
		ConfidenceThreshold:       0.5,
		StalenessHours:            168,
		EscalateBlockedAfterHours: 48,
	}

	issueA := domain.Issue{ID: "issue-1", Identifier: "ENG-1", State: "Todo"}
	issueB := domain.Issue{ID: "issue-2", Identifier: "ENG-2", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issueA, issueB}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	o := New(cfg, mt, nil, nil)

	tmp := t.TempDir()
	sidecarPath := depsanalysis.SidecarPath(tmp)
	// MUST be relative to time.Now() — see the identical note in
	// TestEventLoopPopulatesInferredDeps. onTick measures staleness against
	// the wall clock, so a fixed date fuses this test once StalenessHours
	// elapses from it.
	now := time.Now()
	sc := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: now,
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "ENG-2 mentions ENG-1", Confidence: 0.9, InferredAt: now},
			{Source: "ENG-2", Target: "ENG-1", Evidence: "ENG-1 mentions ENG-2", Confidence: 0.9, InferredAt: now},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, sc))
	o.SetDepsSidecarPath(sidecarPath)

	state := NewState(cfg)
	out := o.onTick(t.Context(), state)

	require.Len(t, out.DependencyCycles, 1, "onTick should have detected the ENG-1<->ENG-2 cycle")
	require.Equal(t, []string{"ENG-1", "ENG-2"}, out.DependencyCycles[0].Members)

	require.Len(t, out.DependencyAttention, 2)
	for _, entry := range out.DependencyAttention {
		require.Equal(t, "cycle", entry.Kind)
	}

	o.storeSnap(out)
	snap := o.Snapshot()

	require.Len(t, snap.DependencyCycles, 1, "snapshot must carry DependencyCycles through storeSnap/Snapshot")
	require.Equal(t, []string{"ENG-1", "ENG-2"}, snap.DependencyCycles[0].Members)
	require.Len(t, snap.DependencyAttention, 2, "snapshot must carry DependencyAttention through storeSnap/Snapshot")
	for _, entry := range snap.DependencyAttention {
		require.Equal(t, "cycle", entry.Kind)
	}
}

// TestSnapshotDeepCopiesDependencyCycles mirrors
// TestSnapshotDeepCopiesInferredDeps: storeSnap/Snapshot must not alias the
// live State.DependencyCycles/DependencyAttention slices (or their
// per-entry Members/Blockers slices). A mutation on the event-loop's State
// after storeSnap must not leak into an already-published snapshot.
func TestSnapshotDeepCopiesDependencyCycles(t *testing.T) {
	cfg := &config.Config{}
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	state.DependencyCycles = []DependencyCycle{{Members: []string{"A", "B"}, Kind: "tracker"}}
	state.DependencyAttention = []DependencyAttentionEntry{{Identifier: "A", Blockers: []string{"B"}, Kind: "cycle"}}

	o.storeSnap(state)
	snap := o.Snapshot()

	// Mutate the live state's slices after publishing the snapshot.
	state.DependencyCycles[0].Members[0] = "MUTATED"
	state.DependencyCycles = append(state.DependencyCycles, DependencyCycle{Members: []string{"C"}})
	state.DependencyAttention[0].Blockers[0] = "MUTATED"
	state.DependencyAttention = append(state.DependencyAttention, DependencyAttentionEntry{Identifier: "Z"})

	require.Equal(t, "A", snap.DependencyCycles[0].Members[0], "snapshot aliases the live DependencyCycles Members slice")
	require.Len(t, snap.DependencyCycles, 1, "snapshot aliases the live DependencyCycles slice")
	require.Equal(t, "B", snap.DependencyAttention[0].Blockers[0], "snapshot aliases the live DependencyAttention Blockers slice")
	require.Len(t, snap.DependencyAttention, 1, "snapshot aliases the live DependencyAttention slice")
}

// TestStoreSnapDeepCopiesDependencyAttention checks storeSnap's own deep
// copy in isolation from Snapshot()'s independent copy — Snapshot() always
// re-copies whatever it finds in lastSnap, so a bug that lets storeSnap
// alias lastSnap.DependencyCycles/DependencyAttention to the live
// event-loop slices would not surface through the Snapshot() round-trip
// alone. Reads o.lastSnap directly, mirroring
// TestStoreSnapDeepCopiesInferredDeps.
func TestStoreSnapDeepCopiesDependencyAttention(t *testing.T) {
	o := &Orchestrator{}
	state := State{
		DependencyCycles:    []DependencyCycle{{Members: []string{"A", "B"}, Kind: "tracker"}},
		DependencyAttention: []DependencyAttentionEntry{{Identifier: "A", Blockers: []string{"B"}, Kind: "cycle"}},
	}

	o.storeSnap(state)

	state.DependencyCycles[0].Members[0] = "MUTATED"
	state.DependencyCycles = append(state.DependencyCycles, DependencyCycle{Members: []string{"C"}})
	state.DependencyAttention[0].Blockers[0] = "MUTATED"
	state.DependencyAttention = append(state.DependencyAttention, DependencyAttentionEntry{Identifier: "Z"})

	o.snapMu.RLock()
	gotCycles := o.lastSnap.DependencyCycles
	gotAttention := o.lastSnap.DependencyAttention
	o.snapMu.RUnlock()

	require.Equal(t, "A", gotCycles[0].Members[0], "storeSnap aliases the live DependencyCycles Members slice")
	require.Len(t, gotCycles, 1, "storeSnap aliases the live DependencyCycles slice")
	require.Equal(t, "B", gotAttention[0].Blockers[0], "storeSnap aliases the live DependencyAttention Blockers slice")
	require.Len(t, gotAttention, 1, "storeSnap aliases the live DependencyAttention slice")
}

// TestDeriveDependencyAttentionFindsAuditRowsKeyedByID locks the production
// audit-map key shape: dependencyAuditKey (dependency_audit.go) prefers
// issue.ID, which for real trackers is a UUID (Linear) or a bare numeric
// string (GitHub) — never the identifier that TickGraph nodes and this
// function's escalation loop key on. Builds the audit row through the real
// write-site helper (auditIssueDependencies) so the map key is
// production-shaped, not hand-keyed by identifier the way the other tests
// in this file construct State.DependencyAudit directly. Two audit passes
// drive a real FirstBlockedAt stamp 100h in the past, then DeriveDependencyAttention
// must still find the row via an Identifier index and escalate.
func TestDeriveDependencyAttentionFindsAuditRowsKeyedByID(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(48)

	issue := domain.Issue{
		ID:         "550e8400-e29b-41d4-a716-446655440000", // UUID-like: production ID shape
		Identifier: "ENG-2",
		State:      "Todo",
		BlockedBy:  []domain.BlockerRef{blockerRef("ENG-9", strPtr("Todo"))},
	}

	state := &State{}
	// First pass, 100h ago: issue is blocked (blocker state known, non-terminal),
	// so auditIssueDependencies stamps FirstBlockedAt via the real production
	// write-site (dependency_audit.go:auditIssueDependencies).
	auditIssueDependencies(state, issue, now.Add(-100*time.Hour))
	// Second pass, now: still blocked; FirstBlockedAt is carried over from prev.
	auditIssueDependencies(state, issue, now)

	// Sanity check: the map is keyed by issue.ID (production shape), NOT by
	// the identifier — this is the precondition the bug depends on.
	require.Contains(t, state.DependencyAudit, issue.ID, "audit map must be keyed by issue.ID per dependencyAuditKey")
	require.NotContains(t, state.DependencyAudit, issue.Identifier, "audit map must NOT be keyed by identifier in production")

	g := dependencyAttentionGraph("ENG-2")
	got := DeriveDependencyAttention(g, nil, *state, cfg, now)

	require.Len(t, got, 1, "stale_blocker entry must be found via an Identifier index despite the audit map being keyed by issue.ID")
	require.Equal(t, "ENG-2", got[0].Identifier)
	require.Equal(t, "stale_blocker", got[0].Kind)
	require.True(t, got[0].BlockedSince.Equal(now.Add(-100*time.Hour)), "BlockedSince must be the tracker FirstBlockedAt stamped by the real write-site")
	require.Equal(t, []string{"ENG-9"}, got[0].Blockers)
}

// TestDeriveDependencyAttentionSkipsZeroBlockedSince locks that a row whose
// combined blockedSince is the zero time (an Unknown-status audit row —
// blocker State nil, e.g. a transient tracker outage — with
// UnresolvedBlockers non-empty but FirstBlockedAt never stamped, and no
// inferred gating edge) does NOT escalate: there is no evidence of duration,
// so now.Sub(zero) exceeding the window is not real staleness. Once an
// inferred gating edge supplies a non-zero InferredAt, the entry emits using
// that InferredAt as usual. Audit rows are built through the real
// auditIssueDependencies write-site so the map key stays production-shaped.
func TestDeriveDependencyAttentionSkipsZeroBlockedSince(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	cfg := dependencyAttentionCfg(48)
	g := dependencyAttentionGraph("ENG-2")

	unknownIssue := domain.Issue{
		ID:         "550e8400-e29b-41d4-a716-446655440001",
		Identifier: "ENG-2",
		State:      "Todo",
		// blocker State nil -> DependencyAuditUnknown, so FirstBlockedAt is
		// never stamped by auditIssueDependencies (only Blocked status arms it).
		BlockedBy: []domain.BlockerRef{blockerRef("ENG-9", nil)},
	}

	t.Run("no inferred edge: zero BlockedSince must not escalate", func(t *testing.T) {
		state := &State{}
		entry := auditIssueDependencies(state, unknownIssue, now)
		require.Equal(t, DependencyAuditUnknown, entry.Status)
		require.True(t, entry.FirstBlockedAt.IsZero(), "precondition: FirstBlockedAt must never be stamped for Unknown status")
		require.NotEmpty(t, entry.UnresolvedBlockers, "precondition: blocker must still be unresolved")

		got := DeriveDependencyAttention(g, nil, *state, cfg, now)
		require.Empty(t, got, "zero blockedSince (no stamped FirstBlockedAt, no inferred edge) must not produce a stale_blocker entry")
	})

	t.Run("with a gating inferred edge: entry emitted using InferredAt", func(t *testing.T) {
		state := &State{}
		auditIssueDependencies(state, unknownIssue, now)
		inferredAt := now.Add(-100 * time.Hour)
		state.InferredDeps = map[string][]InferredDepEntry{
			"ENG-2": {{Source: "ENG-5", Gating: true, InferredAt: inferredAt}},
		}

		got := DeriveDependencyAttention(g, nil, *state, cfg, now)
		require.Len(t, got, 1, "a gating inferred edge with non-zero InferredAt must still escalate")
		require.Equal(t, "ENG-2", got[0].Identifier)
		require.True(t, got[0].BlockedSince.Equal(inferredAt))
		require.Equal(t, []string{"ENG-5", "ENG-9"}, got[0].Blockers, "union includes the unresolved tracker blocker even though it did not contribute a BlockedSince")
	})
}
