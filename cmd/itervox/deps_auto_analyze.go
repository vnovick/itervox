package main

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/server"
)

// depsAutoAnalyzeTickInterval is how often startDepsAutoAnalyze re-evaluates
// the auto-analyze decision. Independent of AutoAnalyzeDebounceMinutes /
// AutoAnalyzeMinIntervalMinutes (both minute-granularity) — a 60s tick gives
// the debounce/floor windows sub-minute resolution without busy-polling.
const depsAutoAnalyzeTickInterval = 60 * time.Second

// autoAnalyzeState is the scheduler's bookkeeping, carried across ticks by
// startDepsAutoAnalyze's goroutine-local variable. It is NOT orchestrator
// state — it never touches the event loop, never gets locked by cfgMu, and
// is not persisted across a daemon restart (a fresh zero value on every
// startup is fine: the first tick after boot re-derives everything it needs
// from the sidecar + snapshot).
type autoAnalyzeState struct {
	// lastAutoRun is the wall-clock time of the last successfully enqueued
	// auto-triggered analysis pass. Zero means "never". Set ONLY by the
	// wrapper (runDepsAutoAnalyzeTick), and only after
	// EnqueueAnalysisWithTrigger returns a nil error — evaluateAutoAnalyze
	// itself never assigns to this field. Consequence: a failed enqueue does
	// not consume the floor window, so the very next tick retries instead of
	// waiting out AutoAnalyzeMinIntervalMinutes for a pass that never ran.
	lastAutoRun time.Time
	// changeFirstSeen is when the current signal set was first observed.
	// Reset to the zero value whenever the signal set becomes empty, and
	// reset to `now` whenever the signal set's fingerprint changes — see
	// signalFingerprint and evaluateAutoAnalyze's "new changes restart the
	// quiet period" semantics.
	changeFirstSeen time.Time
	// warnedNoProfile latches true the first time evaluateAutoAnalyze is
	// called with profileResolves == false, so the wrapper logs the
	// "no profile configured" warning exactly once instead of every tick. It
	// never resets on its own — a daemon reload (fresh process) is what
	// clears it, via a fresh zero-value autoAnalyzeState.
	warnedNoProfile bool
	// signalFingerprint is the sorted, NUL-joined set of issue identifiers
	// evaluateAutoAnalyze most recently classified as "changed". It is not
	// part of the plan brief's terse struct listing, but persisting it is
	// required to implement the documented debounce semantic — "quiet-period
	// measures signal STABILITY ... track a hash of the signal set ... hash
	// change -> changeFirstSeen = now". evaluateAutoAnalyze is a pure
	// function with no memory of its own, so the previous tick's signal set
	// has to be handed back in via state for the next call to diff against.
	signalFingerprint string
}

// evaluateAutoAnalyze is the scheduler's pure decision core: every input is a
// parameter (including `now`), and it performs no I/O. See
// docs/superpowers/specs/2026-08-04-analyzer-autonomy-design.md's Scheduler
// section and .superpowers/sdd/analyzer_autonomy_plan/task-4-brief.md for the
// semantics implemented here.
//
// snap.CandidateSeen supplies the "snapshot issue" rows the change-signal
// rules operate over — one row per identifier tracker polling saw THIS tick,
// regardless of whether any dependency relation exists for it yet.
// (Fix round: the original implementation read snap.DependencyGraphNodes,
// which is empty until at least one dependency-audit or inferred-dependency
// row already exists — cmd/itervox/snapshot_rows.go's dependencyGraphRows
// returns nil,nil when both State.DependencyAudit and State.InferredDeps are
// empty. That made the "nil sidecar + non-empty backlog fires" first-run
// case unreachable on a fresh project with zero dependency relations, which
// is exactly the scenario auto-analyze exists to bootstrap. CandidateSeen —
// internal/orchestrator/candidate_seen.go, populated every onTick straight
// from the freshly fetched candidate-issue set — has no such precondition.)
//
// activeStates is the current cfg.Tracker.ActiveStates set, threaded in by
// the caller via the orchestrator's cfgMu-guarded TrackerStatesCfg accessor
// (never read cfg.Tracker.ActiveStates directly — it is runtime-mutable, see
// CLAUDE.md's cfgMu allowlist). It scopes rule 2 of
// autoAnalyzeSignalIdentifiers — see that function's doc comment for the
// scope-mismatch bug this fixes.
func evaluateAutoAnalyze(
	st autoAnalyzeState,
	snap server.StateSnapshot,
	sidecar *depsanalysis.Sidecar,
	cfg *config.Config,
	profileResolves bool,
	jobRunning bool,
	activeStates []string,
	now time.Time,
) (fire bool, next autoAnalyzeState) {
	next = st

	if cfg == nil || !cfg.Dependencies.AutoAnalyze {
		// Kill switch: short-circuit before touching any other state so a
		// disabled scheduler never accumulates a changeFirstSeen/fingerprint
		// history that would let it fire the instant it's re-enabled.
		return false, next
	}

	if !profileResolves {
		next.warnedNoProfile = true
		return false, next
	}

	ids := autoAnalyzeSignalIdentifiers(snap.CandidateSeen, sidecar, activeStates)
	fingerprint := strings.Join(ids, "\x00")

	if len(ids) == 0 {
		// No signal at all: reset the quiet-period clock so a later signal
		// starts its own debounce window from scratch instead of inheriting
		// a stale changeFirstSeen from a previous, now-irrelevant change.
		next.signalFingerprint = fingerprint
		next.changeFirstSeen = time.Time{}
		return false, next
	}

	if fingerprint != st.signalFingerprint {
		// The signal set changed since the last tick (new/removed/updated
		// issues) — restart the quiet period per the spec's "new changes
		// restart the debounce" rule.
		next.signalFingerprint = fingerprint
		next.changeFirstSeen = now
	}

	if jobRunning {
		return false, next
	}

	debounce := time.Duration(positiveOrDefault(cfg.Dependencies.AutoAnalyzeDebounceMinutes,
		config.DefaultDependenciesAutoAnalyzeDebounceMinutes)) * time.Minute
	floor := time.Duration(positiveOrDefault(cfg.Dependencies.AutoAnalyzeMinIntervalMinutes,
		config.DefaultDependenciesAutoAnalyzeMinIntervalMinutes)) * time.Minute

	if next.changeFirstSeen.IsZero() || now.Sub(next.changeFirstSeen) < debounce {
		return false, next
	}
	if !st.lastAutoRun.IsZero() && now.Sub(st.lastAutoRun) < floor {
		return false, next
	}

	return true, next
}

// positiveOrDefault mirrors config's positiveIntField fallback (<=0 becomes
// the default) so evaluateAutoAnalyze stays correct even when handed a cfg
// built directly by a test rather than one that went through config.Load.
func positiveOrDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// autoAnalyzeSignalIdentifiers computes the set of issue identifiers that
// count as a "change" per the spec's three rules:
//
//  1. a snapshot issue identifier absent from sidecar.Analyzed — including
//     every identifier when sidecar is nil (Sidecar.Analyzed then reads as
//     an empty map), so a nil sidecar with a non-empty candidate set
//     naturally signals on every identifier: "first-ever run fires".
//
//  2. an Analyzed key no longer present in the snapshot's candidate set AND
//     whose recorded AnalyzedIssue.State is in activeStates (the issue left
//     the ACTIVE backlog the analyzer scanned).
//
//     Fix round (CRITICAL, scope mismatch): candidates (snap.CandidateSeen)
//     is populated from orchestrator.State.CandidateSeen, which in turn comes
//     from tracker.FetchCandidateIssues — ACTIVE STATES ONLY. But
//     sidecar.Analyzed is rebuilt by MergeIncremental from
//     depsanalysis.FetchIssues' UNION fetch (active + terminal + backlog
//     states — see deps_analyzer_service.go's run() and
//     init_deps_analysis.go's runInitDepsAnalysis). Without the State scope
//     check below, every Done/Backlog issue the analyzer ever scanned is
//     permanently absent from the active-only candidate set, so rule 2 fired
//     on it every single tick forever — proven empirically at 24 fires in 24
//     quiescent hours. Scoping rule 2 to "was this Analyzed entry active
//     last time we saw it" makes a genuine completion produce exactly ONE
//     extra signal window (Analyzed still says active, issue now gone from
//     candidates) — one pass — after which the rebuilt entry records the new
//     (non-active) State and the signal clears for good. That single fire is
//     correct: completion is a real change worth one more pass. An entry
//     with an empty State (a sidecar written before this field existed, or
//     directly via a test) is treated as active — conservative, since it
//     costs at most one extra migration pass rather than silently
//     mis-scoping a pre-fix entry as terminal and losing its "removed"
//     signal forever.
//
//  3. a snapshot issue whose UpdatedAt is non-zero and after
//     sidecar.GeneratedAt. A zero UpdatedAt (tracker gave none) is skipped
//     rather than treated as a signal or an error, per the brief.
//
// The result is sorted so the caller's join-based fingerprint is stable
// regardless of map iteration order.
func autoAnalyzeSignalIdentifiers(candidates []server.CandidateSeenRow, sidecar *depsanalysis.Sidecar, activeStates []string) []string {
	var analyzed map[string]depsanalysis.AnalyzedIssue
	var generatedAt time.Time
	if sidecar != nil {
		analyzed = sidecar.Analyzed
		generatedAt = sidecar.GeneratedAt
	}

	active := make(map[string]struct{}, len(activeStates))
	for _, s := range activeStates {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		active[s] = struct{}{}
	}
	// isActiveState treats a blank/unrecorded State as active — see rule 2's
	// doc comment above for why that direction is the conservative one.
	isActiveState := func(state string) bool {
		if state == "" {
			return true
		}
		_, ok := active[strings.ToLower(strings.TrimSpace(state))]
		return ok
	}

	present := make(map[string]struct{}, len(candidates))
	signal := make(map[string]struct{})

	for _, c := range candidates {
		if c.Identifier == "" {
			continue
		}
		present[c.Identifier] = struct{}{}
		if _, ok := analyzed[c.Identifier]; !ok {
			signal[c.Identifier] = struct{}{}
			continue
		}
		if c.UpdatedAt.IsZero() {
			continue
		}
		if c.UpdatedAt.After(generatedAt) {
			signal[c.Identifier] = struct{}{}
		}
	}
	for id, entry := range analyzed {
		if _, ok := present[id]; ok {
			continue
		}
		if isActiveState(entry.State) {
			signal[id] = struct{}{}
		}
	}

	ids := make([]string, 0, len(signal))
	for id := range signal {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// depsAutoAnalyzeEnqueuer is the narrow surface startDepsAutoAnalyze needs
// from *depsAnalyzerService — split out so tests can substitute a fake
// without standing up a full service (tracker + orchestrator + JobManager).
type depsAutoAnalyzeEnqueuer interface {
	EnqueueAnalysisWithTrigger(profile, mode, trigger string) (string, time.Time, error)
	jobInFlight() bool
}

// jobInFlight reports whether the service's most recently submitted job is
// still queued or running. Backs the auto-analyze scheduler's jobRunning
// guard — a manual "Analyze dependencies" click, or an already-running
// auto-triggered pass, must not be raced by a second auto-triggered enqueue.
func (s *depsAnalyzerService) jobInFlight() bool {
	if s == nil || s.jobs == nil {
		return false
	}
	job, ok := s.jobs.Latest()
	if !ok || job == nil {
		return false
	}
	return job.Status == depsanalysis.JobQueued || job.Status == depsanalysis.JobRunning
}

// startDepsAutoAnalyze runs the scheduler goroutine: every
// depsAutoAnalyzeTickInterval it takes a fresh snapshot + sidecar read,
// evaluates evaluateAutoAnalyze, and enqueues an "auto"-triggered analysis
// pass when it fires. Started from run() in main.go beside startAutomations.
//
// snapshot mirrors the heartbeat/TUI wiring pattern
// (newHeartbeatWriter/statusui.Run both take the same `func()
// server.StateSnapshot` closure buildSnapFunc returns) rather than reading
// orch.Snapshot() directly, because evaluateAutoAnalyze's signal rules need
// the server-shaped CandidateSeen rows, not raw orchestrator.State.
//
// resolveProfile returns the currently configured deps-analyzer profile name
// and whether it currently resolves to an enabled profile — the caller wires
// this from orch.DepsAnalyzerProfileCfg()/AgentProfileCfg() so the scheduler
// picks up a runtime profile change without a daemon restart.
//
// activeStates returns the current cfg.Tracker.ActiveStates set on every
// call — the caller wires this from orch.TrackerStatesCfg() (cfgMu-guarded;
// ActiveStates is runtime-mutable via PUT /settings/tracker/states) so rule
// 2's scope check stays correct across a runtime states change, the same way
// resolveProfile stays correct across a runtime profile change.
func startDepsAutoAnalyze(
	ctx context.Context,
	svc depsAutoAnalyzeEnqueuer,
	resolveProfile func() (name string, resolves bool),
	snapshot func() server.StateSnapshot,
	sidecarCache *depsanalysis.SidecarCache,
	cfg *config.Config,
	activeStates func() []string,
) {
	if svc == nil || resolveProfile == nil || snapshot == nil || sidecarCache == nil || cfg == nil || activeStates == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(depsAutoAnalyzeTickInterval)
		defer ticker.Stop()
		var state autoAnalyzeState
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runDepsAutoAnalyzeTick(svc, resolveProfile, snapshot, sidecarCache, cfg, activeStates, &state, time.Now())
			}
		}
	}()
}

// runDepsAutoAnalyzeTick performs one scheduler evaluation and, if it fires,
// one enqueue attempt. Extracted from startDepsAutoAnalyze's goroutine body
// so tests can drive it directly with an injected `now` instead of waiting
// on a real 60s ticker.
func runDepsAutoAnalyzeTick(
	svc depsAutoAnalyzeEnqueuer,
	resolveProfile func() (name string, resolves bool),
	snapshot func() server.StateSnapshot,
	sidecarCache *depsanalysis.SidecarCache,
	cfg *config.Config,
	activeStates func() []string,
	state *autoAnalyzeState,
	now time.Time,
) {
	profile, resolves := resolveProfile()
	snap := snapshot()
	sidecar := sidecarCache.Latest()
	jobRunning := svc.jobInFlight()
	active := activeStates()

	prevWarned := state.warnedNoProfile
	fire, next := evaluateAutoAnalyze(*state, snap, sidecar, cfg, resolves, jobRunning, active, now)
	if next.warnedNoProfile && !prevWarned {
		slog.Warn("deps auto-analyze: configured profile does not resolve; auto-analyze paused until fixed",
			"profile", profile)
	}
	*state = next

	if !fire {
		return
	}

	id, _, err := svc.EnqueueAnalysisWithTrigger(profile, "auto", "auto")
	if err != nil {
		slog.Warn("deps auto-analyze: enqueue failed", "profile", profile, "error", err)
		return
	}
	// Record lastAutoRun only on a confirmed successful enqueue (see
	// autoAnalyzeState.lastAutoRun's doc comment) — a failed enqueue must not
	// consume the floor window.
	state.lastAutoRun = now
	slog.Info("deps auto-analyze: enqueued analysis pass", "profile", profile, "job_id", id)
}
