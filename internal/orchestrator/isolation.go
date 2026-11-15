package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
)

// prepareRunIsolation resolves the sandbox a run's agent turns launch in
// (#258): nil for isolation "none"; an error, and no run, when the profile
// asks for "process" on an SSH worker or on a machine where no sandbox
// mechanism works. Runs on the worker goroutine and touches no State;
// cfg.Agent.Isolation, cfg.Workspace.Root and cfg.WorkflowPath are
// read-only after startup.
func (o *Orchestrator) prepareRunIsolation(profile config.AgentProfile, workerHost, wsPath string) (*agent.Isolation, error) {
	mode := config.ResolveIsolation(profile.Isolation, o.cfg.Agent.Isolation)
	if mode == config.IsolationProcess && workerHost != "" {
		return nil, fmt.Errorf("%w: process isolation is not supported on SSH workers (host %s)", agent.ErrIsolationUnavailable, workerHost)
	}
	in := agent.RunIsolationInputs(wsPath, o.cfg.Workspace.Root, filepath.Dir(o.cfg.WorkflowPath), profile)
	return agent.PrepareIsolation(mode, in, o.isolationDetect)
}

// reportRunIsolation records the sandbox in the run log (mechanism and
// policy) and tells the event loop its name for the issue detail.
func (o *Orchestrator) reportRunIsolation(ctx context.Context, issue domain.Issue, runLogID string, iso *agent.Isolation) {
	slog.Info("worker: agent sandboxed", "issue_identifier", issue.Identifier, "mechanism", iso.Name())
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLineWithSession("INFO", "worker: "+iso.Describe(), runLogID))
	}
	select {
	case o.events <- OrchestratorEvent{
		Type:     EventWorkerUpdate,
		RunID:    runIDFrom(ctx),
		IssueID:  issue.ID,
		RunEntry: &RunEntry{Isolation: iso.Name()},
	}:
	default:
		metrics.EventDropped() // CORE-045: the run log still names the sandbox
	}
}
