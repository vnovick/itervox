package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// defaultRateLimitErrorPatterns are case-insensitive substrings that
// classify a terminal-failure exit as rate-limit-driven. The classifier
// is intentionally conservative: a single hit means "this exit was
// definitely caused by vendor throttling" rather than "this exit might
// have been related to throttling."
//
// The list is small on purpose. If a vendor adds a new rate-limit error
// shape we'd rather miss the trigger and fall back to `run_failed` than
// false-positive and aggressively swap profiles on, e.g., a generic 5xx.
//
// Operators can extend or override this list via
// `cfg.Agent.RateLimitErrorPatterns` (gap §5.1). When the cfg slice is
// empty, this default list is used.
var defaultRateLimitErrorPatterns = []string{
	"rate_limit_exceeded",
	"rate limit",
	"429",
	"quota",
	"too many requests",
	// Anthropic vendor surface — phrasings used in 2026 by Claude Max
	// (extra-usage quota), Pro/Team tier limits, and Codex/Claude wrappers.
	// None of these substrings appears in generic 5xx, network, or compile
	// errors, so the conservative-by-default classification holds.
	"out of extra usage",
	"reached the limit for your current claude",
	"out of credits",
	"resets at",
}

// IsRateLimitFailure reports whether an exhausted-retry terminal error
// looks like a vendor rate-limit / quota exhaustion. Match is
// case-insensitive substring against the built-in default pattern list.
// Use IsRateLimitFailureWithPatterns to pass a custom list (e.g. from
// `cfg.Agent.RateLimitErrorPatterns`).
func IsRateLimitFailure(errorMessage string) bool {
	return IsRateLimitFailureWithPatterns(errorMessage, nil)
}

// IsRateLimitFailureWithPatterns is the patterns-aware sibling of
// IsRateLimitFailure. Empty/nil patterns argument falls back to the
// built-in default list. Gap §5.1.
func IsRateLimitFailureWithPatterns(errorMessage string, patterns []string) bool {
	if errorMessage == "" {
		return false
	}
	if len(patterns) == 0 {
		patterns = defaultRateLimitErrorPatterns
	}
	lower := strings.ToLower(errorMessage)
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// dispatchMatchingRateLimitedAutomations is the rate-limited sibling of
// dispatchMatchingRunFailedAutomations. Called from event_loop.go when a
// terminal failure is classified as rate-limit-driven AND the operator has
// configured at least one rate_limited rule. The two helpers are separate
// so rate_limited recovery can take precedence when a switch is queued, while
// generic run_failed handling remains the fallback when no switch fires.
//
// Per-issue switch-cap and per-(issue, profile) cooldown are evaluated
// here so the rule never fires beyond what the operator authorised. Each
// matching rule emits an EventDispatchAutomation through the orchestrator's
// events channel; the existing event-loop handler then claims a slot and
// spawns the helper. When the rule has AutoResume + SwitchToProfile, the
// orchestrator additionally overrides state.IssueProfiles for the issue so
// the next dispatch picks up the new profile.
func (o *Orchestrator) dispatchMatchingRateLimitedAutomations(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	failedProfile string,
	failedBackend string,
	errorMessage string,
	attempt int,
	promptTokensTotal, completionTokensTotal int,
) int {
	rules := o.snapRateLimitedAutomations()
	if len(rules) == 0 {
		return 0
	}
	queued := 0
	for _, rule := range rules {
		if !matchesAutomationFilter(
			issue,
			rule.MatchMode,
			rule.States,
			rule.LabelsAny,
			rule.IdentifierRegex,
			nil,
			"",
		) {
			continue
		}
		if !o.allowRateLimitSwitch(issue.ID, now) {
			slog.Warn("orchestrator: rate_limited switch cap reached, skipping",
				"identifier", issue.Identifier, "automation", rule.ID,
				"failed_profile", failedProfile)
			// Gap §6.5 — surface "you've exhausted the auto-switch budget
			// for this issue" to the operator via a tracker comment so
			// they don't have to grep daemon logs to know why a stuck
			// issue stopped auto-switching. Fire-and-forget.
			if o.claimRateLimitCapComment(issue.ID, now) {
				go o.commentRateLimitCapExhausted(issue, failedProfile)
			}
			continue
		}
		cooldownKey := issue.ID + "|" + failedProfile
		if untilT, muted := o.rateLimitCooldownUntil(cooldownKey); muted && now.Before(untilT) {
			slog.Info("orchestrator: rate_limited rule muted by cooldown",
				"identifier", issue.Identifier, "automation", rule.ID,
				"until", untilT.Format(time.RFC3339))
			continue
		}
		dispatchProfile := rule.ProfileName
		if rule.AutoResume && rule.SwitchToProfile != "" {
			dispatchProfile = rule.SwitchToProfile
		}
		dispatch := AutomationDispatch{
			AutomationID:      rule.ID,
			ProfileName:       dispatchProfile,
			Instructions:      rule.Instructions,
			AutoResume:        rule.AutoResume,
			UseIssueLifecycle: rule.AutoResume && rule.SwitchToProfile != "",
			Trigger: AutomationTriggerContext{
				Type:                  config.AutomationTriggerRateLimited,
				FiredAt:               now,
				AutomationID:          rule.ID,
				CurrentState:          issue.State,
				ErrorMessage:          errorMessage,
				RetryAttempt:          attempt,
				FailedProfile:         failedProfile,
				FailedBackend:         failedBackend,
				PromptTokensTotal:     promptTokensTotal,
				CompletionTokensTotal: completionTokensTotal,
				SwitchedToProfile:     rule.SwitchToProfile,
				SwitchedToBackend:     rule.SwitchToBackend,
			},
		}
		if o.dispatchOrQueueAutomation(ctx, state, issue, dispatch, now) {
			queued++
		} else {
			continue
		}

		o.recordRateLimitSwitch(issue.ID, now)
		if rule.Cooldown > 0 {
			o.setRateLimitCooldown(cooldownKey, now.Add(rule.Cooldown))
		}

		// Auto-switch the issue's profile/backend so the next dispatch
		// picks up the new agent. Only when AutoResume is set AND we have
		// a switch_to_profile (validator already ensures this for
		// rate_limited rules, but defend in depth).
		if rule.AutoResume && rule.SwitchToProfile != "" && state != nil {
			if state.IssueProfiles == nil {
				state.IssueProfiles = make(map[string]string)
			}
			state.IssueProfiles[issue.Identifier] = rule.SwitchToProfile
			if rule.SwitchToBackend != "" {
				if state.IssueBackends == nil {
					state.IssueBackends = make(map[string]string)
				}
				state.IssueBackends[issue.Identifier] = rule.SwitchToBackend
			}
			// Gap §1.3 — track the auto-switch so the override can be
			// cleared on successful exit. Operator-set overrides (via
			// SetIssueProfile/SetIssueBackend) are NOT marked, so they
			// survive successful runs.
			if state.AutoSwitchedIdentifiers == nil {
				state.AutoSwitchedIdentifiers = make(map[string]struct{})
			}
			state.AutoSwitchedIdentifiers[issue.Identifier] = struct{}{}
			// Gap §6.2 — record switch timestamp so the periodic
			// revert check (RevertExpiredAutoSwitches) can drop the
			// override after cfg.Agent.SwitchRevertHours.
			if state.AutoSwitchedAt == nil {
				state.AutoSwitchedAt = make(map[string]time.Time)
			}
			state.AutoSwitchedAt[issue.Identifier] = now
			// Gap §5.3 — persist the override so a daemon crash mid-flight
			// doesn't lose the switch and re-dispatch under the original
			// (rate-limited) profile. Clones the maps before passing to
			// avoid sharing mutable state with the goroutine that writes.
			autoSwitchedCopy := maps.Clone(state.AutoSwitchedIdentifiers)
			profilesCopy := maps.Clone(state.IssueProfiles)
			backendsCopy := maps.Clone(state.IssueBackends)
			switchedAtCopy := maps.Clone(state.AutoSwitchedAt)
			go o.saveAutoSwitchedToDisk(autoSwitchedCopy, profilesCopy, backendsCopy, switchedAtCopy)
			// Gap §6.1 audit-trail: post a managed comment on the issue
			// summarising the swap so operators see "Itervox swapped
			// claude-coder → codex-coder due to rate-limit" without
			// having to read the daemon logs. Fire-and-forget — failure
			// to post must NOT block the dispatch.
			go o.commentRateLimitedSwitch(issue, failedProfile, failedBackend, rule, promptTokensTotal, completionTokensTotal)
		}
	}
	return queued
}

// allowRateLimitSwitch returns true when the issue is still under its
// rolling-window switch cap. Evicts entries older than the window before
// counting so the map cannot grow unboundedly. Reads cap + window via the
// cfgMu-guarded getters; HTTP handlers can mutate them at runtime.
func (o *Orchestrator) allowRateLimitSwitch(issueID string, now time.Time) bool {
	cap := o.MaxSwitchesPerIssuePerWindowCfg()
	if cap <= 0 {
		return true // 0 = unlimited (operator opt-out, not recommended)
	}
	windowH := o.SwitchWindowHoursCfg()
	if windowH <= 0 {
		windowH = 6
	}
	windowStart := now.Add(-time.Duration(windowH) * time.Hour)

	o.switchHistoryMu.Lock()
	defer o.switchHistoryMu.Unlock()
	if o.switchHistory == nil {
		o.switchHistory = make(map[string][]time.Time)
	}
	stamps := o.switchHistory[issueID]
	// Drop expired stamps in place; this also bounds memory growth across
	// long-running daemons.
	pruned := stamps[:0]
	for _, t := range stamps {
		if !t.Before(windowStart) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) == 0 {
		delete(o.switchHistory, issueID)
	} else {
		o.switchHistory[issueID] = pruned
	}
	return len(pruned) < cap
}

func (o *Orchestrator) recordRateLimitSwitch(issueID string, now time.Time) {
	o.switchHistoryMu.Lock()
	defer o.switchHistoryMu.Unlock()
	if o.switchHistory == nil {
		o.switchHistory = make(map[string][]time.Time)
	}
	o.switchHistory[issueID] = append(o.switchHistory[issueID], now)
}

func (o *Orchestrator) rateLimitSwitchWindowDuration() time.Duration {
	windowH := o.SwitchWindowHoursCfg()
	if windowH <= 0 {
		windowH = 6
	}
	return time.Duration(windowH) * time.Hour
}

func (o *Orchestrator) nextRateLimitCapCommentUntil(issueID string, now time.Time) time.Time {
	window := o.rateLimitSwitchWindowDuration()
	windowStart := now.Add(-window)

	o.switchHistoryMu.Lock()
	defer o.switchHistoryMu.Unlock()
	var oldest time.Time
	for _, t := range o.switchHistory[issueID] {
		if t.Before(windowStart) {
			continue
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return now.Add(window)
	}
	until := oldest.Add(window)
	if !until.After(now) {
		return now.Add(window)
	}
	return until
}

func (o *Orchestrator) claimRateLimitCapComment(issueID string, now time.Time) bool {
	if issueID == "" {
		return false
	}
	until := o.nextRateLimitCapCommentUntil(issueID, now)
	o.rateLimitCapCommentMu.Lock()
	defer o.rateLimitCapCommentMu.Unlock()
	if o.rateLimitCapCommentUntil == nil {
		o.rateLimitCapCommentUntil = make(map[string]time.Time)
	}
	if existing, ok := o.rateLimitCapCommentUntil[issueID]; ok && now.Before(existing) {
		return false
	}
	o.rateLimitCapCommentUntil[issueID] = until
	return true
}

func (o *Orchestrator) rateLimitCooldownUntil(key string) (time.Time, bool) {
	o.rateLimitCooldownMu.Lock()
	defer o.rateLimitCooldownMu.Unlock()
	if o.rateLimitCooldown == nil {
		return time.Time{}, false
	}
	t, ok := o.rateLimitCooldown[key]
	return t, ok
}

func (o *Orchestrator) setRateLimitCooldown(key string, until time.Time) {
	o.rateLimitCooldownMu.Lock()
	defer o.rateLimitCooldownMu.Unlock()
	if o.rateLimitCooldown == nil {
		o.rateLimitCooldown = make(map[string]time.Time)
	}
	o.rateLimitCooldown[key] = until
}

// commentRateLimitedSwitch posts a managed comment on the tracker issue
// explaining that the orchestrator just auto-swapped the issue's profile
// in response to a rate-limit failure. Fire-and-forget — caller invokes
// in a goroutine. Uses context.Background() intentionally: the audit
// trail must be delivered even during graceful shutdown so operators
// know what happened. Bounded by a 15s timeout. Gap §6.1.
func (o *Orchestrator) commentRateLimitedSwitch(
	issue domain.Issue,
	failedProfile, failedBackend string,
	rule RateLimitedAutomation,
	promptTokensTotal, completionTokensTotal int,
) {
	if o == nil || o.tracker == nil {
		return
	}
	fromProfile := fallbackProfileLabel(failedProfile)
	body := fmt.Sprintf(
		"🤖 Itervox: rate-limit auto-switch\n\n"+
			"Profile **%s** exhausted retries on backend `%s` (consumed %d input + %d output tokens). "+
			"Re-dispatching this issue under profile **%s**%s.",
		fromProfile, failedBackend, promptTokensTotal, completionTokensTotal,
		rule.SwitchToProfile, formatBackendOverride(rule.SwitchToBackend),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(body)); err != nil {
		slog.Warn("orchestrator: failed to post rate-limit switch comment",
			"issue_id", issue.ID, "automation", rule.ID, "error", err)
	}
}

// commentRateLimitCapExhausted posts a managed comment when the rolling
// switch cap denies a rate_limited rule from re-dispatching. Fire-and-forget
// goroutine pattern matches commentRateLimitedSwitch. Gap §6.5.
func (o *Orchestrator) commentRateLimitCapExhausted(issue domain.Issue, failedProfile string) {
	if o == nil || o.tracker == nil {
		return
	}
	cap := o.MaxSwitchesPerIssuePerWindowCfg()
	hours := o.SwitchWindowHoursCfg()
	if hours <= 0 {
		hours = 6
	}
	body := fmt.Sprintf(
		"🤖 Itervox: rate-limit auto-switch budget exhausted\n\n"+
			"Profile **%s** hit a vendor rate-limit, but this issue has already burned through "+
			"its **%d switches in the last %d hours** budget. No further auto-switches will fire "+
			"until the rolling window opens. Triage manually or raise the cap in /settings.",
		fallbackProfileLabel(failedProfile), cap, hours,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(body)); err != nil {
		slog.Warn("orchestrator: failed to post cap-exhausted comment",
			"issue_id", issue.ID, "error", err)
	}
}

// fallbackProfileLabel renders a placeholder when the failed profile is
// empty (issue ran under the default profile, no IssueProfiles entry).
// Gap §6.3 — without this, the comment + log entry render an empty string
// and the operator has no anchor for the issue's previous profile.
func fallbackProfileLabel(profile string) string {
	if profile == "" {
		return "(default)"
	}
	return profile
}

func formatBackendOverride(backend string) string {
	if backend == "" {
		return ""
	}
	return fmt.Sprintf(" (backend override: `%s`)", backend)
}

// RevertExpiredAutoSwitches drops auto-switch overrides whose age has
// exceeded cfg.Agent.SwitchRevertHours. Called from onTick (and only
// effective when the cfg is > 0). The next dispatch picks up the
// natural profile. Operator-set overrides (not in AutoSwitchedAt) are
// preserved. Returns the count of reverted entries for telemetry.
// Gap §6.2.
func RevertExpiredAutoSwitches(state *State, ttl time.Duration, now time.Time) int {
	if state == nil || ttl <= 0 || len(state.AutoSwitchedAt) == 0 {
		return 0
	}
	threshold := now.Add(-ttl)
	reverted := 0
	for id, switchedAt := range state.AutoSwitchedAt {
		if switchedAt.After(threshold) {
			continue
		}
		delete(state.IssueProfiles, id)
		delete(state.IssueBackends, id)
		delete(state.AutoSwitchedIdentifiers, id)
		delete(state.AutoSwitchedAt, id)
		reverted++
	}
	return reverted
}

func (o *Orchestrator) revertExpiredAutoSwitchesForTick(state *State, now time.Time) int {
	o.cfgMu.RLock()
	revertHours := o.cfg.Agent.SwitchRevertHours
	o.cfgMu.RUnlock()
	if revertHours <= 0 {
		return 0
	}
	ttl := time.Duration(revertHours) * time.Hour
	reverted := RevertExpiredAutoSwitches(state, ttl, now)
	if reverted == 0 {
		return 0
	}
	slog.Info("orchestrator: reverted expired auto-switch overrides",
		"count", reverted, "ttl_hours", revertHours)
	autoSwitchedCopy := maps.Clone(state.AutoSwitchedIdentifiers)
	profilesCopy := maps.Clone(state.IssueProfiles)
	backendsCopy := maps.Clone(state.IssueBackends)
	switchedAtCopy := maps.Clone(state.AutoSwitchedAt)
	o.saveAutoSwitchedToDisk(autoSwitchedCopy, profilesCopy, backendsCopy, switchedAtCopy)
	return reverted
}

// PruneRateLimitedMaps removes entries that can no longer affect any
// future cap or cooldown decision: switchHistory entries whose newest
// stamp is older than 2 * SwitchWindowHours, and rateLimitCooldown
// entries whose `until` is in the past. Without periodic pruning these
// maps grow with every issue + profile that ever fired the rule, even
// if the entries are functionally dead (gap §1.1, §1.2). Safe to call
// from any goroutine; idempotent.
func (o *Orchestrator) PruneRateLimitedMaps(now time.Time) {
	windowH := o.SwitchWindowHoursCfg()
	if windowH <= 0 {
		windowH = 6
	}
	staleAfter := now.Add(-2 * time.Duration(windowH) * time.Hour)

	o.switchHistoryMu.Lock()
	for issueID, stamps := range o.switchHistory {
		// Find the newest stamp; drop the whole entry if it's stale.
		var newest time.Time
		for _, t := range stamps {
			if t.After(newest) {
				newest = t
			}
		}
		if len(stamps) == 0 || newest.Before(staleAfter) {
			delete(o.switchHistory, issueID)
		}
	}
	o.switchHistoryMu.Unlock()

	o.rateLimitCooldownMu.Lock()
	for key, until := range o.rateLimitCooldown {
		if !until.After(now) {
			delete(o.rateLimitCooldown, key)
		}
	}
	o.rateLimitCooldownMu.Unlock()

	o.rateLimitCapCommentMu.Lock()
	for issueID, until := range o.rateLimitCapCommentUntil {
		if !until.After(now) {
			delete(o.rateLimitCapCommentUntil, issueID)
		}
	}
	o.rateLimitCapCommentMu.Unlock()
}
