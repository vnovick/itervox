package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// wireDepsAnalyzerService constructs the deps-analyzer service and returns it
// alongside a bind function the caller invokes once `srv.Notify` is available.
// The two-phase wiring is necessary because the analyzer's completion callback
// must point at the server's broadcaster, but the server itself depends on the
// analyzer at construction time.
func wireDepsAnalyzerService(
	ctx context.Context,
	orch *orchestrator.Orchestrator,
	cfg *config.Config,
	tr tracker.Tracker,
	runner agent.Runner,
	workflowPath string,
	agentLogDir string,
) (svc *depsAnalyzerService, bindNotify func(func())) {
	var notifyTarget func()
	svc = newDepsAnalyzerService(
		ctx, orch, cfg, tr, runner,
		depsanalysis.SidecarPath(filepath.Dir(workflowPath)),
		agentLogDir,
		func() {
			if notifyTarget != nil {
				notifyTarget()
			}
		},
	)
	bindNotify = func(fn func()) { notifyTarget = fn }
	return svc, bindNotify
}

// depsAnalyzerService backs the /api/v1/deps/analyze endpoints (Phase 2.3).
// It owns the in-memory JobManager and runs the analyzer pass on a separate
// daemon-scoped goroutine so the HTTP request can return immediately with a
// 202.
type depsAnalyzerService struct {
	ctx         context.Context
	orch        *orchestrator.Orchestrator
	cfg         *config.Config
	tracker     tracker.Tracker
	runner      agent.Runner
	sidecarPath string
	agentLogDir string
	logger      *slog.Logger
	notify      func()
	jobs        *depsanalysis.JobManager
}

// newDepsAnalyzerService wires the service. notify is invoked after a
// successful analysis pass so the SSE broadcaster reloads the snapshot.
func newDepsAnalyzerService(
	ctx context.Context,
	orch *orchestrator.Orchestrator,
	cfg *config.Config,
	tr tracker.Tracker,
	runner agent.Runner,
	sidecarPath string,
	agentLogDir string,
	notify func(),
) *depsAnalyzerService {
	s := &depsAnalyzerService{
		ctx:         ctx,
		orch:        orch,
		cfg:         cfg,
		tracker:     tr,
		runner:      runner,
		sidecarPath: sidecarPath,
		agentLogDir: agentLogDir,
		notify:      notify,
		logger:      slog.Default().With("component", "deps_analyzer"),
	}
	s.jobs = depsanalysis.NewJobManager(s.run,
		time.Duration(cfg.Agent.DepsAnalyzerTimeoutMs)*time.Millisecond)
	// Wake every SSE subscriber on every analyzer-job
	// transition (queued→running and terminal) so the dashboard repaints
	// without waiting for its polling fallback. The frontend then re-fetches
	// the snapshot, which carries DepsLastAnalyzedAt + the inferred-edge set.
	s.jobs.SetOnTransition(func(job *depsanalysis.Job) {
		if s == nil || s.notify == nil || job == nil {
			return
		}
		s.notify()
	})
	return s
}

// DefaultProfile satisfies the server.DepsAnalyzer interface.
func (s *depsAnalyzerService) DefaultProfile() string {
	if s == nil || s.orch == nil {
		return ""
	}
	return s.orch.DepsAnalyzerProfileCfg()
}

// depsAnalyzeTriggerManual labels a job's provenance (Task 4's Trigger
// provenance requirement). It covers every path that reaches EnqueueAnalysis
// today (dashboard button, direct API call). Task 4's scheduler goroutine
// (cmd/itervox/deps_auto_analyze.go's runDepsAutoAnalyzeTick, wired in
// main.go beside startAutomations) calls EnqueueAnalysisWithTrigger directly
// against the concrete service with trigger "auto" instead.
const depsAnalyzeTriggerManual = "manual"

// EnqueueAnalysis satisfies the server.DepsAnalyzer interface. mode is the
// requested incremental-pass mode ("auto" | "full" | "incremental"); empty
// behaves like "auto". Every call through this interface method is a manual
// (operator-initiated) trigger — see EnqueueAnalysisWithTrigger for the
// scheduler's "auto" entry point.
func (s *depsAnalyzerService) EnqueueAnalysis(profile, mode string) (string, time.Time, error) {
	return s.EnqueueAnalysisWithTrigger(profile, mode, depsAnalyzeTriggerManual)
}

// EnqueueAnalysisWithTrigger is EnqueueAnalysis plus an explicit trigger
// label. It is a distinct exported method — rather than adding trigger to
// EnqueueAnalysis's signature — because server.DepsAnalyzer only ever
// represents manual runs; Task 4's scheduler goroutine holds this concrete
// *depsAnalyzerService (not the interface) and calls this directly with
// trigger "auto".
func (s *depsAnalyzerService) EnqueueAnalysisWithTrigger(profile, mode, trigger string) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("deps analyzer service is nil")
	}
	if _, ok := s.lookupProfile(profile); !ok {
		return "", time.Time{}, fmt.Errorf("profile %q is not configured or is disabled", profile)
	}
	// Hand the daemon's context to the job manager so the analyzer keeps
	// running after the HTTP request returns. Per-pass cancellation happens
	// via daemon shutdown.
	id, err := s.jobs.EnqueueWithOptions(s.ctx, profile, depsanalysis.EnqueueOptions{Mode: mode, Trigger: trigger})
	if err != nil {
		return "", time.Time{}, err
	}
	return id, time.Now().UTC(), nil
}

// Status satisfies the server.DepsAnalyzer interface.
func (s *depsAnalyzerService) Status(id string) (server.DepsAnalyzeJobRow, bool) {
	if s == nil || s.jobs == nil {
		return server.DepsAnalyzeJobRow{}, false
	}
	job, ok := s.jobs.Status(id)
	if !ok {
		return server.DepsAnalyzeJobRow{}, false
	}
	return jobRowFromJob(job), true
}

// CurrentJob returns the JobManager's most recently submitted job — running
// or terminal — as a wire row, for the snapshot's DepsAnalyzeJob field
// (#46-1). Unlike Status, it takes no job ID: this is "whichever job is
// current" rather than a specific one, mirroring currentJobID/currentJobMode
// below. false when no job has ever run this process lifetime.
func (s *depsAnalyzerService) CurrentJob() (server.DepsAnalyzeJobRow, bool) {
	if s == nil || s.jobs == nil {
		return server.DepsAnalyzeJobRow{}, false
	}
	job, ok := s.jobs.Latest()
	if !ok || job == nil {
		return server.DepsAnalyzeJobRow{}, false
	}
	return jobRowFromJob(job), true
}

// CancelAnalysis satisfies the server.DepsAnalyzer interface.
func (s *depsAnalyzerService) CancelAnalysis(jobID string) bool {
	if s == nil || s.jobs == nil {
		return false
	}
	return s.jobs.Cancel(jobID)
}

func (s *depsAnalyzerService) run(ctx context.Context, profile string) (*depsanalysis.JobResult, error) {
	prof, ok := s.lookupProfile(profile)
	if !ok {
		return nil, fmt.Errorf("depsanalysis: profile %q vanished between enqueue and run", profile)
	}
	// cfg.Tracker.ActiveStates/TerminalStates are cfgMu-guarded (runtime
	// setter: SetTrackerStatesCfg via PUT /settings/tracker/states) and this
	// pass runs on a daemon-scoped goroutine well after startup (triggered by
	// EnqueueAnalysis), so read via the getter. BacklogStates has no runtime
	// setter, so the direct cfg read stays legal.
	active, terminal, _ := s.orch.TrackerStatesCfg()
	states := depsanalysis.DedupeStateNames(active, terminal, s.cfg.Tracker.BacklogStates)

	issues, trackerEdges, err := depsanalysis.FetchIssues(ctx, s.tracker, states)
	if err != nil {
		return nil, fmt.Errorf("fetch issues: %w", err)
	}

	prev, err := depsanalysis.LoadSidecar(s.sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("load sidecar: %w", err)
	}

	// Empty-fetch guard (mandatory; see docs/superpowers/specs/2026-08-04-
	// analyzer-autonomy-design.md "Empty-fetch guard"). A fetch that comes
	// back with zero issues while the prior sidecar still carries inferred
	// edges is far more likely to be a transient tracker hiccup than a
	// genuinely emptied backlog — write nothing, and let the next non-empty
	// pass reconcile. Writing here would silently destroy those edges,
	// which matters more now that scheduled auto-analysis (Task 4) can hit
	// this path unattended. The JobResult carries the SAME sidecar that is
	// still on disk (Sidecar: prev), not a synthetic empty one — a caller
	// reading res.Sidecar.Edges sees the true current state, not a lie.
	if len(issues) == 0 && prev != nil && len(prev.Edges) > 0 {
		s.logger.Warn(fmt.Sprintf(
			"deps analyzer empty-fetch guard: refusing to overwrite %d inferred edges with an empty fetch",
			len(prev.Edges)),
			"profile", profile)
		return &depsanalysis.JobResult{
			Sidecar:       prev,
			IssuesScanned: 0,
			ChunksTotal:   0,
		}, nil
	}

	mode := s.currentJobMode()
	plan := depsanalysis.PlanIncremental(issues, prev, profile, mode)
	s.logger.Info("deps analyzer plan",
		"profile", profile, "requested_mode", mode, "resolved_mode", plan.Mode,
		"to_analyze", len(plan.ToAnalyze), "unchanged", len(plan.Unchanged))

	chunkSize := s.cfg.Agent.DepsAnalyzerChunkSize
	if chunkSize <= 0 {
		chunkSize = config.DefaultDepsAnalyzerChunkSize
	}

	logDir := ""
	if s.agentLogDir != "" {
		logDir = filepath.Join(s.agentLogDir, "deps-analyzer")
	}

	// The chunk/scope/fail-atomically/dedupe loop lives in
	// depsanalysis.RunChunkedAgentPass (see internal/depsanalysis/chunked_pass.go)
	// — shared with init_deps_analysis.go's runInitDepsAnalysis (the
	// `itervox init` / `itervox deps analyze` one-shot path) since #47. Do
	// not re-inline a copy here; extend the shared helper instead.
	var chunksTotal int
	onChunkDone := func(done, total int) {
		if done == 0 {
			chunksTotal = total
			// Set the total as soon as it's known, before the per-chunk loop
			// starts, so a job observed mid-run carries a correct
			// ChunksTotal instead of the zero value that would otherwise
			// persist until the job goes terminal.
			s.jobs.SetChunksTotal(s.currentJobID(), total)
			return
		}
		s.jobs.MarkProgress(s.currentJobID(), done)
	}
	all, err := depsanalysis.RunChunkedAgentPass(ctx, depsanalysis.AgentPassInput{
		Runner:        s.runner,
		Profile:       prof,
		ProfileName:   profile,
		WorkspacePath: s.cfg.Workspace.Root,
		LogDir:        logDir,
		Logger:        s.logger,
		OnProgress:    func(agent.TurnResult) { s.jobs.MarkProgress(s.currentJobID(), -1) },
		ReadTimeoutMs: s.cfg.Agent.ReadTimeoutMs,
		TurnTimeoutMs: s.cfg.Agent.TurnTimeoutMs,
	}, plan.ToAnalyze, trackerEdges, chunkSize, onChunkDone)
	if err != nil {
		return nil, err
	}

	sc := depsanalysis.MergeIncremental(prev, plan, all, issues, profile, time.Now().UTC())
	if err := depsanalysis.SaveSidecar(s.sidecarPath, sc); err != nil {
		return nil, fmt.Errorf("save sidecar: %w", err)
	}
	if s.notify != nil {
		s.notify()
	}
	// #52 IssuesScanned honesty — len(issues) is the raw tracker-fetch count,
	// which under incremental mode can be far larger than what actually went
	// to the agent (plan.ToAnalyze): a revalidation-only run with zero
	// content changes previously reported "analyzed 50" while sending 0
	// issues to the agent. Report all three counts explicitly.
	s.logger.Info(fmt.Sprintf("deps analyzer succeeded: scanned %d issues (%d analyzed, %d revalidated)",
		len(issues), len(plan.ToAnalyze), len(plan.Unchanged)),
		"profile", profile, "mode", plan.Mode, "edges", len(sc.Edges), "chunks", chunksTotal)

	return &depsanalysis.JobResult{
		Sidecar:        sc,
		IssuesScanned:  len(issues),
		IssuesAnalyzed: len(plan.ToAnalyze),
		ChunksTotal:    chunksTotal,
	}, nil
}

// currentJobID reads the job manager's latest job ID. During a run, Latest
// returns a clone of the same running job — JobManager caps concurrency at
// one job per process and sets m.latest = job in Enqueue before the runner
// goroutine starts, so this cannot observe a stale prior job while the
// current one is still executing.
//
// This only resolves correctly when run() is reached via JobManager.Enqueue
// (the real dispatch path). If run() is invoked directly — as in a test that
// calls svc.run(...) without going through svc.jobs.Enqueue(...) — no job is
// ever enqueued, Latest() returns (nil, false), currentJobID() returns "",
// and every MarkProgress("", ...) call below is a silent no-op (job.go's
// MarkProgress returns early when m.current is nil). A test built that way
// cannot observe whether progress reporting is wired correctly at all.
func (s *depsAnalyzerService) currentJobID() string {
	if s == nil || s.jobs == nil {
		return ""
	}
	if job, ok := s.jobs.Latest(); ok && job != nil {
		return job.ID
	}
	return ""
}

// currentJobMode mirrors currentJobID's pattern: JobRunner's fixed (ctx,
// profile) signature has no room for the requested incremental-pass mode, so
// run() reads it back from the job manager instead. Same direct-call caveat
// as currentJobID applies — a test that calls svc.run(...) without going
// through svc.jobs.Enqueue(...)/EnqueueWithOptions(...) sees "" here, which
// PlanIncremental treats the same as "auto".
func (s *depsAnalyzerService) currentJobMode() string {
	if s == nil || s.jobs == nil {
		return ""
	}
	if job, ok := s.jobs.Latest(); ok && job != nil {
		return job.Mode
	}
	return ""
}

func (s *depsAnalyzerService) lookupProfile(name string) (config.AgentProfile, bool) {
	if s.orch != nil {
		if p, ok := s.orch.AgentProfileCfg(name); ok && config.ProfileEnabled(p) {
			return p, true
		}
	}
	// Fall through to the loaded cfg pointer; useful at startup before the
	// orchestrator's cfgMu is hot.
	if s.cfg != nil {
		if p, ok := s.cfg.Agent.Profiles[name]; ok && config.ProfileEnabled(p) {
			return p, true
		}
	}
	return config.AgentProfile{}, false
}

func jobRowFromJob(j *depsanalysis.Job) server.DepsAnalyzeJobRow {
	if j == nil {
		return server.DepsAnalyzeJobRow{}
	}
	row := server.DepsAnalyzeJobRow{
		JobID:          j.ID,
		Profile:        j.Profile,
		Status:         string(j.Status),
		QueuedAt:       j.QueuedAt,
		IssuesScanned:  j.IssuesScanned,
		IssuesAnalyzed: j.IssuesAnalyzed,
		EdgesFound:     j.EdgesFound,
		Error:          j.Error,
		ChunksTotal:    j.ChunksTotal,
		ChunksDone:     j.ChunksDone,
		Trigger:        j.Trigger,
	}
	if !j.StartedAt.IsZero() {
		t := j.StartedAt
		row.StartedAt = &t
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		row.FinishedAt = &t
	}
	if !j.LastActivityAt.IsZero() {
		t := j.LastActivityAt
		row.LastActivityAt = &t
	}
	return row
}
