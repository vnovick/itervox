package main

import (
	"cmp"
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/schedule"
	"github.com/vnovick/itervox/internal/tracker"
)

type compiledAutomation struct {
	cfg            config.AutomationConfig
	expr           schedule.Expression
	location       *time.Location
	identifierRe   *regexp.Regexp
	inputContextRe *regexp.Regexp
}

type observedAutomationIssue struct {
	State     string
	InBacklog bool
}

type observedAutomationComment struct {
	LatestCommentID  string
	CommentCreatedAt string
}

type automationPollState struct {
	issues          map[string]observedAutomationIssue
	trackerComments map[string]map[string]observedAutomationComment
}

type compiledAutomationSet struct {
	cron             []compiledAutomation
	polledEvents     []compiledAutomation
	inputRequired    []orchestrator.InputRequiredAutomation
	runFailed        []orchestrator.RunFailedAutomation
	prOpened         []orchestrator.PROpenedAutomation
	rateLimited      []orchestrator.RateLimitedAutomation
	blockersResolved []orchestrator.BlockersResolvedAutomation
}

func startAutomations(ctx context.Context, cfg *config.Config, tr tracker.Tracker, orch *orchestrator.Orchestrator) {
	// Register the startup compiled sets so the event loop can dispatch
	// input-required and run-failed automations from tick 0, even before the
	// goroutine's first re-compile.
	startupCompiled := compileAutomations(cfg)
	orch.SetInputRequiredAutomations(startupCompiled.inputRequired)
	orch.SetRunFailedAutomations(startupCompiled.runFailed)
	orch.SetPROpenedAutomations(startupCompiled.prOpened)
	orch.SetRateLimitedAutomations(startupCompiled.rateLimited)
	orch.SetBlockersResolvedAutomations(startupCompiled.blockersResolved)

	// Summarise what survived compilation so users can see at a glance that
	// their configured automations registered (or that some were dropped
	// for unknown/disabled profiles, bad regex, etc. — those emit their own
	// slog.Warn lines above).
	logAutomationCompileSummary(len(cfg.Automations), startupCompiled)

	// Always run the goroutine: hot-add (empty → non-empty via SetAutomations
	// from the API) must take effect without a daemon restart. The loop
	// re-reads cfg.Automations from the orchestrator each tick, so it picks up
	// changes from orch.SetAutomationsCfg without any channel plumbing.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		lastFired := make(map[string]string)
		pollState := automationPollState{
			issues:          make(map[string]observedAutomationIssue),
			trackerComments: make(map[string]map[string]observedAutomationComment),
		}
		inputRequiredState := inputRequiredReplayState{
			issues: make(map[string]inputRequiredReplayIssueState),
		}
		runOnce := func(now time.Time) {
			// Build a per-tick cfg view with runtime-mutable fields read
			// through orchestrator getters. compileAutomations reads profiles
			// for both the automation profile and rate_limited switch_to_profile,
			// so reading cfg.Agent.Profiles directly here would race profile
			// edits from HTTP handlers.
			tickCfg := automationCompileConfigView(cfg, orch)
			compiled := compileAutomations(&tickCfg)

			// Re-register helper-agent rules every tick. Setters are cheap
			// (slice copy + mutex) and idempotent when rules are unchanged.
			orch.SetInputRequiredAutomations(compiled.inputRequired)
			orch.SetRunFailedAutomations(compiled.runFailed)
			orch.SetPROpenedAutomations(compiled.prOpened)
			orch.SetRateLimitedAutomations(compiled.rateLimited)
			orch.SetBlockersResolvedAutomations(compiled.blockersResolved)
			inputRequiredState = replayInputRequiredAutomations(ctx, tr, orch, compiled.inputRequired, inputRequiredState, now)

			// Drop lastFired entries for automations no longer present so a
			// same-ID rule re-added under new semantics isn't blocked by stale
			// minute markers.
			if len(lastFired) > 0 {
				present := make(map[string]struct{}, len(compiled.cron))
				for _, entry := range compiled.cron {
					present[entry.cfg.ID] = struct{}{}
				}
				for id := range lastFired {
					if _, ok := present[id]; !ok {
						delete(lastFired, id)
					}
				}
			}

			// Snapshot cfgMu-guarded tracker states once per tick so downstream
			// helpers read a consistent view without racing concurrent HTTP
			// updates via orch.SetTrackerStatesCfg.
			activeStates, terminalStates, completionState := orch.TrackerStatesCfg()
			for _, entry := range compiled.cron {
				if !entry.cfg.Enabled {
					continue
				}
				localNow := now.In(entry.location)
				minuteKey := localNow.Format("2006-01-02T15:04")
				if lastFired[entry.cfg.ID] == minuteKey {
					continue
				}
				if !entry.expr.Matches(localNow) {
					continue
				}
				lastFired[entry.cfg.ID] = minuteKey
				execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				runCronAutomation(execCtx, cfg, tr, orch, entry, activeStates, now)
				cancel()
			}
			if len(compiled.polledEvents) > 0 {
				execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				pollState = pollAutomationEvents(execCtx, cfg, tr, orch, compiled.polledEvents, pollState, activeStates, terminalStates, completionState, now)
				cancel()
			}
		}

		runOnce(time.Now())
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				runOnce(now)
			}
		}
	}()
}

func automationCompileConfigView(cfg *config.Config, orch *orchestrator.Orchestrator) config.Config {
	tickCfg := *cfg
	tickCfg.Automations = orch.AutomationsCfg()
	tickCfg.Agent.Profiles = orch.ProfilesCfg()
	return tickCfg
}

func automationProducersPaused(snap orchestrator.State) bool {
	return snap.AutomationQueueBackpressure.PausedProducers
}

// automationProducersPausedLive reads the live paused-producers flag without
// deep-copying the entire snapshot. Use this in pre-fetch gates where the
// only field consumed is PausedProducers; reach for the full snapshot only
// when other fields are also needed downstream. v0.2.0 audit P2-1.
func automationProducersPausedLive(orch *orchestrator.Orchestrator) bool {
	if orch == nil {
		return false
	}
	return orch.IsAutomationProducersPaused()
}

func compileAutomations(cfg *config.Config) compiledAutomationSet {
	var compiled compiledAutomationSet
	if len(cfg.Automations) == 0 {
		return compiled
	}
	compiled.cron = make([]compiledAutomation, 0, len(cfg.Automations))
	compiled.polledEvents = make([]compiledAutomation, 0, len(cfg.Automations))
	compiled.inputRequired = make([]orchestrator.InputRequiredAutomation, 0, len(cfg.Automations))
	compiled.runFailed = make([]orchestrator.RunFailedAutomation, 0, len(cfg.Automations))
	compiled.prOpened = make([]orchestrator.PROpenedAutomation, 0, len(cfg.Automations))
	compiled.rateLimited = make([]orchestrator.RateLimitedAutomation, 0, len(cfg.Automations))
	compiled.blockersResolved = make([]orchestrator.BlockersResolvedAutomation, 0, len(cfg.Automations))
	for _, entry := range cfg.Automations {
		if !entry.Enabled {
			continue
		}
		profile, ok := cfg.Agent.Profiles[entry.Profile]
		if !ok {
			slog.Warn("automation: skipping rule with unknown profile", "automation", entry.ID, "profile", entry.Profile)
			continue
		}
		if !config.ProfileEnabled(profile) {
			slog.Warn("automation: skipping rule with disabled profile", "automation", entry.ID, "profile", entry.Profile)
			continue
		}
		var identifierRe *regexp.Regexp
		if entry.Filter.IdentifierRegex != "" {
			re, err := regexp.Compile(entry.Filter.IdentifierRegex)
			if err != nil {
				slog.Warn("automation: invalid identifier regex", "automation", entry.ID, "regex", entry.Filter.IdentifierRegex, "error", err)
				continue
			}
			identifierRe = re
		}
		switch entry.Trigger.Type {
		case config.AutomationTriggerCron:
			expr, err := schedule.Parse(entry.Trigger.Cron)
			if err != nil {
				slog.Warn("automation: invalid cron expression", "automation", entry.ID, "cron", entry.Trigger.Cron, "error", err)
				continue
			}
			location := time.Local
			if entry.Trigger.Timezone != "" {
				loc, err := time.LoadLocation(entry.Trigger.Timezone)
				if err != nil {
					slog.Warn("automation: invalid timezone", "automation", entry.ID, "timezone", entry.Trigger.Timezone, "error", err)
					continue
				}
				location = loc
			}
			compiled.cron = append(compiled.cron, compiledAutomation{
				cfg:          entry,
				expr:         expr,
				location:     location,
				identifierRe: identifierRe,
			})
		case config.AutomationTriggerInputRequired:
			var inputContextRe *regexp.Regexp
			if entry.Filter.InputContextRegex != "" {
				re, err := regexp.Compile(entry.Filter.InputContextRegex)
				if err != nil {
					slog.Warn("automation: invalid input context regex", "automation", entry.ID, "regex", entry.Filter.InputContextRegex, "error", err)
					continue
				}
				inputContextRe = re
			}
			compiled.inputRequired = append(compiled.inputRequired, orchestrator.InputRequiredAutomation{
				ID:                entry.ID,
				ProfileName:       entry.Profile,
				Instructions:      entry.Instructions,
				MatchMode:         entry.Filter.MatchMode,
				States:            entry.Filter.States,
				LabelsAny:         entry.Filter.LabelsAny,
				IdentifierRegex:   identifierRe,
				InputContextRegex: inputContextRe,
				AutoResume:        entry.Policy.AutoResume,
				MaxAge:            time.Duration(entry.Filter.MaxAgeMinutes) * time.Minute,
			})
		case config.AutomationTriggerTrackerComment,
			config.AutomationTriggerIssueEnteredState,
			config.AutomationTriggerIssueMovedBacklog:
			compiled.polledEvents = append(compiled.polledEvents, compiledAutomation{
				cfg:            entry,
				identifierRe:   identifierRe,
				inputContextRe: inputContextReFor(entry),
			})
		case config.AutomationTriggerRunFailed:
			compiled.runFailed = append(compiled.runFailed, orchestrator.RunFailedAutomation{
				ID:              entry.ID,
				ProfileName:     entry.Profile,
				Instructions:    entry.Instructions,
				MatchMode:       entry.Filter.MatchMode,
				States:          entry.Filter.States,
				LabelsAny:       entry.Filter.LabelsAny,
				IdentifierRegex: identifierRe,
			})
		case config.AutomationTriggerPROpened:
			compiled.prOpened = append(compiled.prOpened, orchestrator.PROpenedAutomation{
				ID:              entry.ID,
				ProfileName:     entry.Profile,
				Instructions:    entry.Instructions,
				MatchMode:       entry.Filter.MatchMode,
				States:          entry.Filter.States,
				LabelsAny:       entry.Filter.LabelsAny,
				IdentifierRegex: identifierRe,
			})
		case config.AutomationTriggerRateLimited:
			switchProfile, ok := cfg.Agent.Profiles[entry.Policy.SwitchToProfile]
			if !ok {
				slog.Warn("automation: skipping rate_limited rule with unknown switch_to_profile",
					"automation", entry.ID, "switch_to_profile", entry.Policy.SwitchToProfile)
				continue
			}
			if !config.ProfileEnabled(switchProfile) {
				slog.Warn("automation: skipping rate_limited rule with disabled switch_to_profile",
					"automation", entry.ID, "switch_to_profile", entry.Policy.SwitchToProfile)
				continue
			}
			cooldown := time.Duration(entry.Policy.CooldownMinutes) * time.Minute
			if cooldown == 0 {
				cooldown = 30 * time.Minute // sane default per plan
			}
			compiled.rateLimited = append(compiled.rateLimited, orchestrator.RateLimitedAutomation{
				ID:              entry.ID,
				ProfileName:     entry.Profile,
				Instructions:    entry.Instructions,
				MatchMode:       entry.Filter.MatchMode,
				States:          entry.Filter.States,
				LabelsAny:       entry.Filter.LabelsAny,
				IdentifierRegex: identifierRe,
				AutoResume:      entry.Policy.AutoResume,
				SwitchToProfile: entry.Policy.SwitchToProfile,
				SwitchToBackend: entry.Policy.SwitchToBackend,
				Cooldown:        cooldown,
			})
		case config.AutomationTriggerBlockersResolved:
			compiled.blockersResolved = append(compiled.blockersResolved, orchestrator.BlockersResolvedAutomation{
				ID:              entry.ID,
				ProfileName:     entry.Profile,
				Instructions:    entry.Instructions,
				MatchMode:       entry.Filter.MatchMode,
				States:          entry.Filter.States,
				LabelsAny:       entry.Filter.LabelsAny,
				IdentifierRegex: identifierRe,
				MoveToState:     entry.Policy.MoveToState,
			})
		default:
			slog.Warn("automation: unsupported trigger type", "automation", entry.ID, "type", entry.Trigger.Type)
		}
	}
	return compiled
}

func runCronAutomation(
	ctx context.Context,
	cfg *config.Config,
	tr tracker.Tracker,
	orch *orchestrator.Orchestrator,
	entry compiledAutomation,
	activeStates []string,
	now time.Time,
) {
	// v0.2.0 audit P2-1 — gate on the live paused-producers bool before
	// paying for Snapshot()'s deep copy; only fetch the full snapshot if we
	// actually proceed.
	if automationProducersPausedLive(orch) {
		slog.Warn("automation: producers paused by automation queue backpressure", "automation", entry.cfg.ID)
		return
	}
	snap := orch.Snapshot()
	states := cronAutomationFetchStates(cfg, entry, activeStates)
	issues, err := tr.FetchIssuesByStates(ctx, states)
	if err != nil {
		slog.Warn("automation: fetch issues failed", "automation", entry.cfg.ID, "error", err)
		return
	}

	matches := make([]domain.Issue, 0, len(issues))
	for _, issue := range issues {
		if shouldSkipAutomatedIssue(snap, cfg, issue) {
			continue
		}
		if !matchesAutomationFilter(issue, entry, "") {
			continue
		}
		matches = append(matches, issue)
	}
	slices.SortFunc(matches, func(a, b domain.Issue) int {
		return cmp.Compare(a.Identifier, b.Identifier)
	})

	limit := entry.cfg.Filter.Limit
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	if len(matches) == 0 {
		return
	}

	count := 0
	for _, issue := range matches {
		if orch.DispatchAutomation(ctx, issue, orchestrator.AutomationDispatch{
			AutomationID: entry.cfg.ID,
			ProfileName:  entry.cfg.Profile,
			Instructions: entry.cfg.Instructions,
			AutoResume:   entry.cfg.Policy.AutoResume,
			Trigger: orchestrator.AutomationTriggerContext{
				Type:         config.AutomationTriggerCron,
				FiredAt:      now,
				AutomationID: entry.cfg.ID,
				Cron:         entry.cfg.Trigger.Cron,
				Timezone:     entry.cfg.Trigger.Timezone,
				CurrentState: issue.State,
			},
		}) {
			count++
		}
	}
	if count > 0 {
		slog.Info("automation: accepted automation dispatch events", "automation", entry.cfg.ID, "count", count, "profile", entry.cfg.Profile)
	}
}

// shouldSkipAutomatedIssue is the watcher-side pre-filter that mirrors the
// event-loop's TOCTOU re-check. Both call orchestrator.IneligibleReasonForAutomation
// so they cannot drift — adding a new dispatch guard here is a one-place edit
// in dispatch.go (T-35 unification).
func shouldSkipAutomatedIssue(state orchestrator.State, cfg *config.Config, issue domain.Issue) bool {
	reason := orchestrator.IneligibleReasonForAutomation(issue, state, cfg)
	return reason != "" && !orchestrator.IsQueueableAutomationReason(reason)
}

func matchesAutomationFilter(issue domain.Issue, entry compiledAutomation, inputContext string) bool {
	checks := make([]bool, 0, 4)
	if entry.identifierRe != nil {
		checks = append(checks, entry.identifierRe.MatchString(issue.Identifier))
	}
	if len(entry.cfg.Filter.States) > 0 {
		checks = append(checks, slices.ContainsFunc(entry.cfg.Filter.States, func(s string) bool {
			return strings.EqualFold(s, issue.State)
		}))
	}
	if len(entry.cfg.Filter.LabelsAny) > 0 {
		issueLabels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			issueLabels = append(issueLabels, strings.ToLower(label))
		}
		ok := false
		for _, wanted := range entry.cfg.Filter.LabelsAny {
			if slices.Contains(issueLabels, strings.ToLower(wanted)) {
				ok = true
				break
			}
		}
		checks = append(checks, ok)
	}
	if entry.inputContextRe != nil {
		checks = append(checks, entry.inputContextRe.MatchString(inputContext))
	}
	if len(checks) == 0 {
		return true
	}
	if entry.cfg.Filter.MatchMode == config.AutomationFilterMatchAny {
		for _, check := range checks {
			if check {
				return true
			}
		}
		return false
	}
	for _, check := range checks {
		if !check {
			return false
		}
	}
	return true
}

func inputContextReFor(entry config.AutomationConfig) *regexp.Regexp {
	if entry.Filter.InputContextRegex == "" {
		return nil
	}
	re, err := regexp.Compile(entry.Filter.InputContextRegex)
	if err != nil {
		slog.Warn("automation: invalid input context regex", "automation", entry.ID, "regex", entry.Filter.InputContextRegex, "error", err)
		return nil
	}
	return re
}

// cronAutomationFetchStates computes the tracker states to fetch for a cron
// automation. activeStates must be a snapshot obtained via
// orch.TrackerStatesCfg() — reading cfg.Tracker.ActiveStates directly races
// with HTTP-handler updates.
func cronAutomationFetchStates(cfg *config.Config, entry compiledAutomation, activeStates []string) []string {
	if entry.cfg.Filter.MatchMode != config.AutomationFilterMatchAny && len(entry.cfg.Filter.States) > 0 {
		return append([]string{}, entry.cfg.Filter.States...)
	}
	return deduplicateStates(cfg.Tracker.BacklogStates, activeStates, entry.cfg.Filter.States, "")
}

func pollAutomationEvents(
	ctx context.Context,
	cfg *config.Config,
	tr tracker.Tracker,
	orch *orchestrator.Orchestrator,
	entries []compiledAutomation,
	prev automationPollState,
	activeStates, terminalStates []string,
	completionState string,
	now time.Time,
) automationPollState {
	// v0.2.0 audit P2-1 — gate on the live paused-producers bool before
	// paying for Snapshot()'s deep copy.
	if automationProducersPausedLive(orch) {
		slog.Warn("automation: poll-event producers paused by automation queue backpressure")
		return prev
	}
	snap := orch.Snapshot()
	states := automationPollStates(cfg, entries, activeStates, terminalStates, completionState)
	if len(states) == 0 {
		return prev
	}
	issues, err := tr.FetchIssuesByStates(ctx, states)
	if err != nil {
		slog.Warn("automation: poll-event fetch failed", "error", err)
		return prev
	}
	slices.SortFunc(issues, func(a, b domain.Issue) int {
		return cmp.Compare(a.Identifier, b.Identifier)
	})

	next := automationPollState{
		issues:          make(map[string]observedAutomationIssue, len(issues)),
		trackerComments: make(map[string]map[string]observedAutomationComment, len(prev.trackerComments)),
	}

	detailCache := make(map[string]*domain.Issue)
	getDetail := func(issue domain.Issue) *domain.Issue {
		if detailed, ok := detailCache[issue.ID]; ok {
			return detailed
		}
		detailed, err := tr.FetchIssueDetail(ctx, issue.ID)
		if err != nil {
			slog.Warn("automation: fetch issue detail failed", "automation_issue", issue.Identifier, "error", err)
			detailCache[issue.ID] = nil
			return nil
		}
		detailCache[issue.ID] = detailed
		return detailed
	}

	for _, issue := range issues {
		next.issues[issue.ID] = observedAutomationIssue{
			State: issue.State,
			InBacklog: slices.ContainsFunc(cfg.Tracker.BacklogStates, func(s string) bool {
				return strings.EqualFold(s, issue.State)
			}),
		}
	}
	for _, entry := range entries {
		matches := make([]struct {
			issue   domain.Issue
			trigger orchestrator.AutomationTriggerContext
		}, 0)
		var prevComments map[string]observedAutomationComment
		var nextComments map[string]observedAutomationComment
		if entry.cfg.Trigger.Type == config.AutomationTriggerTrackerComment {
			prevComments = prev.trackerComments[entry.cfg.ID]
			nextComments = make(map[string]observedAutomationComment)
			next.trackerComments[entry.cfg.ID] = nextComments
		}
		for _, issue := range issues {
			if shouldSkipAutomatedIssue(snap, cfg, issue) {
				continue
			}
			prevIssue, seenBefore := prev.issues[issue.ID]
			if !seenBefore {
				continue
			}
			switch entry.cfg.Trigger.Type {
			case config.AutomationTriggerIssueEnteredState:
				if strings.EqualFold(prevIssue.State, issue.State) || !strings.EqualFold(issue.State, entry.cfg.Trigger.State) {
					continue
				}
				if !matchesAutomationFilter(issue, entry, "") {
					continue
				}
				matches = append(matches, struct {
					issue   domain.Issue
					trigger orchestrator.AutomationTriggerContext
				}{
					issue: issue,
					trigger: orchestrator.AutomationTriggerContext{
						Type:          config.AutomationTriggerIssueEnteredState,
						FiredAt:       now,
						AutomationID:  entry.cfg.ID,
						TriggerState:  entry.cfg.Trigger.State,
						PreviousState: prevIssue.State,
						CurrentState:  issue.State,
					},
				})
			case config.AutomationTriggerIssueMovedBacklog:
				inBacklogNow := slices.ContainsFunc(cfg.Tracker.BacklogStates, func(s string) bool {
					return strings.EqualFold(s, issue.State)
				})
				if prevIssue.InBacklog || !inBacklogNow {
					continue
				}
				if !matchesAutomationFilter(issue, entry, "") {
					continue
				}
				matches = append(matches, struct {
					issue   domain.Issue
					trigger orchestrator.AutomationTriggerContext
				}{
					issue: issue,
					trigger: orchestrator.AutomationTriggerContext{
						Type:          config.AutomationTriggerIssueMovedBacklog,
						FiredAt:       now,
						AutomationID:  entry.cfg.ID,
						PreviousState: prevIssue.State,
						CurrentState:  issue.State,
					},
				})
			case config.AutomationTriggerTrackerComment:
				if !matchesAutomationFilter(issue, entry, "") {
					continue
				}
				detailed := getDetail(issue)
				if detailed == nil {
					continue
				}
				commentSnapshot := observedAutomationComment{}
				comment, ok := latestAutomationComment(detailed.Comments)
				if ok {
					commentSnapshot = observedAutomationCommentFromComment(comment)
				}
				nextComments[issue.ID] = commentSnapshot
				prevComment, seenBefore := prevComments[issue.ID]
				if !seenBefore {
					continue
				}
				if !hasNewAutomationComment(prevComment, commentSnapshot) {
					continue
				}
				if !ok {
					continue
				}
				if isAutomationManagedComment(comment) {
					continue
				}
				trigger := orchestrator.AutomationTriggerContext{
					Type:              config.AutomationTriggerTrackerComment,
					FiredAt:           now,
					AutomationID:      entry.cfg.ID,
					CurrentState:      issue.State,
					CommentID:         comment.ID,
					CommentBody:       comment.Body,
					CommentAuthorID:   comment.AuthorID,
					CommentAuthorName: comment.AuthorName,
				}
				if comment.CreatedAt != nil {
					trigger.CommentCreatedAt = comment.CreatedAt.Format(time.RFC3339)
				}
				matches = append(matches, struct {
					issue   domain.Issue
					trigger orchestrator.AutomationTriggerContext
				}{issue: issue, trigger: trigger})
			}
		}
		limit := entry.cfg.Filter.Limit
		if limit > 0 && len(matches) > limit {
			matches = matches[:limit]
		}
		dispatched := 0
		for _, match := range matches {
			if orch.DispatchAutomation(ctx, match.issue, orchestrator.AutomationDispatch{
				AutomationID: entry.cfg.ID,
				ProfileName:  entry.cfg.Profile,
				Instructions: entry.cfg.Instructions,
				AutoResume:   entry.cfg.Policy.AutoResume,
				Trigger:      match.trigger,
			}) {
				dispatched++
			}
		}
		// Parity with runCronAutomation: log accepted event handoffs distinctly
		// from actual worker starts or durable queue rows owned by orchestrator state.
		if dispatched > 0 {
			slog.Info("automation: accepted polled-event dispatch events", "automation", entry.cfg.ID, "count", dispatched, "profile", entry.cfg.Profile)
		}
		if dropped := len(matches) - dispatched; dropped > 0 {
			slog.Debug("automation: polled-event dispatches not accepted before context done", "automation", entry.cfg.ID, "dropped", dropped)
		}
	}

	return next
}

// automationPollStates computes the tracker states to poll for event-driven
// automations. activeStates / terminalStates / completionState must be a
// consistent snapshot obtained via orch.TrackerStatesCfg() — reading
// cfg.Tracker.* directly races with HTTP-handler updates.
func automationPollStates(cfg *config.Config, entries []compiledAutomation, activeStates, terminalStates []string, completionState string) []string {
	states := deduplicateStates(cfg.Tracker.BacklogStates, activeStates, terminalStates, completionState)
	for _, entry := range entries {
		states = append(states, entry.cfg.Filter.States...)
		if entry.cfg.Trigger.State != "" {
			states = append(states, entry.cfg.Trigger.State)
		}
	}
	seen := make(map[string]struct{}, len(states))
	out := make([]string, 0, len(states))
	for _, state := range states {
		if state == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(state)]; ok {
			continue
		}
		seen[strings.ToLower(state)] = struct{}{}
		out = append(out, state)
	}
	return out
}

func latestAutomationComment(comments []domain.Comment) (domain.Comment, bool) {
	if len(comments) == 0 {
		return domain.Comment{}, false
	}
	return comments[len(comments)-1], true
}

func observedAutomationCommentFromComment(comment domain.Comment) observedAutomationComment {
	snapshot := observedAutomationComment{LatestCommentID: comment.ID}
	if comment.CreatedAt != nil {
		snapshot.CommentCreatedAt = comment.CreatedAt.Format(time.RFC3339)
	}
	return snapshot
}

func hasNewAutomationComment(prev, current observedAutomationComment) bool {
	if current.LatestCommentID == "" && current.CommentCreatedAt == "" {
		return false
	}
	if prev.LatestCommentID == "" && prev.CommentCreatedAt == "" {
		return true
	}
	if current.LatestCommentID != "" {
		return !strings.EqualFold(prev.LatestCommentID, current.LatestCommentID)
	}
	return current.CommentCreatedAt != prev.CommentCreatedAt
}

func isAutomationManagedComment(comment domain.Comment) bool {
	return tracker.IsManagedComment(comment) ||
		strings.HasPrefix(comment.Body, "🤖 **Agent needs your input**") ||
		strings.EqualFold(strings.TrimSpace(comment.AuthorName), "Itervox")
}

// logAutomationCompileSummary emits a single INFO line with the outcome of
// compileAutomations so users can verify that their configured automations
// actually registered. Per-rule skip reasons (unknown profile, disabled
// profile, invalid cron, invalid regex) are already emitted as WARN by
// compileAutomations itself; this summary makes the net registered/dropped
// counts visible without grepping.
func logAutomationCompileSummary(total int, compiled compiledAutomationSet) {
	if total == 0 {
		slog.Info("automations: no automations configured in WORKFLOW.md")
		return
	}
	registered := len(compiled.cron) + len(compiled.polledEvents) + len(compiled.inputRequired) + len(compiled.runFailed) + len(compiled.prOpened) + len(compiled.rateLimited) + len(compiled.blockersResolved)
	slog.Info("automations: compiled",
		"total_configured", total,
		"registered", registered,
		"dropped", total-registered,
		"cron", len(compiled.cron),
		"input_required", len(compiled.inputRequired),
		"polled_events", len(compiled.polledEvents),
		"run_failed", len(compiled.runFailed),
		"pr_opened", len(compiled.prOpened),
		"rate_limited", len(compiled.rateLimited),
		"blockers_resolved", len(compiled.blockersResolved),
	)
}
