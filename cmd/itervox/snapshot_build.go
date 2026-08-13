package main

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// snapshot_build.go — buildSnapFunc, extracted out of main.go (outbox
// Task 4 size-budget) so main.go stays under its 2000-line cap. See
// go-package-hygiene: split by responsibility, not alphabet — this file's
// sole concern is wiring the live orchestrator/tracker/config into the
// StateSnapshot closure server.New consumes.

// buildSnapFunc returns the StateSnapshot function wired to the live orchestrator,
// tracker, and config. Extracted from run() to keep that function scannable.
func buildSnapFunc(orch *orchestrator.Orchestrator, tr tracker.Tracker, cfg *config.Config, appSessionID string, logBuf *logbuffer.Buffer, workflowPath string, depsSvc *depsAnalyzerService, ob *outbox.Outbox) func() server.StateSnapshot {
	// projectName is computed once at construction time (not per snapshot) —
	// neither the tracker slug nor the workflow path change within a daemon
	// run (config reload restarts the process, so this closure is recreated).
	projectName := resolveProjectName(cfg, workflowPath)
	// depsSidecar is mtime-cached; Latest() reads the sidecar only when the
	// file has changed since the last successful load. v0.2.0 todolist6.
	depsSidecar := depsanalysis.NewSidecarCache(depsanalysis.SidecarPath(filepath.Dir(workflowPath)))
	return func() server.StateSnapshot {
		s := orch.Snapshot()
		now := time.Now()

		running := make([]server.RunningRow, 0, len(s.Running))
		for _, r := range s.Running {
			msg := r.LastMessage
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			var lastEvAt string
			if r.LastEventAt != nil {
				lastEvAt = r.LastEventAt.Format(time.RFC3339)
			}
			// Count subagent markers in the log buffer for this issue.
			var subCount int
			if logBuf != nil {
				for _, line := range logBuf.Get(r.Issue.Identifier) {
					if strings.Contains(line, `"claude: subagent"`) || strings.Contains(line, `"codex: subagent"`) {
						subCount++
					}
				}
			}
			// T-6: prefer the live counter (incremented from the HTTP handler
			// goroutine) over RunEntry.CommentCount because the latter is only
			// updated when the run terminates. If both are zero we omit the
			// field via omitempty.
			liveComments := r.CommentCount
			if c := orch.CommentCountFor(r.Issue.Identifier); c > liveComments {
				liveComments = c
			}
			running = append(running, server.RunningRow{
				Identifier:    r.Issue.Identifier,
				State:         r.Issue.State,
				TurnCount:     r.TurnCount,
				Tokens:        r.TotalTokens,
				InputTokens:   r.InputTokens,
				OutputTokens:  r.OutputTokens,
				LastEvent:     msg,
				LastEventAt:   lastEvAt,
				SessionID:     r.SessionID,
				WorkerHost:    r.WorkerHost,
				Backend:       r.Backend,
				Kind:          r.Kind,
				AutomationID:  r.AutomationID,
				TriggerType:   r.TriggerType,
				CommentCount:  liveComments,
				ElapsedMs:     now.Sub(r.StartedAt).Milliseconds(),
				StartedAt:     r.StartedAt,
				SubagentCount: subCount,
			})
		}
		sort.Slice(running, func(i, j int) bool {
			return running[i].StartedAt.Before(running[j].StartedAt)
		})

		retrying := sortedRetryRows(s.RetryAttempts)
		paused := sortedPausedIdentifiers(s.PausedIdentifiers)

		var rateLimits *server.RateLimitInfo
		var activeProjectFilter []string
		if rl, ok := tr.(tracker.RateLimiter); ok {
			if snap := rl.RateLimitSnapshot(); snap != nil {
				rateLimits = &server.RateLimitInfo{
					RequestsLimit:       snap.RequestsLimit,
					RequestsRemaining:   snap.RequestsRemaining,
					RequestsReset:       snap.Reset,
					ComplexityLimit:     snap.ComplexityLimit,
					ComplexityRemaining: snap.ComplexityRemaining,
				}
			}
		}
		if tpm, ok := tr.(tracker.ProjectManager); ok {
			activeProjectFilter = tpm.GetProjectFilter()
		}
		// When no runtime filter is set but WORKFLOW.md has project_slug,
		// surface it so the TUI picker shows it as checked.
		if activeProjectFilter == nil && cfg.Tracker.ProjectSlug != "" {
			activeProjectFilter = []string{cfg.Tracker.ProjectSlug}
		}
		profiles := orch.ProfilesCfg()
		autoClearWorkspace := orch.AutoClearWorkspaceCfg()
		activeStates, terminalStates, completionState := orch.TrackerStatesCfg()

		var availableProfiles []string
		for name, profile := range profiles {
			if config.ProfileEnabled(profile) {
				availableProfiles = append(availableProfiles, name)
			}
		}
		sort.Strings(availableProfiles)

		var profileDefs map[string]server.ProfileDef
		if len(profiles) > 0 {
			profileDefs = make(map[string]server.ProfileDef, len(profiles))
			for n, p := range profiles {
				profileDefs[n] = profileDefFromConfig(p)
			}
		}

		completedRuns := orch.RunHistory()
		history := make([]server.HistoryRow, 0, len(completedRuns))
		for _, r := range completedRuns {
			history = append(history, server.HistoryRow{
				Identifier:   r.Identifier,
				Title:        r.Title,
				StartedAt:    r.StartedAt,
				FinishedAt:   r.FinishedAt,
				ElapsedMs:    r.ElapsedMs,
				TurnCount:    r.TurnCount,
				TotalTokens:  r.TotalTokens,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				Status:       r.Status,
				WorkerHost:   r.WorkerHost,
				Backend:      r.Backend,
				Kind:         r.Kind,
				SessionID:    r.SessionID,
				AppSessionID: r.AppSessionID,
				AutomationID: r.AutomationID,
				TriggerType:  r.TriggerType,
				CommentCount: r.CommentCount,
			})
		}

		sshHostAddrs, sshHostDescs := orch.SSHHostsCfg()
		sshHostInfos := make([]server.SSHHostInfo, 0, len(sshHostAddrs))
		for _, h := range sshHostAddrs {
			sshHostInfos = append(sshHostInfos, server.SSHHostInfo{
				Host:        h,
				Description: sshHostDescs[h],
			})
		}

		// unified-dependency-graph Task 7: the dashboard graph now derives
		// solely from State.InferredDeps (event-loop reconciled, with
		// provenance flags) — dependencyGraphRows no longer reads the sidecar
		// directly. depsSidecar is still consulted below for
		// DepsLastAnalyzedAt (a separate "when did the analyzer last run"
		// concern, unrelated to graph edge derivation).
		sidecar := depsSidecar.Latest()
		dependencyGraphNodes, dependencyGraphEdges := dependencyGraphRows(s)
		var depsLastAnalyzedAt *time.Time
		if sidecar != nil && !sidecar.GeneratedAt.IsZero() {
			t := sidecar.GeneratedAt
			depsLastAnalyzedAt = &t
		}
		// DepsAnalyzeJob (#46-1) — the analyzer JobManager's current job,
		// running or terminal, independent of which HTTP request (if any)
		// started it. nil when no job has ever run this process lifetime
		// (JobManager.Latest reports false — see depsAnalyzerService.CurrentJob).
		var depsAnalyzeJob *server.DepsAnalyzeJobRow
		if row, ok := depsSvc.CurrentJob(); ok {
			depsAnalyzeJob = &row
		}
		snap := server.StateSnapshot{
			GeneratedAt:                  now,
			Counts:                       server.Counts{Running: len(running), Retrying: len(retrying), Paused: len(paused)},
			Running:                      running,
			History:                      history,
			Retrying:                     retrying,
			Paused:                       paused,
			MaxConcurrentAgents:          orch.MaxWorkers(),
			MaxRetries:                   orch.MaxRetriesCfg(),
			FailedState:                  orch.FailedStateCfg(),
			MaxSwitchesPerIssuePerWindow: orch.MaxSwitchesPerIssuePerWindowCfg(),
			SwitchWindowHours:            orch.SwitchWindowHoursCfg(),
			RateLimits:                   rateLimits,
			TrackerKind:                  cfg.Tracker.Kind,
			ProjectName:                  projectName,
			ActiveProjectFilter:          activeProjectFilter,
			AvailableProfiles:            availableProfiles,
			ProfileDefs:                  profileDefs,
			ActiveStates:                 activeStates,
			TerminalStates:               terminalStates,
			CompletionState:              completionState,
			BacklogStates:                cfg.Tracker.BacklogStates,
			PollIntervalMs:               cfg.Polling.IntervalMs,
			AutoClearWorkspace:           autoClearWorkspace,
			CurrentAppSessionID:          appSessionID,
			SSHHosts:                     sshHostInfos,
			DispatchStrategy:             orch.DispatchStrategyCfg(),
			DefaultBackend:               configuredBackend(cfg.Agent.Command, cfg.Agent.Backend),
			InlineInput:                  orch.InlineInputCfg(),
			Automations:                  automationconfig.DefinitionsFromConfigs(orch.AutomationsCfg()),
			AutomationQueue:              automationQueueRows(s),
			AutomationQueueBackpressure:  automationQueueBackpressureRow(s.AutomationQueueBackpressure),
			DispatchPressure:             dispatchPressureRow(s.DispatchPressure),
			// gaps_11 G-11 — the snapshot value is a struct-copy of the
			// event-loop counter (State is copied by value in storeSnap), so
			// reading it here never touches live State.
			AutomationDropsSelfReentryTotal: s.AutomationDropsSelfReentryTotal,
			DependencyAudit:                 dependencyAuditRows(s.DependencyAudit),
			// DepsRefreshingCount surfaces the in-flight batch size (not the
			// bool latch) so the dashboard can render "refreshing N" — a
			// zero-row states-only batch renders no suffix at all rather than
			// a confusing "refreshing 0" (LiveOpsStrip.tsx depsChipLabel).
			DepsRefreshingCount:       s.DepsRefreshBatchSize,
			DepsRefreshLastDurationMs: s.DepsRefreshLastDurationMs,
			DepsRefreshDegradedCount:  degradedDependencyAuditCount(s.DependencyAudit),
			DependencyGraphNodes:      dependencyGraphNodes,
			DependencyGraphEdges:      dependencyGraphEdges,
			DependencyCycles:          dependencyCycleRows(s),
			DependencyAttention:       dependencyAttentionRows(s),
			DepsAnalyzerProfile:       orch.DepsAnalyzerProfileCfg(),
			DepsLastAnalyzedAt:        depsLastAnalyzedAt,
			DepsAnalyzeJob:            depsAnalyzeJob,
			AvailableModels:           convertModelsForSnapshot(cfg.Agent.AvailableModels),
			SupportedAgentActions:     config.SupportedAgentActions(),
			ReviewerProfile:           func() string { p, _ := orch.ReviewerCfg(); return p }(),
			AutoReview:                func() bool { _, a := orch.ReviewerCfg(); return a }(),
			CandidateSeen:             snapshotCandidateSeenRows(s.CandidateSeen),
			// outbox Task 4 — ob.Snapshot() is the outbox's own global
			// enqueue-order copy (empty when cfg.Tracker.Outbox is false;
			// the handle is still constructed but nothing is ever
			// enqueued into it — see the ob construction comment above).
			// OutboxSyncing is the sorted join-key list the web matches
			// against /api/v1/issues rows by Identifier.
			OutboxEntries: outboxEntryRows(ob.Snapshot()),
			OutboxSyncing: outboxSyncingRows(s.OutboxSyncing),
		}
		// Stale threshold for the dashboard badge: pick the longest
		// MaxAgeMinutes across all enabled input_required automations. If no
		// rule configures one, fall back to 24h so abandoned entries still
		// surface visually even when no automation guards against them.
		staleAfter := longestInputRequiredMaxAge(orch.AutomationsCfg(), 24*time.Hour)
		snap.InputRequired = sortedInputRequiredRows(s.InputRequiredIssues, s.PendingInputResumes, staleAfter, now)
		// Surface in-flight WORKFLOW.md reload failures (T-26). nil when valid.
		snap.ConfigInvalid = loadConfigInvalid()
		return snap
	}
}
