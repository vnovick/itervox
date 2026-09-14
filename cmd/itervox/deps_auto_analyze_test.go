package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/server"
)

// ─── test helpers ───────────────────────────────────────────────────────────

func autoAnalyzeTestCfg(debounceMin, floorMin int) *config.Config {
	return &config.Config{
		Dependencies: config.DependenciesConfig{
			AutoAnalyze:                   true,
			AutoAnalyzeDebounceMinutes:    debounceMin,
			AutoAnalyzeMinIntervalMinutes: floorMin,
		},
	}
}

// snapWithIdentifiers builds a snapshot whose CandidateSeen rows are the
// given identifiers with a zero UpdatedAt (as if the tracker gave none).
// CandidateSeen — not DependencyGraphNodes — is evaluateAutoAnalyze's signal
// source: DependencyGraphNodes is only populated once a dependency relation
// already exists (see deps_auto_analyze.go's doc comment on
// autoAnalyzeSignalIdentifiers), which would make every fixture in this file
// implicitly depend on dependency-audit/inferred-dep wiring it has nothing to
// do with.
func snapWithIdentifiers(ids ...string) server.StateSnapshot {
	rows := make([]server.CandidateSeenRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, server.CandidateSeenRow{Identifier: id})
	}
	return server.StateSnapshot{CandidateSeen: rows}
}

// snapWithCandidateSeen builds a snapshot from explicit CandidateSeenRow
// values, for tests that need to control UpdatedAt directly.
func snapWithCandidateSeen(rows ...server.CandidateSeenRow) server.StateSnapshot {
	return server.StateSnapshot{CandidateSeen: rows}
}

// sidecarWithAnalyzed builds a sidecar whose Analyzed map already contains
// the given identifiers (so they do NOT count as a change-signal under rule
// 1), stamped with generatedAt. Every entry's State is left blank — tests
// that need to exercise rule 2's active-state scoping use
// sidecarWithAnalyzedStates instead.
func sidecarWithAnalyzed(generatedAt time.Time, ids ...string) *depsanalysis.Sidecar {
	analyzed := make(map[string]depsanalysis.AnalyzedIssue, len(ids))
	for _, id := range ids {
		analyzed[id] = depsanalysis.AnalyzedIssue{AnalyzedAt: generatedAt}
	}
	return &depsanalysis.Sidecar{GeneratedAt: generatedAt, Analyzed: analyzed}
}

// sidecarWithAnalyzedStates builds a sidecar whose Analyzed map carries an
// explicit per-identifier State — the field rule 2's scope-mismatch fix
// reads to decide whether an Analyzed entry absent from the candidate set
// counts as "issue left the ACTIVE backlog" (signal) or "issue already
// terminal, sidecar just hasn't been told the candidate set shrank for a
// state-irrelevant reason" (no signal).
func sidecarWithAnalyzedStates(generatedAt time.Time, entries map[string]string) *depsanalysis.Sidecar {
	analyzed := make(map[string]depsanalysis.AnalyzedIssue, len(entries))
	for id, state := range entries {
		analyzed[id] = depsanalysis.AnalyzedIssue{AnalyzedAt: generatedAt, State: state}
	}
	return &depsanalysis.Sidecar{GeneratedAt: generatedAt, Analyzed: analyzed}
}

// ─── evaluateAutoAnalyze — pure core ────────────────────────────────────────

func TestAutoAnalyzeFiresAfterDebounce(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzed(base, "ENG-001") // ENG-002 unseen -> signal
	snap := snapWithIdentifiers("ENG-001", "ENG-002")

	var st autoAnalyzeState

	now := base.Add(time.Minute)
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, now)
	require.False(t, fire, "must not fire the instant a signal is first observed")
	require.Equal(t, now, next.changeFirstSeen)
	st = next

	now2 := now.Add(4 * time.Minute) // 4 of 5 debounce minutes elapsed
	fire, next = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, now2)
	require.False(t, fire, "must not fire before the debounce window elapses")
	st = next

	now3 := now.Add(5 * time.Minute) // exactly the debounce window since first-seen
	fire, _ = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, now3)
	require.True(t, fire, "must fire once the signal has been stable for the full debounce window")
}

func TestAutoAnalyzeDebounceRestartsOnNewChanges(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzed(base) // nothing analyzed yet

	var st autoAnalyzeState

	now := base.Add(time.Minute)
	snap1 := snapWithIdentifiers("ENG-001")
	fire, next := evaluateAutoAnalyze(st, snap1, sidecar, cfg, true, false, nil, now)
	require.False(t, fire)
	require.Equal(t, now, next.changeFirstSeen)
	st = next

	// 4 minutes later (short of the 5-minute debounce) a NEW identifier
	// appears in the signal set — this must restart the quiet period rather
	// than let the original changeFirstSeen carry the pass over the line.
	now2 := now.Add(4 * time.Minute)
	snap2 := snapWithIdentifiers("ENG-001", "ENG-002")
	fire, next = evaluateAutoAnalyze(st, snap2, sidecar, cfg, true, false, nil, now2)
	require.False(t, fire)
	require.Equal(t, now2, next.changeFirstSeen, "a changed signal set must restart changeFirstSeen")
	st = next

	// 4 minutes after the restart: still short of a full debounce window
	// measured from the restart.
	now3 := now2.Add(4 * time.Minute)
	fire, next = evaluateAutoAnalyze(st, snap2, sidecar, cfg, true, false, nil, now3)
	require.False(t, fire, "only 4 of 5 debounce minutes have elapsed since the restart")
	st = next

	// 5 minutes after the restart: fires.
	now4 := now2.Add(5 * time.Minute)
	fire, _ = evaluateAutoAnalyze(st, snap2, sidecar, cfg, true, false, nil, now4)
	require.True(t, fire)
}

func TestAutoAnalyzeRespectsFloor(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Simulate an auto-triggered pass that already ran at `base`.
	st := autoAnalyzeState{lastAutoRun: base}

	sidecar := sidecarWithAnalyzed(base) // nothing analyzed -> ENG-001 signals
	snap := snapWithIdentifiers("ENG-001")

	changeAt := base.Add(time.Minute)
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, changeAt)
	require.False(t, fire)
	st = next

	// The debounce window (5 min) elapses well before the floor (60 min
	// since lastAutoRun == base).
	afterDebounce := changeAt.Add(6 * time.Minute) // base+7m
	fire, next = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, afterDebounce)
	require.False(t, fire, "debounce alone is not enough — the floor since lastAutoRun has not elapsed")
	st = next

	// Once 60 minutes have passed since lastAutoRun, it fires.
	afterFloor := base.Add(61 * time.Minute)
	fire, _ = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, afterFloor)
	require.True(t, fire)
}

func TestAutoAnalyzeKillSwitch(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	cfg.Dependencies.AutoAnalyze = false
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzed(base)
	snap := snapWithIdentifiers("ENG-001")

	// Pretend the signal has already been stable for a long time and the
	// floor is trivially satisfied — the kill switch alone must still block.
	st := autoAnalyzeState{changeFirstSeen: base.Add(-time.Hour)}
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, base.Add(24*time.Hour))
	require.False(t, fire)
	require.Equal(t, st, next, "kill switch must short-circuit before touching any other state")
}

func TestAutoAnalyzeNoProfileWarnsOnce(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := snapWithIdentifiers("ENG-001")

	var st autoAnalyzeState
	fire, next := evaluateAutoAnalyze(st, snap, nil, cfg, false /* profileResolves */, false, nil, base)
	require.False(t, fire)
	require.True(t, next.warnedNoProfile, "must latch true the first time the profile fails to resolve")
	st = next

	// A later tick with the profile still unresolved must not un-latch it —
	// this is what lets the wrapper (runDepsAutoAnalyzeTick) log the warning
	// exactly once by comparing prevWarned to next.warnedNoProfile.
	fire, next = evaluateAutoAnalyze(st, snap, nil, cfg, false, false, nil, base.Add(time.Hour))
	require.False(t, fire)
	require.True(t, next.warnedNoProfile)
}

// TestAutoAnalyzeNilSidecarNonEmptyBacklogFires proves the genuinely fresh-
// project case: a snapshot with CandidateSeen rows but NO dependency-audit or
// inferred-dependency data at all (snap.DependencyGraphNodes/DependencyAudit
// are left as their zero value / unset — nothing in this fixture touches
// them) must still fire once nil-sidecar + non-empty CandidateSeen has been
// stable for the debounce window. This is exactly the scenario the fix round
// exists for: before it, evaluateAutoAnalyze read snap.DependencyGraphNodes,
// which stays empty until a dependency relation already exists, so this case
// was unreachable on a fresh backlog.
func TestAutoAnalyzeNilSidecarNonEmptyBacklogFires(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := snapWithIdentifiers("ENG-001", "ENG-002")
	require.Empty(t, snap.DependencyGraphNodes, "fixture must carry zero dependency-graph rows — this is the fresh-project case")
	require.Empty(t, snap.DependencyAudit, "fixture must carry zero dependency-audit rows — this is the fresh-project case")

	var st autoAnalyzeState
	fire, next := evaluateAutoAnalyze(st, snap, nil /* sidecar */, cfg, true, false, nil, base)
	require.False(t, fire, "signal just observed — must debounce first")
	require.Equal(t, base, next.changeFirstSeen)
	st = next

	fire, _ = evaluateAutoAnalyze(st, snap, nil, cfg, true, false, nil, base.Add(5*time.Minute))
	require.True(t, fire, "nil sidecar + non-empty backlog is a signal (first-ever run) and must eventually fire, "+
		"even with zero dependency-graph/audit rows")
}

// TestAutoAnalyzeUpdatedAtChangeTriggers proves rule 3: an identifier set
// unchanged from what the sidecar already analyzed, but with one issue's
// CandidateSeen UpdatedAt newer than sidecar.GeneratedAt, is a signal that
// fires after the debounce window — content changed since the last analysis
// pass, even though no issue entered/left the backlog.
func TestAutoAnalyzeUpdatedAtChangeTriggers(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzed(base, "ENG-001", "ENG-002") // both already analyzed as of base

	changedAt := base.Add(90 * time.Minute) // after sidecar.GeneratedAt
	snap := snapWithCandidateSeen(
		server.CandidateSeenRow{Identifier: "ENG-001"}, // unchanged, no UpdatedAt
		server.CandidateSeenRow{Identifier: "ENG-002", UpdatedAt: changedAt},
	)

	var st autoAnalyzeState
	now := changedAt.Add(time.Minute)
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, now)
	require.False(t, fire, "signal just observed — must debounce first")
	require.Equal(t, now, next.changeFirstSeen)
	st = next

	fire, _ = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, nil, now.Add(5*time.Minute))
	require.True(t, fire, "an UpdatedAt newer than sidecar.GeneratedAt is a signal even with an unchanged identifier set")
}

func TestAutoAnalyzeSkipsWhileJobRunning(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzed(base) // nothing analyzed -> ENG-001 signals
	snap := snapWithIdentifiers("ENG-001")

	// Signal already stable for an hour and floor trivially satisfied
	// (lastAutoRun zero) — only jobRunning should block firing.
	st := autoAnalyzeState{
		changeFirstSeen:   base.Add(-time.Hour),
		signalFingerprint: "ENG-001",
	}
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, true /* jobRunning */, nil, base)
	require.False(t, fire, "must not fire while a job is already in flight")
	require.Equal(t, st.changeFirstSeen, next.changeFirstSeen, "jobRunning must not disturb the debounce clock")
}

// TestAutoAnalyzeTerminalAnalyzedEntriesDoNotSignal is the fix-round
// regression test for the CRITICAL scope-mismatch bug: sidecar.Analyzed is
// rebuilt from depsanalysis.FetchIssues' UNION fetch (active + terminal +
// backlog states), but snap.CandidateSeen is populated from
// tracker.FetchCandidateIssues — ACTIVE STATES ONLY. Before this fix, rule 2
// signaled on EVERY Analyzed entry absent from the candidate set, so a
// completed issue the analyzer had already scanned became a PERMANENT
// signal — proven empirically at 24 fires in 24 quiescent hours on a project
// with a completed issue. This test drives several floor/debounce windows
// with no change to the underlying data and requires zero fires throughout,
// which is red pre-fix (the old unscoped rule 2 fires on the very first
// window once ENG-2's absence has been stable for the debounce period).
func TestAutoAnalyzeTerminalAnalyzedEntriesDoNotSignal(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sidecar := sidecarWithAnalyzedStates(base, map[string]string{
		"ENG-1": "In Progress", // present in candidates -> not a rule-2 candidate at all
		"ENG-2": "Done",        // absent from candidates, but terminal -> must not signal
	})
	snap := snapWithIdentifiers("ENG-1")
	activeStates := []string{"In Progress", "Todo"}

	var st autoAnalyzeState
	now := base
	for i := 0; i < 5; i++ { // span several debounce/floor windows — a quiescent sequence
		now = now.Add(90 * time.Minute)
		fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, activeStates, now)
		require.Falsef(t, fire, "a terminal Analyzed entry absent from candidates must never signal (round %d)", i)
		st = next
	}
}

// TestAutoAnalyzeCompletionFiresOnce proves the fix's documented semantics
// (deps_auto_analyze.go's rule-2 doc comment): an issue that completes
// produces exactly ONE extra signal window — Analyzed still records it
// active while it's absent from the candidate set — after which the
// simulated analysis pass rewrites Analyzed with the terminal state and the
// signal clears for good.
func TestAutoAnalyzeCompletionFiresOnce(t *testing.T) {
	cfg := autoAnalyzeTestCfg(5, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// ENG-2 was analyzed while still active; it has since completed and left
	// the active candidate set, but no analysis pass has run since, so the
	// sidecar still records it as active.
	sidecar := sidecarWithAnalyzedStates(base, map[string]string{"ENG-2": "In Progress"})
	snap := snapWithIdentifiers() // ENG-2 no longer a candidate
	activeStates := []string{"In Progress", "Todo"}

	var st autoAnalyzeState
	now := base.Add(time.Minute)
	fire, next := evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, activeStates, now)
	require.False(t, fire, "must debounce before firing")
	st = next

	now2 := now.Add(6 * time.Minute) // past the 5-minute debounce
	fire, next = evaluateAutoAnalyze(st, snap, sidecar, cfg, true, false, activeStates, now2)
	require.True(t, fire, "a completed issue still recorded active in the sidecar must fire exactly once")
	st = next

	// Simulate the triggered pass: MergeIncremental rebuilds Analyzed from the
	// UNION fetch and records ENG-2's now-terminal state — this is the sidecar
	// the next tick would read off disk.
	postPass := sidecarWithAnalyzedStates(now2, map[string]string{"ENG-2": "Done"})

	for i, delta := range []time.Duration{2 * time.Hour, 4 * time.Hour, 6 * time.Hour} {
		later := now2.Add(delta)
		fire, next = evaluateAutoAnalyze(st, snap, postPass, cfg, true, false, activeStates, later)
		require.Falsef(t, fire, "must never fire again once the sidecar records the completed state (round %d)", i)
		st = next
	}
}

// ─── runDepsAutoAnalyzeTick — thin wrapper, driven via a fake enqueuer ─────

type fakeAutoAnalyzeCall struct{ profile, mode, trigger string }

type fakeAutoAnalyzeEnqueuer struct {
	calls   []fakeAutoAnalyzeCall
	err     error
	running bool
}

func (f *fakeAutoAnalyzeEnqueuer) EnqueueAnalysisWithTrigger(profile, mode, trigger string) (string, time.Time, error) {
	f.calls = append(f.calls, fakeAutoAnalyzeCall{profile, mode, trigger})
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "job-1", time.Now(), nil
}

func (f *fakeAutoAnalyzeEnqueuer) jobInFlight() bool { return f.running }

// noActiveStates is the activeStates accessor most wrapper tests use — they
// exercise rule 1/3 signals, not rule 2's scoping, so an empty set is
// sufficient (and mirrors a config with ActiveStates unset).
func noActiveStates() []string { return nil }

// TestAutoAnalyzeWrapperEnqueuesOnFire proves the wrapper — not just the pure
// core — actually calls EnqueueAnalysisWithTrigger("<profile>", "auto",
// "auto") when evaluateAutoAnalyze fires, and records lastAutoRun afterward.
func TestAutoAnalyzeWrapperEnqueuesOnFire(t *testing.T) {
	cfg := autoAnalyzeTestCfg(1, 1)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := func() server.StateSnapshot { return snapWithIdentifiers("ENG-001") }
	// No file on disk -> SidecarCache.Latest() returns nil, matching the
	// nil-sidecar "first-ever run" case.
	sidecarCache := depsanalysis.NewSidecarCache(filepath.Join(t.TempDir(), "deps-sidecar.json"))
	fake := &fakeAutoAnalyzeEnqueuer{}
	resolveProfile := func() (string, bool) { return "deps-analyzer", true }

	var state autoAnalyzeState
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, noActiveStates, &state, base)
	require.Empty(t, fake.calls, "must not enqueue before the debounce window elapses")

	later := base.Add(2 * time.Minute) // past both the 1-min debounce and 1-min floor
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, noActiveStates, &state, later)
	require.Len(t, fake.calls, 1)
	assert.Equal(t, fakeAutoAnalyzeCall{profile: "deps-analyzer", mode: "auto", trigger: "auto"}, fake.calls[0])
	assert.False(t, state.lastAutoRun.IsZero(), "wrapper must record lastAutoRun after a successful enqueue")
}

// TestAutoAnalyzeWrapperDoesNotRecordLastAutoRunOnEnqueueError proves the
// documented choice (see autoAnalyzeState.lastAutoRun's doc comment): a
// failed enqueue must not consume the floor window, so the scheduler retries
// on the very next tick instead of waiting out AutoAnalyzeMinIntervalMinutes.
func TestAutoAnalyzeWrapperDoesNotRecordLastAutoRunOnEnqueueError(t *testing.T) {
	cfg := autoAnalyzeTestCfg(1, 1)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := func() server.StateSnapshot { return snapWithIdentifiers("ENG-001") }
	sidecarCache := depsanalysis.NewSidecarCache(filepath.Join(t.TempDir(), "deps-sidecar.json"))
	fake := &fakeAutoAnalyzeEnqueuer{err: errors.New("boom")}
	resolveProfile := func() (string, bool) { return "deps-analyzer", true }

	var state autoAnalyzeState
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, noActiveStates, &state, base)
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, noActiveStates, &state, base.Add(2*time.Minute))

	require.Len(t, fake.calls, 1)
	require.True(t, state.lastAutoRun.IsZero(), "a failed enqueue must not consume the floor window")
}

// TestAutoAnalyzeWrapperThreadsActiveStates proves the wrapper calls the
// injected activeStates accessor and feeds its result into
// evaluateAutoAnalyze's rule-2 scoping — a terminal Analyzed entry absent
// from the candidate set must not fire even through the full wrapper path,
// not just the pure core.
func TestAutoAnalyzeWrapperThreadsActiveStates(t *testing.T) {
	cfg := autoAnalyzeTestCfg(1, 1)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	sidecarPath := filepath.Join(dir, "deps-sidecar.json")
	sidecar := sidecarWithAnalyzedStates(base, map[string]string{"ENG-2": "Done"})
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, sidecar))

	snapshot := func() server.StateSnapshot { return snapWithIdentifiers() } // ENG-2 absent
	sidecarCache := depsanalysis.NewSidecarCache(sidecarPath)
	fake := &fakeAutoAnalyzeEnqueuer{}
	resolveProfile := func() (string, bool) { return "deps-analyzer", true }
	activeStates := func() []string { return []string{"In Progress", "Todo"} }

	var state autoAnalyzeState
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, activeStates, &state, base)
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, activeStates, &state, base.Add(2*time.Minute))
	runDepsAutoAnalyzeTick(fake, resolveProfile, snapshot, sidecarCache, cfg, activeStates, &state, base.Add(4*time.Minute))

	require.Empty(t, fake.calls, "a terminal Analyzed entry must never fire through the wrapper path")
}
