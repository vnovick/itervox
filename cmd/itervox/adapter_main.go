package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/app"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/skills"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// orchestratorAdapter implements server.OrchestratorClient using the live
// orchestrator, log buffer, tracker, and WORKFLOW.md persistence helpers.
// notify must be set after server construction (adapter.notify = srv.Notify).
type orchestratorAdapter struct {
	orch         *orchestrator.Orchestrator
	logBuf       *logbuffer.Buffer
	cfg          *config.Config
	tr           tracker.Tracker
	workflowPath string
	notify       func()
	skillsCache  *skills.Cache
}

func (a *orchestratorAdapter) FetchIssues(ctx context.Context) ([]server.TrackerIssue, error) {
	allStates := deduplicateStates(a.cfg.Tracker.BacklogStates, a.cfg.Tracker.ActiveStates, a.cfg.Tracker.TerminalStates, a.cfg.Tracker.CompletionState)
	issues, err := a.tr.FetchIssuesByStates(ctx, allStates)
	if err != nil {
		return nil, err
	}
	snap := a.orch.Snapshot()
	now := time.Now()
	result := make([]server.TrackerIssue, len(issues))
	for i, issue := range issues {
		result[i] = app.EnrichIssue(issue, snap, now, a.cfg)
	}
	return result, nil
}

func (a *orchestratorAdapter) CancelIssue(identifier string) bool {
	return a.orch.CancelIssue(identifier)
}

func (a *orchestratorAdapter) ResumeIssue(identifier string) bool {
	ok := a.orch.ResumeIssue(identifier)
	if ok {
		a.orch.Refresh()
	}
	return ok
}

func (a *orchestratorAdapter) TerminateIssue(identifier string) bool {
	ok := a.orch.TerminateIssue(identifier)
	if ok {
		a.orch.Refresh()
	}
	return ok
}

func (a *orchestratorAdapter) ReanalyzeIssue(identifier string) bool {
	return a.orch.ReanalyzeIssue(identifier)
}

func (a *orchestratorAdapter) FetchLogs(identifier string) []string {
	return a.logBuf.Get(identifier)
}

func (a *orchestratorAdapter) FetchLogIdentifiers() []string {
	return a.logBuf.Identifiers()
}

func (a *orchestratorAdapter) ClearLogs(identifier string) error {
	return a.logBuf.Clear(identifier)
}

func (a *orchestratorAdapter) ClearAllLogs() error {
	return a.logBuf.ClearAll()
}

func (a *orchestratorAdapter) ClearIssueSubLogs(identifier string) error {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil
	}
	issueDir := filepath.Join(logDir, workspace.SanitizeKey(identifier))
	if err := workspace.AssertContained(logDir, issueDir); err != nil {
		return fmt.Errorf("clear sublogs: %w", err)
	}
	entries, err := os.ReadDir(issueDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		_ = os.Remove(filepath.Join(issueDir, e.Name()))
	}
	return nil
}

func (a *orchestratorAdapter) ClearSessionSublog(identifier, sessionID string) error {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil
	}
	// Sanitize both path components to prevent directory traversal.
	safeID := workspace.SanitizeKey(identifier)
	safeSess := workspace.SanitizeKey(sessionID)
	p := filepath.Join(logDir, safeID, safeSess+".jsonl")
	if err := workspace.AssertContained(logDir, p); err != nil {
		return fmt.Errorf("clear session sublog: %w", err)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// FetchSubLogs returns parsed Claude Code session logs from CLAUDE_CODE_LOG_DIR.
// The fetcher is selected based on where the issue was last run:
//   - SSH host → SSHSublogFetcher (tar-over-SSH, session IDs from filenames)
//   - local    → LocalSublogFetcher (direct disk read)
//   - Docker   → DockerSublogFetcher (planned)
func (a *orchestratorAdapter) FetchSubLogs(ctx context.Context, identifier string) ([]domain.IssueLogEntry, error) {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil, nil
	}
	issueLogDir := filepath.Join(logDir, workspace.SanitizeKey(identifier))
	return a.sublogFetcher(identifier).FetchSubLogs(ctx, issueLogDir)
}

// sublogFetcher resolves the correct SublogFetcher for identifier by inspecting
// run history and live running sessions. Returns LocalSublogFetcher when no
// remote host is found.
func (a *orchestratorAdapter) sublogFetcher(identifier string) agent.SublogFetcher {
	// Check currently-running sessions first (most recent wins).
	// Running is keyed by issue ID, not identifier — iterate values.
	snap := a.orch.Snapshot()
	for _, entry := range snap.Running {
		if entry.Issue.Identifier == identifier && entry.WorkerHost != "" {
			return agent.SSHSublogFetcher{Host: entry.WorkerHost}
		}
	}
	// Fall back to run history.
	for _, run := range a.orch.RunHistory() {
		if run.Identifier == identifier && run.WorkerHost != "" {
			return agent.SSHSublogFetcher{Host: run.WorkerHost}
		}
	}
	return agent.LocalSublogFetcher{}
}

func (a *orchestratorAdapter) DispatchReviewer(identifier string) error {
	return a.orch.DispatchReviewer(identifier)
}

// RefreshAvailableModels implements server.ModelRefresher so the dashboard's
// Settings → Models "Refresh" button can rewrite agent.available_models in
// WORKFLOW.md without an operator hand-edit. Delegates to the same
// mergeAvailableModelsIntoWorkflow helper that powers
// `itervox models refresh`, so CLI and HTTP share one code path.
func (a *orchestratorAdapter) RefreshAvailableModels(ctx context.Context, backend string) (map[string][]server.ModelOption, error) {
	if !IsAcceptedModelBackend(backend) {
		return nil, fmt.Errorf("invalid backend %q (claude|codex|all)", backend)
	}
	discovered := map[string][]agent.ModelOption{}
	switch backend {
	case "claude":
		discovered["claude"] = agent.ListClaudeModels()
	case "codex":
		discovered["codex"] = agent.ListCodexModels()
	default: // "all"
		discovered["claude"] = agent.ListClaudeModels()
		discovered["codex"] = agent.ListCodexModels()
	}
	merged, err := mergeAvailableModelsIntoWorkflow(a.workflowPath, discovered)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]server.ModelOption, len(merged))
	for k, list := range merged {
		opts := make([]server.ModelOption, len(list))
		for i, m := range list {
			opts[i] = server.ModelOption{ID: m.ID, Label: m.Label}
		}
		out[k] = opts
	}
	_ = ctx
	return out, nil
}

// EmitPRMerged implements server.PRMergedEmitter so the merge_pr action
// handler can fire pr_merged automations after a successful gh merge. P1.
func (a *orchestratorAdapter) EmitPRMerged(ctx context.Context, identifier, prURL string, prNumber int, mergedSHA, baseRef string) error {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", identifier)
	}
	a.orch.DispatchPRMergedAutomations(ctx, *issue, orchestrator.PRMergedEvent{
		PRURL:     prURL,
		PRNumber:  prNumber,
		BaseRef:   baseRef,
		MergedSHA: mergedSHA,
		MergedAt:  time.Now().UTC(),
	})
	return nil
}

func (a *orchestratorAdapter) CommentOnIssue(ctx context.Context, identifier, body string) error {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", identifier)
	}
	_, err = a.tr.CreateComment(ctx, issue.ID, tracker.MarkManagedComment(body))
	return err
}

func (a *orchestratorAdapter) CreateIssue(
	ctx context.Context,
	identifier, title, body, stateName string,
) (*domain.Issue, error) {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return nil, fmt.Errorf("issue %s not found", identifier)
	}
	return a.tr.CreateIssue(ctx, issue.ID, title, body, stateName)
}

func (a *orchestratorAdapter) UpdateIssueState(ctx context.Context, identifier, stateName string) error {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", identifier)
	}
	if err := a.tr.UpdateIssueState(ctx, issue.ID, stateName); err != nil {
		return err
	}
	source := orchestrator.StatusSourceDashboard
	if server.IssueStatusSource(ctx) == server.IssueStatusSourceAgent {
		source = orchestrator.StatusSourceWorkerLifecycle
	}
	change := orchestrator.IssueStatusChange{
		Identifier: identifier,
		FromState:  issue.State,
		ToState:    stateName,
		Source:     source,
	}
	snap := a.orch.Snapshot()
	if live := snap.Running[issue.ID]; live != nil {
		change.ProfileName = live.ProfileName
		change.Backend = live.Backend
		change.WorkerHost = live.WorkerHost
		if live.AutomationID != "" && server.IssueStatusSource(ctx) == server.IssueStatusSourceAgent {
			change.Source = orchestrator.StatusSourceAutomation
			change.AutomationID = live.AutomationID
			change.TriggerType = live.TriggerType
		}
	}
	a.orch.RecordIssueStatusChange(change)
	return nil
}
