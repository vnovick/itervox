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
) (svc *depsAnalyzerService, bindNotify func(func())) {
	var notifyTarget func()
	svc = newDepsAnalyzerService(
		ctx, orch, cfg, tr, runner,
		depsanalysis.SidecarPath(filepath.Dir(workflowPath)),
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
	notify func(),
) *depsAnalyzerService {
	s := &depsAnalyzerService{
		ctx:         ctx,
		orch:        orch,
		cfg:         cfg,
		tracker:     tr,
		runner:      runner,
		sidecarPath: sidecarPath,
		notify:      notify,
		logger:      slog.Default().With("component", "deps_analyzer"),
	}
	s.jobs = depsanalysis.NewJobManager(s.run)
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

// EnqueueAnalysis satisfies the server.DepsAnalyzer interface.
func (s *depsAnalyzerService) EnqueueAnalysis(profile string) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("deps analyzer service is nil")
	}
	if _, ok := s.lookupProfile(profile); !ok {
		return "", time.Time{}, fmt.Errorf("profile %q is not configured or is disabled", profile)
	}
	// Hand the daemon's context to the job manager so the analyzer keeps
	// running after the HTTP request returns. Per-pass cancellation happens
	// via daemon shutdown.
	id, err := s.jobs.Enqueue(s.ctx, profile)
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

func (s *depsAnalyzerService) run(ctx context.Context, profile string) (*depsanalysis.Sidecar, error) {
	prof, ok := s.lookupProfile(profile)
	if !ok {
		return nil, fmt.Errorf("profile %q vanished between enqueue and run", profile)
	}
	// cfg.Tracker.ActiveStates/TerminalStates are cfgMu-guarded (runtime
	// setter: SetTrackerStatesCfg via PUT /settings/tracker/states) and this
	// pass runs on a daemon-scoped goroutine well after startup (triggered by
	// EnqueueAnalysis), so read via the getter. BacklogStates has no runtime
	// setter, so the direct cfg read stays legal.
	active, terminal, _ := s.orch.TrackerStatesCfg()
	states := depsanalysis.DedupeStateNames(
		active,
		terminal,
		s.cfg.Tracker.BacklogStates,
	)
	issues, trackerEdges, err := depsanalysis.FetchIssues(ctx, s.tracker, states)
	if err != nil {
		return nil, fmt.Errorf("fetch issues: %w", err)
	}
	s.logger.Info("deps analyzer started", "profile", profile, "issues", len(issues), "tracker_edges", len(trackerEdges))
	edges, err := depsanalysis.RunAgentPass(ctx, depsanalysis.AgentPassInput{
		Runner:        s.runner,
		Profile:       prof,
		ProfileName:   profile,
		Issues:        issues,
		TrackerEdges:  trackerEdges,
		WorkspacePath: s.cfg.Workspace.Root,
		LogDir:        "",
		Logger:        s.logger,
		ReadTimeoutMs: s.cfg.Agent.ReadTimeoutMs,
		TurnTimeoutMs: s.cfg.Agent.TurnTimeoutMs,
	})
	if err != nil {
		s.logger.Warn("deps analyzer pass failed", "profile", profile, "error", err.Error())
		return nil, err
	}
	sc := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Now().UTC(),
		Profile:     profile,
		Edges:       edges,
	}
	if err := depsanalysis.SaveSidecar(s.sidecarPath, sc); err != nil {
		return nil, fmt.Errorf("save sidecar: %w", err)
	}
	if s.notify != nil {
		s.notify()
	}
	s.logger.Info("deps analyzer succeeded", "profile", profile, "edges", len(edges))
	return sc, nil
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
		JobID:         j.ID,
		Profile:       j.Profile,
		Status:        string(j.Status),
		QueuedAt:      j.QueuedAt,
		IssuesScanned: j.IssuesScanned,
		EdgesFound:    j.EdgesFound,
		Error:         j.Error,
	}
	if !j.StartedAt.IsZero() {
		t := j.StartedAt
		row.StartedAt = &t
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		row.FinishedAt = &t
	}
	return row
}
