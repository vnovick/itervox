package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
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
// `cfg.Agent.RateLimitErrorPatterns` (gap §5.1) applies per
// `cfg.Agent.RateLimitErrorPatternsMode` (CORE-100): "replace" (default)
// uses ONLY the operator's list and drops these defaults; "extend" checks
// the operator's list AND these defaults. An empty (or all-blank) operator
// list always means these defaults, whatever the mode.
var defaultRateLimitErrorPatterns = []string{
	"rate_limit_exceeded",
	"rate limit",
	// CORE-030: no bare "429" or "quota" — they matched unrelated CLI
	// output ("pr-1429.json", "TestQuotaHandler", "disk quota exceeded").
	// Only anchored forms remain here; a standalone 429 token is accepted
	// on the agent-reported side only (standaloneHTTP429 below).
	"http 429",
	"status: 429",
	"(429)",
	"429 too many requests",
	"insufficient_quota",
	"quota exceeded for",
	"usage quota",
	// OpenAI's 429 body: "You exceeded your current quota, please check
	// your plan and billing details."
	"exceeded your current quota",
	"too many requests",
	// Anthropic vendor surface — phrasings used in 2026 by Claude Max
	// (extra-usage quota), Pro/Team tier limits, and Codex/Claude wrappers.
	// None of these substrings appears in generic 5xx, network, or compile
	// errors, so the conservative-by-default classification holds.
	"out of extra usage",
	"reached the limit for your current claude",
	"out of credits",
	"resets at",
	// CORE-009 — current Claude Code (code.claude.com/docs/en/errors) and
	// Codex (codex-rs/protocol/src/error.rs) usage-limit wording, checked
	// 2026-09-25. Matching runs after normaliseApostrophes, so "you've"
	// also covers upstream Codex's U+2019 spelling ("You’ve"). Codex's
	// workspace wording is "You hit your spend cap ..." (no apostrophe, no
	// "usage limit"), hence the separate "spend cap" entry.
	//
	// M0-close G12: the generic phrases "you've hit your" and "try again
	// at" are NOT bare substrings — both occur in unrelated failures
	// ("network timeout; try again at 15:00", "You've hit your breakpoint
	// ..."). "you've hit your" is a clause rule instead (see
	// defaultRateLimitClauseRules): it counts only when limit wording
	// follows it in the same clause. "try again at" is dropped: every
	// vendor message carrying it already names its limit ("usage limit"),
	// which matches on its own.
	"you hit your spend cap",
	"usage limit",
	"session limit",
	"weekly limit",
	"spend limit",
	"spend cap",
	"credit balance is too low",
	"credits required",
	"temporarily limiting requests",
}

// rateLimitClauseRule matches when lead is followed — within maxGap bytes
// and inside the same clause (no '.', ';', ':', '!', '?' or newline in
// between) — by one of qualifiers. It lets a generic vendor opener count
// only when limit wording completes it: "You've hit your Opus limit" and
// "You've hit your team's shared budget" match, "You've hit your
// breakpoint; output limited" does not.
type rateLimitClauseRule struct {
	lead       string
	qualifiers []string
	maxGap     int
}

// defaultRateLimitClauseRules extend defaultRateLimitErrorPatterns. They
// apply only when the default list is in use; operator-supplied
// RateLimitErrorPatterns stay plain substrings.
var defaultRateLimitClauseRules = []rateLimitClauseRule{
	// Claude Code (code.claude.com/docs/en/errors): "You've hit your
	// {session|weekly|Opus|Sonnet|monthly spend|...} limit", "... team's
	// shared budget"; Codex: "You’ve hit your usage limit".
	{lead: "you've hit your", qualifiers: []string{"limit", "budget", "spend cap"}, maxGap: 48},
}

// matches reports whether lower (already normalised) satisfies r. The
// clause is a byte range of lower, not a copy: a qualifier must lie wholly
// inside it, but its word boundaries are judged against lower itself, so the
// maxGap cut can never pass for a boundary (M0-close re-check 2 N10: cutting
// "... request_dispatcher/limiter.go" at 48 bytes left "limit" at the end of
// the slice, which the slice-based check accepted as a whole word).
func (r rateLimitClauseRule) matches(lower string) bool {
	for from := 0; ; {
		i := strings.Index(lower[from:], r.lead)
		if i < 0 {
			return false
		}
		lo := from + i + len(r.lead)
		hi := min(len(lower), lo+r.maxGap)
		if end := strings.IndexAny(lower[lo:hi], ".;:!?\n"); end >= 0 {
			hi = lo + end
		}
		for _, q := range r.qualifiers {
			if containsWordIn(lower, lo, hi, q) {
				return true
			}
		}
		from = lo
	}
}

// containsWordIn reports whether word occurs wholly inside s[lo:hi] as a
// whole word of s: not preceded by a letter or digit, and followed by a
// non-letter/digit or a plural "s" that is itself followed by one. Both
// boundaries are read from s, never from the s[lo:hi] slice. M0-close
// re-check G12: a bare substring match let "limit" complete the clause inside
// "limiter" ("You've hit your breakpoint at limiter.go:42").
func containsWordIn(s string, lo, hi int, word string) bool {
	for off := lo; off < hi; {
		i := strings.Index(s[off:hi], word)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(word)
		if end < len(s) && s[end] == 's' {
			end++
		}
		if (start == 0 || !isWordByte(s[start-1])) && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		off = start + 1
	}
	return false
}

// isWordByte reports whether b is an ASCII letter or digit. Non-ASCII bytes
// count as boundaries; the qualifiers are ASCII.
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// apostropheNormaliser folds typographic apostrophes to ASCII so a pattern
// written as "you've" matches vendor text spelled "You’ve" (U+2019) and
// vice versa. U+2018 is folded too because some renderers emit it.
var apostropheNormaliser = strings.NewReplacer("\u2019", "'", "\u2018", "'")

// normaliseForRateLimitMatch lowercases s and folds typographic
// apostrophes to ASCII. Applied to both the failure text and each pattern.
func normaliseForRateLimitMatch(s string) string {
	return strings.ToLower(apostropheNormaliser.Replace(s))
}

// IsRateLimitFailureWithPatternsMode applies the operator's patterns per
// mode (CORE-100): "replace" (and "") matches only the operator's list when
// it has a non-blank entry; "extend" matches the operator's list or the
// built-in defaults. With no non-blank operator pattern the defaults apply
// in either mode.
func IsRateLimitFailureWithPatternsMode(errorMessage string, patterns []string, mode string) bool {
	if errorMessage == "" {
		return false
	}
	lower := normaliseForRateLimitMatch(errorMessage)
	custom := false
	for _, p := range patterns {
		if p == "" {
			continue
		}
		custom = true
		if strings.Contains(lower, normaliseForRateLimitMatch(p)) {
			return true
		}
	}
	if custom && mode != config.RateLimitPatternsModeExtend {
		return false
	}
	for _, r := range defaultRateLimitClauseRules {
		if r.matches(lower) {
			return true
		}
	}
	for _, p := range defaultRateLimitErrorPatterns {
		if strings.Contains(lower, normaliseForRateLimitMatch(p)) {
			return true
		}
	}
	agentSide, _ := agent.SplitFailureText(lower)
	return standaloneHTTP429.MatchString(agentSide)
}

// isRateLimitFailureCfg classifies msg with the configured patterns and
// mode, copied together under cfgMu so the classifier sees a consistent
// pair (CORE-100). Both the retry-exhaustion site in the event loop and
// classifyWorkerFailure go through it.
func (o *Orchestrator) isRateLimitFailureCfg(msg string) bool {
	o.cfgMu.RLock()
	patterns := append([]string(nil), o.cfg.Agent.RateLimitErrorPatterns...)
	mode := o.cfg.Agent.RateLimitErrorPatternsMode
	o.cfgMu.RUnlock()
	return IsRateLimitFailureWithPatternsMode(msg, patterns, mode)
}

// standaloneHTTP429 matches 429 as a whole number ("API Error: 429 {"), not
// as digits inside a longer one ("pr-1429.json", "issue-4290"). Applied to
// the agent-reported field only (CORE-030). Accepted residual: an agent-side
// "exit code 429" still matches.
var standaloneHTTP429 = regexp.MustCompile(`(^|[^0-9])429([^0-9]|$)`)

// retriesExhaustedNote carries the max-retries-exhausted comment body into
// the first accepted rate_limited switch comment (CORE-103), so an accepted
// switch on retry exhaustion produces ONE managed comment instead of two.
// folded is set on the event loop when a switch comment takes the body; the
// caller posts the body on its own only when nothing folded it.
type retriesExhaustedNote struct {
	body   string
	folded bool
}

// dispatchMatchingRateLimitedAutomationsNote is
// dispatchMatchingRateLimitedAutomations with an optional exhaustion note to
// fold into the first accepted switch comment (nil: no note).
func (o *Orchestrator) dispatchMatchingRateLimitedAutomationsNote(
	ctx context.Context,
	state *State,
	issue domain.Issue,
	now time.Time,
	failedProfile string,
	failedBackend string,
	errorMessage string,
	attempt int,
	promptTokensTotal, completionTokensTotal int,
	exhausted *retriesExhaustedNote,
) int {
	rules := o.snapRateLimitedAutomations()
	if len(rules) == 0 {
		return 0
	}
	queued := 0
	// touched is set when this call changed persisted state (a switch, a
	// cooldown, a cap-comment claim, an override) so it is saved once.
	touched := false
	for _, rule := range rules {
		if !MatchesAutomationFilter(
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
		// CORE-032: the profile that just exhausted its retries IS this
		// rule's switch target (both backends are limited). Switching onto
		// it again would burn a cap slot and a whole retry round for
		// nothing — and loop without bound when cooldown and cap are both 0.
		// Checked before the cap and cooldown so a skip records neither.
		// Rules without AutoResume dispatch a helper on rule.ProfileName and
		// are unaffected.
		if rule.AutoResume && rule.SwitchToProfile != "" && failedProfile == rule.SwitchToProfile {
			slog.Info("orchestrator: rate_limited rule skipped, failed profile is already its switch target",
				"identifier", issue.Identifier, "automation", rule.ID,
				"failed_profile", failedProfile)
			continue
		}
		if !o.allowRateLimitSwitch(state, issue.ID, now) {
			slog.Warn("orchestrator: rate_limited switch cap reached, skipping",
				"identifier", issue.Identifier, "automation", rule.ID,
				"failed_profile", failedProfile)
			// Gap §6.5 — surface "you've exhausted the auto-switch budget
			// for this issue" to the operator via a tracker comment so
			// they don't have to grep daemon logs to know why a stuck
			// issue stopped auto-switching. Fire-and-forget.
			if o.claimRateLimitCapComment(state, issue.ID, now) {
				touched = true
				// Tracked on commentWg: this goroutine enqueues a DURABLE
				// outbox entry, so Run must not return while it is still
				// writing .itervox/outbox.json.
				goSafe(&o.commentWg, "rate-limit-cap-comment", issue.Identifier, func() {
					o.commentRateLimitCapExhausted(issue, failedProfile)
				}, o.withPanicFailure("rate-limit-cap-comment", issue.Identifier, nil))
			}
			continue
		}
		cooldownKey := issue.ID + "|" + failedProfile
		if untilT, muted := o.rateLimitCooldownUntil(*state, cooldownKey); muted && now.Before(untilT) {
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

		touched = true
		o.recordRateLimitSwitch(state, issue.ID, now)
		if rule.Cooldown > 0 {
			o.setRateLimitCooldown(state, cooldownKey, now.Add(rule.Cooldown))
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
			// CORE-055: provenance, captured at the switch and persisted.
			if state.AutoSwitchInfo == nil {
				state.AutoSwitchInfo = make(map[string]AutoSwitchRecord)
			}
			toBackend := rule.SwitchToBackend
			if toBackend == "" {
				toBackend = o.profileKeyBackend(rule.SwitchToProfile, o.cfg.Agent.Command)
			}
			state.AutoSwitchInfo[issue.Identifier] = AutoSwitchRecord{
				Source:      AutoSwitchSourceAutomation,
				FromBackend: failedBackend, FromProfile: failedProfile,
				ToBackend: toBackend, ToProfile: rule.SwitchToProfile,
				Reason:     fmt.Sprintf("rate_limited automation %q", rule.ID),
				FromKey:    BackendHealthKey(failedBackend, ""),
				SwitchedAt: now,
			}
			// Gap §5.3 — the override is persisted (with the switch
			// bookkeeping, CORE-052) once after the loop below, so a
			// daemon crash mid-flight doesn't lose the switch and
			// re-dispatch under the original (rate-limited) profile.
			// Gap §6.1 audit-trail: post a managed comment on the issue
			// summarising the swap so operators see "Itervox swapped
			// claude-coder → codex-coder due to rate-limit" without
			// having to read the daemon logs. Fire-and-forget — failure
			// to post must NOT block the dispatch.
			// Tracked on commentWg — see the cap-exhausted call site above.
			// CORE-103: on retry exhaustion the first accepted switch also
			// carries the exhaustion text, so the issue gets one comment.
			exhaustedBody := ""
			if exhausted != nil && !exhausted.folded {
				exhaustedBody, exhausted.folded = exhausted.body, true
			}
			goSafe(&o.commentWg, "rate-limit-switch-comment", issue.Identifier, func() {
				o.commentRateLimitedSwitch(issue, failedProfile, failedBackend, rule, promptTokensTotal, completionTokensTotal, exhaustedBody)
			}, o.withPanicFailure("rate-limit-switch-comment", issue.Identifier, nil))
		}
	}
	if touched {
		// Local file write through the ordered ledger writer; synchronous
		// submission from the event loop (see saveAutoSwitchedToDisk).
		o.saveAutoSwitchedToDisk(state)
	}
	return queued
}

// allowRateLimitSwitch returns true when the issue is still under its
// rolling-window switch cap. Evicts stamps older than the window before
// counting so the map cannot grow unboundedly. Reads cap + window via the
// cfgMu-guarded getters; HTTP handlers can mutate them at runtime. The
// history is event-loop State (CORE-052): callers pass the loop's *State.
func (o *Orchestrator) allowRateLimitSwitch(state *State, issueID string, now time.Time) bool {
	cap := o.MaxSwitchesPerIssuePerWindowCfg()
	if cap <= 0 {
		return true // 0 = unlimited (operator opt-out, not recommended)
	}
	windowStart := now.Add(-o.rateLimitSwitchWindowDuration())
	if state.SwitchHistory == nil {
		state.SwitchHistory = make(map[string][]time.Time)
	}
	stamps := state.SwitchHistory[issueID]
	// Drop expired stamps into a fresh slice (never in place: a published
	// snapshot may still share nothing, but a fresh slice keeps the rule
	// simple); this also bounds memory growth across long-running daemons.
	var pruned []time.Time
	for _, t := range stamps {
		if !t.Before(windowStart) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) == 0 {
		delete(state.SwitchHistory, issueID)
	} else {
		state.SwitchHistory[issueID] = pruned
	}
	return len(pruned) < cap
}

func (o *Orchestrator) recordRateLimitSwitch(state *State, issueID string, now time.Time) {
	if state.SwitchHistory == nil {
		state.SwitchHistory = make(map[string][]time.Time)
	}
	state.SwitchHistory[issueID] = append(state.SwitchHistory[issueID], now)
}

func (o *Orchestrator) rateLimitSwitchWindowDuration() time.Duration {
	windowH := o.SwitchWindowHoursCfg()
	if windowH <= 0 {
		windowH = 6
	}
	return time.Duration(windowH) * time.Hour
}

func (o *Orchestrator) nextRateLimitCapCommentUntil(state State, issueID string, now time.Time) time.Time {
	window := o.rateLimitSwitchWindowDuration()
	windowStart := now.Add(-window)

	var oldest time.Time
	for _, t := range state.SwitchHistory[issueID] {
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

func (o *Orchestrator) claimRateLimitCapComment(state *State, issueID string, now time.Time) bool {
	if issueID == "" {
		return false
	}
	until := o.nextRateLimitCapCommentUntil(*state, issueID, now)
	if state.RateLimitCapCommentUntil == nil {
		state.RateLimitCapCommentUntil = make(map[string]time.Time)
	}
	if existing, ok := state.RateLimitCapCommentUntil[issueID]; ok && now.Before(existing) {
		return false
	}
	state.RateLimitCapCommentUntil[issueID] = until
	return true
}

func (o *Orchestrator) rateLimitCooldownUntil(state State, key string) (time.Time, bool) {
	t, ok := state.RateLimitCooldowns[key]
	return t, ok
}

func (o *Orchestrator) setRateLimitCooldown(state *State, key string, until time.Time) {
	if state.RateLimitCooldowns == nil {
		state.RateLimitCooldowns = make(map[string]time.Time)
	}
	state.RateLimitCooldowns[key] = until
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
	exhaustedBody string,
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
	if exhaustedBody != "" {
		body += "\n\n" + exhaustedBody
	}
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
		clearAutoSwitch(state, id)
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
	o.saveAutoSwitchedToDisk(state)
	return reverted
}

// pruneRateLimitedMaps removes entries that can no longer affect any
// future cap or cooldown decision: SwitchHistory entries whose newest stamp
// is older than 2 * SwitchWindowHours, and cooldown / cap-comment entries
// whose `until` is in the past. Without periodic pruning these maps grow
// with every issue + profile that ever fired the rule (gap §1.1, §1.2).
// Event loop only (onTick); returns how many entries were dropped so the
// caller persists only when something changed (CORE-052).
func (o *Orchestrator) pruneRateLimitedMaps(state *State, now time.Time) int {
	staleAfter := now.Add(-2 * o.rateLimitSwitchWindowDuration())
	dropped := 0
	for issueID, stamps := range state.SwitchHistory {
		// Find the newest stamp; drop the whole entry if it's stale.
		var newest time.Time
		for _, t := range stamps {
			if t.After(newest) {
				newest = t
			}
		}
		if len(stamps) == 0 || newest.Before(staleAfter) {
			delete(state.SwitchHistory, issueID)
			dropped++
		}
	}
	for key, until := range state.RateLimitCooldowns {
		if !until.After(now) {
			delete(state.RateLimitCooldowns, key)
			dropped++
		}
	}
	for issueID, until := range state.RateLimitCapCommentUntil {
		if !until.After(now) {
			delete(state.RateLimitCapCommentUntil, issueID)
			dropped++
		}
	}
	return dropped
}

// rateLimitVendorDelayCap bounds how long a vendor limit hint may delay a
// retry (CORE-051). A reset time parsed from text in the wrong zone, or a
// weekly limit days away, must not park an issue for days: after the cap
// the retry runs, fails fast on the limit, and is rescheduled again.
const rateLimitVendorDelayCap = 6 * time.Hour

// vendorRetryDelayMs is the retry delay a limit signal asks for, in ms,
// capped at rateLimitVendorDelayCap; 0 when the signal carries none.
func vendorRetryDelayMs(limit *agent.LimitSignal, now time.Time) int {
	d := min(limit.VendorDelay(now), rateLimitVendorDelayCap)
	return int(d / time.Millisecond)
}

// limitTypeOf renders a signal's vendor window for logs ("" when unknown).
func limitTypeOf(limit *agent.LimitSignal) string {
	if limit == nil {
		return ""
	}
	return limit.LimitType
}

// dispatchRateLimitedFallback evaluates the rate_limited rules for a
// limited run, identifying the failed profile/backend and token totals from
// the live RunEntry. Shared by the first-failure path (TerminalRateLimited,
// CORE-051) and the retry-exhaustion path, so both go through the same
// switch cap, cooldown, cap-comment dedupe and self-switch guard.
func (o *Orchestrator) dispatchRateLimitedFallback(
	ctx context.Context, state *State, issue domain.Issue, liveEntry *RunEntry,
	now time.Time, errMsg string, attempt int, exhausted *retriesExhaustedNote,
) int {
	failedProfile, failedBackend := "", ""
	inputTokens, outputTokens := 0, 0
	if liveEntry != nil {
		failedProfile = liveEntry.ProfileName
		failedBackend = liveEntry.Backend
		inputTokens = liveEntry.InputTokens
		outputTokens = liveEntry.OutputTokens
	}
	if failedProfile == "" {
		failedProfile = o.issueProfileForDispatch(*state, issue.Identifier)
	}
	return o.dispatchMatchingRateLimitedAutomationsNote(
		ctx, state, issue, now,
		failedProfile, failedBackend, errMsg, attempt,
		inputTokens, outputTokens, exhausted,
	)
}
