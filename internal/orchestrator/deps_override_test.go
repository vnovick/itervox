package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// depsOverrideTestConfig returns a config with a sidecar-gating-friendly
// dependencies policy and MaxConcurrentAgents=0 so onTick never attempts to
// actually dispatch a worker against the nil runner used by these tests —
// ReconcileInferredDeps runs unconditionally in onTick regardless of
// available dispatch slots, so this does not affect what's under test.
func depsOverrideTestConfig() *config.Config {
	cfg := dependencyAuditConfig()
	cfg.Agent.MaxConcurrentAgents = 0
	cfg.Dependencies = config.DependenciesConfig{
		InferredGating:      true,
		ConfidenceThreshold: 0.5,
		StalenessHours:      168,
	}
	return cfg
}

// depsOverrideSidecarFixture writes a single fresh, above-threshold,
// non-terminal-source sidecar edge ENG-1 -> ENG-2 to sidecarPath and returns
// a tracker seeded with both issues, non-terminal.
func depsOverrideSidecarFixture(t *testing.T, sidecarPath string, cfg *config.Config) *tracker.MemoryTracker {
	t.Helper()
	now := time.Now()
	sc := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: now,
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "issue body mentions ENG-1", Confidence: 0.9, InferredAt: now},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, sc))

	blocked := domain.Issue{ID: "issue-2", Identifier: "ENG-2", State: "Todo"}
	blocker := domain.Issue{ID: "issue-1", Identifier: "ENG-1", State: "Todo"}
	return tracker.NewMemoryTracker([]domain.Issue{blocked, blocker}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
}

// TestSetDepsOverrideRoundTrip drives a real orchestrator through Run() with
// a sidecar-backed gating entry. PollIntervalMs is set far longer than the
// test window so only the initial tick (time.NewTimer(0)) ever runs
// ReconcileInferredDeps — isolating the SetDepsOverride round trip to the
// EventSetDepsOverride handler's same-tick recompute (inferred_deps.go /
// deps_override.go), not a second onTick pass. If the handler stopped
// recomputing state.InferredDeps in place, this test would time out waiting.
func TestSetDepsOverrideRoundTrip(t *testing.T) {
	cfg := depsOverrideTestConfig()
	cfg.Polling.IntervalMs = 60_000 // effectively "only the initial tick" for this test's lifetime

	tmp := t.TempDir()
	sidecarPath := depsanalysis.SidecarPath(tmp)
	mt := depsOverrideSidecarFixture(t, sidecarPath, cfg)

	o := New(cfg, mt, nil, nil)
	o.SetDepsSidecarPath(sidecarPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		_ = o.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Baseline: the initial tick populates a gating entry.
	require.Eventually(t, func() bool {
		snap := o.Snapshot()
		entries := snap.InferredDeps["ENG-2"]
		return len(entries) == 1 && entries[0].Gating && !entries[0].Overridden
	}, 2*time.Second, 20*time.Millisecond, "expected baseline gating entry for ENG-2")

	require.True(t, o.SetDepsOverride("ENG-2", true), "SetDepsOverride(true) should queue")

	require.Eventually(t, func() bool {
		snap := o.Snapshot()
		entries := snap.InferredDeps["ENG-2"]
		if len(entries) != 1 {
			return false
		}
		_, overridden := snap.DepsOverrides["ENG-2"]
		return overridden && entries[0].Overridden && !entries[0].Gating
	}, 2*time.Second, 20*time.Millisecond, "expected override to dismiss gating same-tick")

	require.True(t, o.SetDepsOverride("ENG-2", false), "SetDepsOverride(false) should queue")

	require.Eventually(t, func() bool {
		snap := o.Snapshot()
		entries := snap.InferredDeps["ENG-2"]
		if len(entries) != 1 {
			return false
		}
		_, overridden := snap.DepsOverrides["ENG-2"]
		return !overridden && !entries[0].Overridden && entries[0].Gating
	}, 2*time.Second, 20*time.Millisecond, "expected gating restored same-tick after clearing override")
}

// TestDepsOverridePersistsAcrossRestart: an override set on one orchestrator
// instance is loaded by a fresh orchestrator instance pointed at the same
// overrides file, and ReconcileInferredDeps honors it once the sidecar edges
// load on the new instance's first tick (AUTO-4-style restart contract).
func TestDepsOverridePersistsAcrossRestart(t *testing.T) {
	cfg := depsOverrideTestConfig()
	cfg.Polling.IntervalMs = 20

	tmp := t.TempDir()
	sidecarPath := depsanalysis.SidecarPath(tmp)
	overridesPath := filepath.Join(tmp, "deps_overrides.json")

	// Daemon 1: set the override, wait for it to land, then stop.
	mt1 := depsOverrideSidecarFixture(t, sidecarPath, cfg)
	o1 := New(cfg, mt1, nil, nil)
	o1.SetDepsSidecarPath(sidecarPath)
	o1.SetDepsOverridesFile(overridesPath)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Second)
	done1 := make(chan struct{})
	go func() {
		_ = o1.Run(ctx1)
		close(done1)
	}()

	require.Eventually(t, func() bool {
		snap := o1.Snapshot()
		entries := snap.InferredDeps["ENG-2"]
		return len(entries) == 1 && entries[0].Gating
	}, 2*time.Second, 20*time.Millisecond, "daemon 1: expected baseline gating entry")

	require.True(t, o1.SetDepsOverride("ENG-2", true))

	require.Eventually(t, func() bool {
		snap := o1.Snapshot()
		_, overridden := snap.DepsOverrides["ENG-2"]
		return overridden
	}, 2*time.Second, 20*time.Millisecond, "daemon 1: expected override to be recorded")

	cancel1()
	<-done1

	// Daemon 2: fresh orchestrator + fresh tracker, same sidecar + overrides files.
	mt2 := tracker.NewMemoryTracker(
		[]domain.Issue{
			{ID: "issue-2", Identifier: "ENG-2", State: "Todo"},
			{ID: "issue-1", Identifier: "ENG-1", State: "Todo"},
		},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	o2 := New(cfg, mt2, nil, nil)
	o2.SetDepsSidecarPath(sidecarPath)
	o2.SetDepsOverridesFile(overridesPath)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	done2 := make(chan struct{})
	go func() {
		_ = o2.Run(ctx2)
		close(done2)
	}()
	t.Cleanup(func() {
		cancel2()
		<-done2
	})

	require.Eventually(t, func() bool {
		snap := o2.Snapshot()
		_, overridden := snap.DepsOverrides["ENG-2"]
		entries := snap.InferredDeps["ENG-2"]
		return overridden && len(entries) == 1 && entries[0].Overridden && !entries[0].Gating
	}, 2*time.Second, 20*time.Millisecond, "daemon 2: expected restored override to dismiss gating once edges load")
}

// TestDepsOverrideCorruptFileStartsEmpty locks the corrupt-file-starts-empty
// contract shared by every other Set*File loader (paused.json,
// input_required.json, automation_queue.json): a malformed overrides file
// must not panic startup, and must leave DepsOverrides empty (not nil).
func TestDepsOverrideCorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deps_overrides.json")
	require.NoError(t, os.WriteFile(path, []byte("{ this is not valid json"), 0o644))

	cfg := depsOverrideTestConfig()
	o := New(cfg, nil, nil, nil)
	o.SetDepsOverridesFile(path)

	state := o.loadDepsOverridesFromDisk(NewState(cfg))

	require.NotNil(t, state.DepsOverrides)
	require.Empty(t, state.DepsOverrides)
}

// TestStoreSnapDeepCopiesDepsOverrides mirrors
// TestStoreSnapDeepCopiesInferredDeps: reads o.lastSnap directly so a bug
// that lets storeSnap alias lastSnap.DepsOverrides to the live event-loop map
// cannot hide behind Snapshot()'s independent re-copy (Snapshot() always
// clones whatever it finds in lastSnap, masking an aliasing bug inside
// storeSnap itself).
func TestStoreSnapDeepCopiesDepsOverrides(t *testing.T) {
	o := &Orchestrator{}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	state := State{DepsOverrides: map[string]time.Time{"ENG-2": now}}

	o.storeSnap(state)

	state.DepsOverrides["ENG-2"] = now.Add(time.Hour)
	state.DepsOverrides["ENG-3"] = now

	o.snapMu.RLock()
	got := o.lastSnap.DepsOverrides
	o.snapMu.RUnlock()

	require.NotContains(t, got, "ENG-3", "storeSnap aliases the live DepsOverrides map")
	require.Equal(t, now, got["ENG-2"], "storeSnap aliases the live DepsOverrides map")
}
