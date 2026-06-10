package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// Errors returned by TestAutomation (T-10) so HTTP handlers can map them to
// distinct status codes without parsing strings.
var (
	errInvalidTestAutomation = errors.New("orchestrator: test automation requires both rule id and issue identifier")
	errAutomationNotFound    = errors.New("orchestrator: automation rule not found")
	errAutomationQueueFull   = errors.New("orchestrator: automation dispatch event not accepted")
)

// AutomationTriggerContext is the per-dispatch snapshot of why an automation
// fired. It is passed into the prompt renderer as the `trigger.*` Liquid
// binding so automation instructions can reference the firing condition
// (e.g. {{ trigger.input_context }}, {{ trigger.comment_body }}).
//
// Only the fields relevant to the trigger type are populated; the rest are
// zero-valued. The struct is intentionally flat (one type, all triggers)
// because the Liquid renderer needs uniform field access across all
// trigger types. The fields are grouped below by which trigger populates
// them — gap §3.2 captures the readability win without restructuring
// every read site through a discriminated union (which would also force
// the renderer to do per-type dispatch).
type AutomationTriggerContext struct {
	// ─── Common fields (every trigger sets these) ─────────────────────
	Type         string
	FiredAt      time.Time
	AutomationID string
	CurrentState string

	// ─── Cron trigger fields ──────────────────────────────────────────
	Cron     string
	Timezone string

	// ─── issue_entered_state / issue_moved_to_backlog fields ──────────
	TriggerState  string
	PreviousState string

	// ─── input_required trigger fields ────────────────────────────────
	InputContext   string
	BlockedProfile string
	BlockedBackend string

	// ─── tracker_comment_added trigger fields ─────────────────────────
	CommentID         string
	CommentBody       string
	CommentAuthorID   string
	CommentAuthorName string
	CommentCreatedAt  string

	// ─── run_failed trigger fields ────────────────────────────────────
	ErrorMessage   string
	WillRetry      bool
	RetryAttempt   int
	RetryBackoffMs int

	// ─── pr_opened / pr_merged trigger fields (gap B / P1) ────────────
	PRURL        string
	PRBranch     string
	PRBaseBranch string
	// PRNumber is set on pr_opened and pr_merged triggers.
	PRNumber int
	// MergedSHA is set on pr_merged only.
	MergedSHA string
	// MergedAt is set on pr_merged only. Zero value means "not a merge event".
	MergedAt time.Time

	// ─── rate_limited trigger fields (gap E) ──────────────────────────
	FailedProfile         string
	FailedBackend         string
	PromptTokensTotal     int
	CompletionTokensTotal int
	SwitchedToProfile     string
	SwitchedToBackend     string

	// ─── blockers_resolved trigger fields ─────────────────────────────
	ResolvedBlockers       []domain.BlockerRef
	PreviouslyBlockedBy    []domain.BlockerRef
	DependencyAuditVersion int64
	DependencyUnblockedAt  time.Time
	MoveToState            string
}

// AutomationDispatch is the message an automation producer (cron goroutine,
// poll goroutine, or event-loop fallback handler) sends to the orchestrator
// to request a helper-worker run. ProfileName must reference a configured
// agent profile; Instructions is appended to the rendered prompt after the
// profile prompt. AutoResume opts the helper into the standard
// input-required resume contract when the blocked worker's profile allows
// provide_input.
type AutomationDispatch struct {
	AutomationID string
	ProfileName  string
	Instructions string
	Trigger      AutomationTriggerContext
	AutoResume   bool
	MoveToState  string
	// UseIssueLifecycle keeps automation prompt attribution but lets the worker
	// run through the normal issue lifecycle: working transition, PR/comment
	// handling, completion-state transition, and active-state reconciliation.
	UseIssueLifecycle bool
}

// InputRequiredAutomation is the compiled, event-loop-ready form of an
// `input_required` automation rule. Constructed by compileAutomations in
// cmd/itervox and installed via SetInputRequiredAutomations; evaluated by
// dispatchMatchingInputRequiredAutomations on every TerminalInputRequired
// event. IdentifierRegex and InputContextRegex are nil when the user did
// not configure a regex filter.
type InputRequiredAutomation struct {
	ID                string
	ProfileName       string
	Instructions      string
	MatchMode         string
	States            []string
	LabelsAny         []string
	IdentifierRegex   *regexp.Regexp
	InputContextRegex *regexp.Regexp
	AutoResume        bool
	// MaxAge, when > 0, restricts the rule to input-required entries whose
	// QueuedAt is within the window. Older entries are considered stale and
	// skipped. 0 means "no age limit".
	MaxAge time.Duration
}

// RunFailedAutomation is the compiled, event-loop-ready form of a
// `run_failed` automation rule. Fires when a worker exits with
// TerminalFailed after all retries are exhausted. Unlike
// InputRequiredAutomation there is no InputContextRegex field — the failure
// context comes from AutomationTriggerContext.ErrorMessage instead.
type RunFailedAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
}

// PROpenedAutomation (gap B) is the compiled, event-loop-ready form of a
// `pr_opened` automation rule. The worker's post-run logic emits a
// `pr_opened` signal as soon as it detects a new PR; this rule reacts to
// it. The trigger context carries the PR URL + branch so the prompt can
// reference them via the `trigger.*` Liquid binding.
type PROpenedAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
}

// RateLimitedAutomation (gap E) is the compiled, event-loop-ready form of a
// `rate_limited` automation rule. Fires only when an exhausted-retry
// terminal exit was classified by the orchestrator as rate-limit-driven
// (vendor 429 / quota exhaustion). The policy carries SwitchToProfile +
// SwitchToBackend so an auto_resume run can re-dispatch the issue under a
// different agent instead of just commenting and giving up.
type RateLimitedAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
	AutoResume      bool
	SwitchToProfile string
	SwitchToBackend string // empty | "claude" | "codex"
	Cooldown        time.Duration
}

// BlockersResolvedAutomation is the compiled, event-loop-ready form of a
// `blockers_resolved` automation rule. It fires only when dependency audit
// observes a previously blocked issue becoming unblocked.
type BlockersResolvedAutomation struct {
	ID              string
	ProfileName     string
	Instructions    string
	MatchMode       string
	States          []string
	LabelsAny       []string
	IdentifierRegex *regexp.Regexp
	MoveToState     string
}

// setAutomationRegistry installs a compiled-automation slice under the
// orchestrator's automationsMu write lock. Generic helper that DRYs up the
// per-trigger Set helpers (input_required / run_failed / pr_opened /
// rate_limited). Gap §3.1 partial. Safe to call from any goroutine.
func setAutomationRegistry[T any](o *Orchestrator, dst *[]T, src []T) {
	cp := append([]T(nil), src...)
	o.automationsMu.Lock()
	*dst = cp
	o.automationsMu.Unlock()
}

// snapAutomationRegistry returns a lock-free snapshot of a compiled-automation
// slice, taken under automationsMu's read lock. Generic sibling of
// setAutomationRegistry. Returns nil for empty slices so hot paths can
// short-circuit with a single len() check.
//
// IMPORTANT: takes a pointer to the slice (not the slice itself) so the
// header read happens INSIDE the lock. A by-value parameter would capture
// the slice header at call time, racing with concurrent setAutomationRegistry
// calls that update *dst under the write lock.
func snapAutomationRegistry[T any](o *Orchestrator, src *[]T) []T {
	o.automationsMu.RLock()
	defer o.automationsMu.RUnlock()
	if len(*src) == 0 {
		return nil
	}
	cp := make([]T, len(*src))
	copy(cp, *src)
	return cp
}

// SetInputRequiredAutomations installs the compiled input-required automation
// rules. Safe to call from any goroutine and at any time — the automations
// goroutine re-registers these on each reload.
func (o *Orchestrator) SetInputRequiredAutomations(automations []InputRequiredAutomation) {
	setAutomationRegistry(o, &o.inputRequiredAutomations, automations)
}

// SetRunFailedAutomations installs the compiled terminal-failure automation
// rules. Safe to call from any goroutine and at any time.
func (o *Orchestrator) SetRunFailedAutomations(automations []RunFailedAutomation) {
	setAutomationRegistry(o, &o.runFailedAutomations, automations)
}

// SetPROpenedAutomations (gap B) installs the compiled pr_opened automation
// rules. Safe to call from any goroutine and at any time.
func (o *Orchestrator) SetPROpenedAutomations(automations []PROpenedAutomation) {
	setAutomationRegistry(o, &o.prOpenedAutomations, automations)
}

// snapPROpenedAutomations returns a lock-free snapshot for the worker's
// pr_opened dispatch helper to iterate.
func (o *Orchestrator) snapPROpenedAutomations() []PROpenedAutomation {
	return snapAutomationRegistry(o, &o.prOpenedAutomations)
}

// DispatchPROpenedAutomations (gap B) is invoked by the worker right after
// it detects + comments a new PR for the issue. For every matching rule,
// the helper queues an EventDispatchAutomation through the orchestrator
// event loop carrying the PR URL/branch/base in the trigger context. Safe
// to call from any goroutine.
func (o *Orchestrator) DispatchPROpenedAutomations(ctx context.Context, issue domain.Issue, prURL, prBranch, baseBranch string) {
	rules := o.snapPROpenedAutomations()
	if len(rules) == 0 {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	for _, rule := range rules {
		if sendCtx.Err() != nil {
			return
		}
		if !matchesAutomationFilter(issue, rule.MatchMode, rule.States, rule.LabelsAny, rule.IdentifierRegex, nil, "") {
			continue
		}
		dispatch := AutomationDispatch{
			AutomationID: rule.ID,
			ProfileName:  rule.ProfileName,
			Instructions: rule.Instructions,
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerPROpened,
				FiredAt:      now,
				AutomationID: rule.ID,
				CurrentState: issue.State,
				PRURL:        prURL,
				PRBranch:     prBranch,
				PRBaseBranch: baseBranch,
			},
		}
		// Queue through the events channel so the actual run is started by
		// the single event-loop goroutine, preserving the orchestrator's
		// state-mutation invariants.
		select {
		case o.events <- OrchestratorEvent{
			Type:       EventDispatchAutomation,
			Issue:      &issue,
			Automation: &dispatch,
		}:
		case <-sendCtx.Done():
			slog.Warn("orchestrator: pr_opened dispatch event not accepted before context done",
				"identifier", issue.Identifier,
				"automation", rule.ID,
				"pr_url", prURL,
				"error", sendCtx.Err())
			return
		}
	}
}

// snapInputRequiredAutomations returns a lock-free snapshot for event-loop use.
func (o *Orchestrator) snapInputRequiredAutomations() []InputRequiredAutomation {
	return snapAutomationRegistry(o, &o.inputRequiredAutomations)
}

// snapRunFailedAutomations returns a lock-free snapshot for event-loop use.
func (o *Orchestrator) snapRunFailedAutomations() []RunFailedAutomation {
	return snapAutomationRegistry(o, &o.runFailedAutomations)
}

// SetRateLimitedAutomations (gap E) installs the compiled rate_limited rules.
// Safe to call from any goroutine.
func (o *Orchestrator) SetRateLimitedAutomations(automations []RateLimitedAutomation) {
	setAutomationRegistry(o, &o.rateLimitedAutomations, automations)
}

// snapRateLimitedAutomations returns a lock-free snapshot for event-loop use.
func (o *Orchestrator) snapRateLimitedAutomations() []RateLimitedAutomation {
	return snapAutomationRegistry(o, &o.rateLimitedAutomations)
}

// SetBlockersResolvedAutomations installs dependency-unblock automation rules.
// Safe to call from any goroutine.
func (o *Orchestrator) SetBlockersResolvedAutomations(automations []BlockersResolvedAutomation) {
	setAutomationRegistry(o, &o.blockersResolvedAutomations, automations)
}

func (o *Orchestrator) snapBlockersResolvedAutomations() []BlockersResolvedAutomation {
	return snapAutomationRegistry(o, &o.blockersResolvedAutomations)
}

// DispatchAutomation queues an automation worker through the event loop.
// Safe to call from any goroutine.
func (o *Orchestrator) DispatchAutomation(ctx context.Context, issue domain.Issue, automation AutomationDispatch) bool {
	select {
	case o.events <- OrchestratorEvent{
		Type:       EventDispatchAutomation,
		Issue:      &issue,
		Automation: &automation,
	}:
		return true
	case <-ctx.Done():
		slog.Warn("orchestrator: automation dispatch event not accepted before context done",
			"identifier", issue.Identifier,
			"automation", automation.AutomationID,
			"error", ctx.Err())
		return false
	}
}

// TestAutomationTriggerType is the synthetic trigger type stamped on
// run records produced by the "Try this automation" button (T-10). This
// allows the timeline / logs surfaces to render test fires under the same
// "automation runs only" filter while still distinguishing them from
// production automations in run inspectors.
const TestAutomationTriggerType = "test"

// TestAutomation dispatches an automation worker for the named rule against
// the given issue, regardless of the rule's normal trigger schedule. The
// resulting run is tagged with TriggerType="test" (T-10).
//
// Safe for any goroutine. Returns an error if the rule does not exist, the
// referenced profile is missing, or the issue cannot be located via the
// tracker. The actual dispatch is asynchronous: a successful return only means
// the EventDispatchAutomation handoff was accepted by the event loop.
func (o *Orchestrator) TestAutomation(ctx context.Context, automationID, identifier string) error {
	if automationID == "" || identifier == "" {
		return errInvalidTestAutomation
	}

	o.cfgMu.RLock()
	var rule *config.AutomationConfig
	for i := range o.cfg.Automations {
		if o.cfg.Automations[i].ID == automationID {
			rule = &o.cfg.Automations[i]
			break
		}
	}
	o.cfgMu.RUnlock()
	if rule == nil {
		return errAutomationNotFound
	}

	// Look up the issue. If the tracker is unavailable, fall back to a
	// minimal Issue value built from the identifier — startAutomationRun
	// only needs ID/Identifier/State for its eligibility checks.
	issue := domain.Issue{ID: identifier, Identifier: identifier, State: ""}
	if o.tracker != nil {
		issues, err := o.tracker.FetchCandidateIssues(ctx)
		if err != nil {
			slog.Warn("orchestrator: TestAutomation could not fetch issues", "error", err, "identifier", identifier)
		} else {
			for i := range issues {
				if issues[i].Identifier == identifier {
					issue = issues[i]
					break
				}
			}
		}
	}

	dispatch := AutomationDispatch{
		AutomationID: rule.ID,
		ProfileName:  rule.Profile,
		Instructions: rule.Instructions,
		Trigger: AutomationTriggerContext{
			Type:         TestAutomationTriggerType,
			FiredAt:      time.Now(),
			AutomationID: rule.ID,
		},
	}
	if !o.DispatchAutomation(ctx, issue, dispatch) {
		return errAutomationQueueFull
	}
	return nil
}

func (o *Orchestrator) dispatchMatchingInputRequiredAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	entry *InputRequiredEntry,
	now time.Time,
	prevRun *RunEntry,
) {
	if entry == nil {
		return
	}
	// v0.2.0 todolist5 B1 — self-reentry guard. If the worker that just exited
	// input_required was itself dispatched by an input_required automation,
	// firing another input_required automation on this issue would loop
	// indefinitely until an unrelated rate-limit / retry budget kicks in.
	// Skip dispatch; user-launched workers exiting input_required (AutomationID
	// empty) still fire automations as before.
	if prevRun != nil && prevRun.AutomationID != "" &&
		prevRun.TriggerType == config.AutomationTriggerInputRequired {
		state.AutomationDropsSelfReentryTotal++
		slog.Warn("orchestrator: input-required dispatch suppressed (self-reentry)",
			"identifier", issue.Identifier,
			"prev_automation", prevRun.AutomationID,
			"prev_trigger", prevRun.TriggerType,
			"automation_drops_self_reentry_total", state.AutomationDropsSelfReentryTotal)
		return
	}
	automations := o.snapInputRequiredAutomations()
	if len(automations) == 0 {
		// No input-required automations registered. Emit at Debug so enabling
		// -verbose reveals the common "I configured an input_required
		// automation but it never fired" case — the most likely reason is
		// the referenced profile doesn't exist (compileAutomations logs
		// "skipping rule with unknown profile" at startup).
		slog.Debug("orchestrator: input-required fired but no automations registered",
			"identifier", issue.Identifier)
		return
	}
	matchCount := 0
	for _, automation := range automations {
		if !matchesInputRequiredAutomation(issue, automation, entry.Context) {
			continue
		}
		// Stale-entry skip (gap A): if the rule sets a max age and the entry
		// has been queued longer than that, skip the dispatch. The entry's
		// Stale flag is updated by the snapshot path; this path mirrors the
		// same predicate so the skip decision matches what the dashboard
		// shows.
		if automation.MaxAge > 0 && !entry.QueuedAt.IsZero() && now.Sub(entry.QueuedAt) > automation.MaxAge {
			slog.Debug("orchestrator: input-required automation skipped (stale entry)",
				"identifier", issue.Identifier,
				"automation", automation.ID,
				"queued_at", entry.QueuedAt,
				"age_minutes", int(now.Sub(entry.QueuedAt).Minutes()),
				"max_age_minutes", int(automation.MaxAge.Minutes()))
			continue
		}
		matchCount++
		o.dispatchOrQueueAutomation(ctx, state, issue, AutomationDispatch{
			AutomationID: automation.ID,
			ProfileName:  automation.ProfileName,
			Instructions: automation.Instructions,
			AutoResume:   automation.AutoResume,
			Trigger: AutomationTriggerContext{
				Type:           config.AutomationTriggerInputRequired,
				FiredAt:        now,
				AutomationID:   automation.ID,
				InputContext:   entry.Context,
				BlockedProfile: entry.ProfileName,
				BlockedBackend: entry.Backend,
			},
		}, now)
	}
	if matchCount == 0 {
		// Registered automations exist but none matched the current
		// issue/context. Emit at Debug so -verbose surfaces the typical
		// "my input_context_regex didn't match the agent's question"
		// troubleshooting case.
		slog.Debug("orchestrator: input-required automations registered but none matched",
			"identifier", issue.Identifier,
			"registered_count", len(automations),
			"input_context_excerpt", truncateForLog(entry.Context, 120))
	}
}

func (o *Orchestrator) replayPersistedInputRequiredAutomations(ctx context.Context, state *State, now time.Time) {
	if len(state.InputRequiredIssues) == 0 {
		return
	}
	identifiers := make([]string, 0, len(state.InputRequiredIssues))
	for identifier := range state.InputRequiredIssues {
		identifiers = append(identifiers, identifier)
	}
	slices.Sort(identifiers)
	for _, identifier := range identifiers {
		entry := state.InputRequiredIssues[identifier]
		issue := o.fetchInputRequiredIssue(ctx, entry)
		if issue == nil {
			continue
		}
		// Persistence-replay path: no live RunEntry — the previous worker is
		// gone (daemon restart). B1 self-reentry guard does not apply; nil.
		o.dispatchMatchingInputRequiredAutomations(ctx, state, *issue, entry, now, nil)
	}
}

func (o *Orchestrator) fetchInputRequiredIssue(ctx context.Context, entry *InputRequiredEntry) *domain.Issue {
	if entry == nil {
		return nil
	}
	if entry.IssueID != "" {
		issue, err := o.tracker.FetchIssueDetail(ctx, entry.IssueID)
		if err != nil {
			slog.Warn("orchestrator: input-required replay detail fetch failed",
				"identifier", entry.Identifier,
				"issue_id", entry.IssueID,
				"error", err)
			return nil
		}
		return issue
	}
	if entry.Identifier == "" {
		return nil
	}
	issue, err := o.tracker.FetchIssueByIdentifier(ctx, entry.Identifier)
	if err != nil {
		slog.Warn("orchestrator: input-required replay identifier fetch failed",
			"identifier", entry.Identifier,
			"error", err)
		return nil
	}
	return issue
}

// truncateForLog returns s truncated to max runes with an ellipsis, so noisy
// fields (question text, comment bodies) don't blow up log lines.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Byte-safe truncation is fine here; we're emitting for debugging not
	// display, and we're adding an explicit ellipsis.
	return s[:max] + "…"
}

func matchesInputRequiredAutomation(issue domain.Issue, automation InputRequiredAutomation, inputContext string) bool {
	return matchesAutomationFilter(
		issue,
		automation.MatchMode,
		automation.States,
		automation.LabelsAny,
		automation.IdentifierRegex,
		automation.InputContextRegex,
		inputContext,
	)
}

func (o *Orchestrator) dispatchMatchingRunFailedAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	errorMessage string,
	attempt int,
) {
	automations := o.snapRunFailedAutomations()
	if len(automations) == 0 {
		return
	}
	for _, automation := range automations {
		if !matchesAutomationFilter(
			issue,
			automation.MatchMode,
			automation.States,
			automation.LabelsAny,
			automation.IdentifierRegex,
			nil,
			"",
		) {
			continue
		}
		o.dispatchOrQueueAutomation(ctx, state, issue, AutomationDispatch{
			AutomationID: automation.ID,
			ProfileName:  automation.ProfileName,
			Instructions: automation.Instructions,
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerRunFailed,
				FiredAt:      now,
				AutomationID: automation.ID,
				CurrentState: issue.State,
				ErrorMessage: errorMessage,
				WillRetry:    false,
				RetryAttempt: attempt,
			},
		}, now)
	}
}

func (o *Orchestrator) dispatchMatchingBlockersResolvedAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	audit DependencyAuditEntry,
	now time.Time,
) {
	automations := o.snapBlockersResolvedAutomations()
	if len(automations) == 0 || audit.LastTransitionVersion == 0 {
		return
	}
	for _, automation := range automations {
		if !matchesAutomationFilter(
			issue,
			automation.MatchMode,
			automation.States,
			automation.LabelsAny,
			automation.IdentifierRegex,
			nil,
			"",
		) {
			continue
		}
		dispatch := AutomationDispatch{
			AutomationID: automation.ID,
			ProfileName:  automation.ProfileName,
			Instructions: automation.Instructions,
			MoveToState:  automation.MoveToState,
			Trigger: AutomationTriggerContext{
				Type:                   config.AutomationTriggerBlockersResolved,
				FiredAt:                now,
				AutomationID:           automation.ID,
				CurrentState:           issue.State,
				ResolvedBlockers:       copyBlockerRefs(audit.ResolvedBlockers),
				PreviouslyBlockedBy:    copyBlockerRefs(audit.BlockedBy),
				DependencyAuditVersion: audit.LastTransitionVersion,
				DependencyUnblockedAt:  audit.UnblockedAt,
				MoveToState:            automation.MoveToState,
			},
		}
		o.dispatchOrQueueAutomation(ctx, state, issue, dispatch, now)
	}
}

func matchesAutomationFilter(
	issue domain.Issue,
	matchMode string,
	states []string,
	labelsAny []string,
	identifierRegex *regexp.Regexp,
	inputContextRegex *regexp.Regexp,
	inputContext string,
) bool {
	checks := make([]bool, 0, 4)
	if identifierRegex != nil {
		checks = append(checks, identifierRegex.MatchString(issue.Identifier))
	}
	if len(states) > 0 {
		checks = append(checks, slices.ContainsFunc(states, func(s string) bool {
			return strings.EqualFold(s, issue.State)
		}))
	}
	if len(labelsAny) > 0 {
		labelMatch := slices.ContainsFunc(labelsAny, func(wanted string) bool {
			return slices.ContainsFunc(issue.Labels, func(l string) bool {
				return strings.EqualFold(l, wanted)
			})
		})
		checks = append(checks, labelMatch)
	}
	if inputContextRegex != nil {
		checks = append(checks, inputContextRegex.MatchString(inputContext))
	}
	if len(checks) == 0 {
		return true
	}
	if matchMode == config.AutomationFilterMatchAny {
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

func (o *Orchestrator) startAutomationRun(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	automation AutomationDispatch,
) bool {
	if automation.ProfileName == "" {
		return false
	}
	if _, running := state.Running[issue.ID]; running {
		return false
	}
	if _, claimed := state.Claimed[issue.ID]; claimed {
		return false
	}
	if AvailableSlots(*state) <= 0 {
		return false
	}

	o.cfgMu.RLock()
	profile, ok := o.cfg.Agent.Profiles[automation.ProfileName]
	defaultCommand := o.cfg.Agent.Command
	defaultBackend := o.cfg.Agent.Backend
	hosts := append([]string{}, o.cfg.Agent.SSHHosts...)
	dispatchStrategy := o.cfg.Agent.DispatchStrategy
	o.cfgMu.RUnlock()

	if !ok {
		slog.Warn("orchestrator: automation profile not found", "identifier", issue.Identifier, "profile", automation.ProfileName, "automation", automation.AutomationID)
		return false
	}
	if !config.ProfileEnabled(profile) {
		slog.Warn("orchestrator: automation profile disabled", "identifier", issue.Identifier, "profile", automation.ProfileName, "automation", automation.AutomationID)
		return false
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	workerHost := o.selectWorkerHost(hosts, dispatchStrategy, *state)

	// Automation workers follow the same default/profile/per-issue backend
	// resolution as normal worker and reviewer dispatch. Rate-limit auto-switch
	// recovery is a deliberate final override below because it represents the
	// backend that just became available for the fallback run.
	issueBackend := o.issueBackendForDispatch(*state, issue.Identifier)
	agentCommand, runnerCommand, backend := resolveBackendForIssue(
		defaultCommand, defaultBackend, &profile, issueBackend,
	)
	if issueBackend != "" {
		slog.Info("orchestrator: using per-issue backend override for automation",
			"identifier", issue.Identifier, "backend", issueBackend)
	}
	if automation.Trigger.Type == config.AutomationTriggerRateLimited &&
		automation.AutoResume &&
		automation.Trigger.SwitchedToBackend != "" {
		backend = automation.Trigger.SwitchedToBackend
		runnerCommand = agent.CommandWithBackendHint(agentCommand, backend)
	}

	if o.DryRun {
		workerCancel()
		slog.Info("orchestrator: [DRY-RUN] would dispatch automation",
			"identifier", issue.Identifier,
			"automation", automation.AutomationID,
			"profile", automation.ProfileName,
			"worker_host", workerHost,
			"backend", backend)
		state.Claimed[issue.ID] = struct{}{}
		return true
	}

	state.Claimed[issue.ID] = struct{}{}
	attempt := 0
	kind := "automation"
	if automation.UseIssueLifecycle {
		kind = "worker"
	}
	state.Running[issue.ID] = &RunEntry{
		Issue:        issue,
		WorkerHost:   workerHost,
		Backend:      backend,
		ProfileName:  automation.ProfileName,
		Kind:         kind,
		AutomationID: automation.AutomationID,
		TriggerType:  automation.Trigger.Type,
		StartedAt:    now,
		RetryAttempt: &attempt,
		WorkerCancel: workerCancel,
	}

	o.workerCancelsMu.Lock()
	o.workerCancels[issue.Identifier] = workerCancel
	o.workerCancelsMu.Unlock()

	slog.Info("orchestrator: dispatching automation worker",
		"identifier", issue.Identifier,
		"automation", automation.AutomationID,
		"profile", automation.ProfileName,
		"backend", backend,
	)

	o.recordAutomationDispatch(issue, automation, backend)

	go o.runWorker(workerCtx, issue, attempt, workerHost, runnerCommand, backend, automation.ProfileName, false, nil, &automation)
	return true
}

// AutomationFiredLogPrefix is the canonical leading token of the synthetic
// per-issue log entry written when an automation dispatches a worker. The web
// Logs page filters on this prefix to render an "automation events only"
// chip (T-4), so the Go writer and TS reader must agree on its exact value.
// Defined as a constant rather than a string literal to prevent drift.
const AutomationFiredLogPrefix = "AUTOMATION FIRED"

// recordAutomationDispatch appends a structured `AUTOMATION FIRED` entry to
// the per-issue log buffer (F-2). This is the only path that surfaces the
// "this run was kicked off by automation X" signal in the per-issue Logs view
// — the daemon's slog line goes to the global file sink, which is invisible
// when an operator drills into a single issue.
//
// The line has a fixed prefix so the Logs page can grep for `AUTOMATION FIRED`
// to scope its filter chip (T-4). Every field that follows is best-effort:
// long contexts are truncated to keep buffer entries within the 64 KiB
// per-line cap.
func (o *Orchestrator) recordAutomationDispatch(issue domain.Issue, automation AutomationDispatch, backend string) {
	if o.logBuf == nil {
		return
	}
	context := strings.TrimSpace(automation.Trigger.InputContext)
	if context == "" {
		context = strings.TrimSpace(automation.Trigger.CommentBody)
	}
	if len(context) > 240 {
		context = context[:237] + "…"
	}
	contextLine := ""
	if context != "" {
		contextLine = fmt.Sprintf(`  context: %q`, context)
	}
	parts := []string{
		fmt.Sprintf("%s · %s", AutomationFiredLogPrefix, automation.AutomationID),
		fmt.Sprintf("  trigger: %s", automation.Trigger.Type),
	}
	if contextLine != "" {
		parts = append(parts, contextLine)
	}
	parts = append(parts, fmt.Sprintf("  profile: %s · backend: %s", automation.ProfileName, backend))
	msg := strings.Join(parts, "\n")
	o.logBuf.Add(issue.Identifier, makeBufLine("INFO", msg))
}

func filterAllowedActionsForAutomation(actions []string, automation *AutomationDispatch) []string {
	normalized := config.NormalizeAllowedActions(actions)
	if automation == nil {
		return normalized
	}
	removeProvideInput := automation.Trigger.Type == config.AutomationTriggerInputRequired && !automation.AutoResume
	removeMoveState := automation.Trigger.Type == config.AutomationTriggerBlockersResolved && automation.MoveToState == ""
	if !removeProvideInput && !removeMoveState {
		return normalized
	}
	return slices.DeleteFunc(normalized, func(action string) bool {
		return (removeProvideInput && action == config.AgentActionProvideInput) ||
			(removeMoveState && action == config.AgentActionMoveState)
	})
}
