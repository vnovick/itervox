package config

import (
	"strings"

	"github.com/vnovick/itervox/internal/issuefilter"
)

const (
	AutomationTriggerCron              = "cron"
	AutomationTriggerInputRequired     = "input_required"
	AutomationTriggerTrackerComment    = "tracker_comment_added"
	AutomationTriggerIssueEnteredState = "issue_entered_state"
	AutomationTriggerIssueMovedBacklog = "issue_moved_to_backlog"
	AutomationTriggerRunFailed         = "run_failed"
	// AutomationTriggerPROpened (gap B) fires the moment a worker's
	// post-run logic detects a new PR for the issue (the same code path
	// that emits the `worker: pr_opened url=...` log line). The dispatch
	// happens in-process from worker.go so no external webhook ingestion
	// is required.
	AutomationTriggerPROpened = "pr_opened"
	// AutomationTriggerPRMerged (P1) fires when a tracked PR opened by an
	// itervox-managed branch transitions to MERGED. Mirrors PR-opened but on
	// the merge side. Trigger context carries pr_url, pr_number, merged_sha,
	// base_ref, merged_at.
	AutomationTriggerPRMerged = "pr_merged"
	// AutomationTriggerRateLimited (gap E) is a specialisation of
	// run_failed that fires only when the orchestrator's terminal-failure
	// classifier tags the exit as rate-limit-driven (vendor 429 / quota
	// exhaustion). The policy carries SwitchToProfile / SwitchToBackend so
	// auto_resume runs can re-dispatch the issue under a different agent
	// instead of giving up.
	AutomationTriggerRateLimited      = "rate_limited"
	AutomationTriggerBlockersResolved = "blockers_resolved"
	// The PR-event family (#173) fires on GitHub activity on an
	// itervox-managed pull request: one an itervox run recorded through the
	// worker's PR detection. Sources are GitHub webhooks (pr_events.
	// webhook_secret) or a poll of the open managed PRs. Trigger context
	// carries pr_url, pr_number, pr_branch and head_sha plus the per-type
	// fields below.
	//
	// AutomationTriggerPRCheckCompleted: a check run or commit status on the
	// PR's head SHA completed (check_name, check_conclusion).
	AutomationTriggerPRCheckCompleted = "pr_check_completed"
	// AutomationTriggerPRSynchronized: the PR's head SHA moved (a new push).
	AutomationTriggerPRSynchronized = "pr_synchronized"
	// AutomationTriggerPRReviewSubmitted: a review was submitted on the PR
	// (review_author, review_state).
	AutomationTriggerPRReviewSubmitted = "pr_review_submitted"
	// AutomationTriggerPRMergeableChanged: the PR's mergeable_state changed
	// (by default only into or out of "dirty", i.e. a merge conflict).
	AutomationTriggerPRMergeableChanged = "pr_mergeable_changed"
	AutomationFilterMatchAll            = "all"
	AutomationFilterMatchAny            = "any"
)

// IsPREventTrigger reports whether triggerType is one of the #173 PR-event
// triggers (pr_check_completed, pr_synchronized, pr_review_submitted,
// pr_mergeable_changed).
func IsPREventTrigger(triggerType string) bool {
	switch triggerType {
	case AutomationTriggerPRCheckCompleted, AutomationTriggerPRSynchronized,
		AutomationTriggerPRReviewSubmitted, AutomationTriggerPRMergeableChanged:
		return true
	}
	return false
}

// PRCheckConclusions are the values filter.conclusions accepts: GitHub check
// run conclusions plus the completed commit-status states (success, failure,
// error).
var PRCheckConclusions = []string{"success", "failure", "neutral", "cancelled", "skipped", "timed_out", "action_required", "stale", "error"}

// PRReviewStates are the values filter.review_states accepts.
var PRReviewStates = []string{"approved", "changes_requested", "commented", "dismissed"}

// PRMergeableStates are the values filter.mergeable_states accepts (GitHub's
// mergeable_state, without "unknown", which means "not computed yet").
var PRMergeableStates = []string{"clean", "dirty", "blocked", "behind", "unstable", "has_hooks", "draft"}

type AutomationTriggerConfig struct {
	Type     string
	Cron     string
	Timezone string
	State    string
}

type AutomationFilterConfig struct {
	MatchMode string
	States    []string
	LabelsAny []string
	// Groups is the tracker-filter group syntax (#198): an issue passes
	// when any group matches (conditions ANDed inside a group). It is one
	// more check beside states / labels_any / identifier_regex, combined by
	// match_mode. Written as `filter: [ {...}, ... ]` (groups only) or
	// `filter: {groups: [...], ...}`.
	Groups issuefilter.Filter
	// GroupsError is why the groups did not parse; validation reports it.
	GroupsError       string
	IdentifierRegex   string
	Limit             int
	InputContextRegex string
	// MaxAgeMinutes, when > 0, restricts input_required automations to entries
	// queued less than the given number of minutes ago. Stale entries are
	// skipped (and surfaced as `stale: true` on the dashboard) so a third bot
	// retry doesn't keep hammering an issue that's been blocked for days.
	// 0 means "no age limit" — preserves pre-feature behaviour.
	MaxAgeMinutes int
	// BodyContains restricts tracker_comment_added (and any future body-bearing
	// trigger) to comments whose body contains at least one of the listed
	// substrings (case-insensitive). Empty list = match all bodies.
	BodyContains []string
	// BodyRegex restricts the same triggers to comments whose body matches the
	// given regular expression. Empty = no regex check. BodyContains and
	// BodyRegex are AND-combined when both are set.
	BodyRegex string
	// CheckNames restricts pr_check_completed to checks whose name (check
	// run name or commit status context) equals one of the entries,
	// case-insensitively. Empty = every check.
	CheckNames []string
	// Conclusions restricts pr_check_completed to these conclusions
	// (PRCheckConclusions, lower case). Empty = every conclusion.
	Conclusions []string
	// ReviewStates restricts pr_review_submitted to these review states
	// (PRReviewStates, lower case). Empty = every state.
	ReviewStates []string
	// ReviewAuthors restricts pr_review_submitted to reviews by these GitHub
	// logins, case-insensitively (e.g. "greptile-apps[bot]"). Empty = every
	// author.
	ReviewAuthors []string
	// MergeableStates restricts pr_mergeable_changed to transitions into
	// these states (PRMergeableStates). Empty = transitions into or out of
	// "dirty" (a merge conflict appearing or clearing).
	MergeableStates []string
}

type AutomationPolicyConfig struct {
	// AutoResume has dual meaning depending on trigger type:
	//   - input_required: opt the helper into the "may call provide-input"
	//     contract so it can resume the blocked worker.
	//   - rate_limited: opt into the "auto-switch profile/backend without a
	//     human in the loop" behaviour (gap E).
	// In WORKFLOW.md, `auto_switch: true` is accepted as a clearer alias for
	// `auto_resume: true` on rate_limited triggers (gap §5.2). Both parse
	// into this single Go field.
	AutoResume bool
	// SwitchToProfile is the profile name a `rate_limited` automation
	// re-dispatches the issue under after the original profile exhausted
	// retries via vendor rate-limits. Required for `rate_limited`
	// automations; ignored on other trigger types.
	SwitchToProfile string
	// SwitchToBackend, when non-empty, overrides the new profile's backend
	// for the resumed run ("claude" or "codex"). Power-user knob; the
	// recommended default is empty (let the swapped profile pick its own
	// backend). Ignored on non-`rate_limited` triggers.
	SwitchToBackend string
	// CooldownMinutes mutes a `rate_limited` rule for the given (issue,
	// profile) tuple after a fire, preventing thrash when both backends
	// are throttled simultaneously. Default 30 when unset.
	CooldownMinutes int
	// MoveToState allows blockers_resolved automations to opt into tracker
	// state mutation when the selected profile is allowed to move issues.
	MoveToState string
}

type AutomationConfig struct {
	ID           string
	Enabled      bool
	Profile      string
	Instructions string
	Trigger      AutomationTriggerConfig
	Filter       AutomationFilterConfig
	Policy       AutomationPolicyConfig
}

type ScheduleFilterConfig struct {
	States          []string
	LabelsAny       []string
	IdentifierRegex string
	Limit           int
}

type ScheduleConfig struct {
	ID       string
	Enabled  bool
	Cron     string
	Timezone string
	Profile  string
	Filter   ScheduleFilterConfig
}

func parseAutomations(raw any) []AutomationConfig {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	automations := make([]AutomationConfig, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strField(m, "id", "")
		profile := strField(m, "profile", "")
		trigger := nestedMap(m, "trigger")
		triggerType := strField(trigger, "type", "")
		if id == "" || profile == "" || triggerType == "" {
			continue
		}
		filter := nestedMap(m, "filter")
		// #198: a list is the group syntax alone; a map may carry groups.
		rawGroups := filter["groups"]
		if list, ok := m["filter"].([]any); ok {
			rawGroups = list
		}
		groups, groupsErr := issuefilter.Parse(rawGroups, "filter.groups")
		groupsError := ""
		if groupsErr != nil {
			groupsError = groupsErr.Error()
		}
		policy := nestedMap(m, "policy")
		limit := intField(filter, "limit", 0)
		if limit < 0 {
			limit = 0
		}
		maxAge := intField(filter, "max_age_minutes", 0)
		if maxAge < 0 {
			maxAge = 0
		}
		states := strSliceField(filter, "states", nil)
		if len(states) == 0 {
			states = strSliceField(filter, "states_any", nil)
		}
		automations = append(automations, AutomationConfig{
			ID:           id,
			Enabled:      boolField(m, "enabled", true),
			Profile:      profile,
			Instructions: strField(m, "instructions", ""),
			Trigger: AutomationTriggerConfig{
				Type:     triggerType,
				Cron:     strField(trigger, "cron", ""),
				Timezone: strField(trigger, "timezone", ""),
				State:    strField(trigger, "state", ""),
			},
			Filter: AutomationFilterConfig{
				MatchMode:         normalizeAutomationMatchMode(strField(filter, "match_mode", "")),
				States:            states,
				LabelsAny:         strSliceField(filter, "labels_any", nil),
				Groups:            groups,
				GroupsError:       groupsError,
				IdentifierRegex:   strField(filter, "identifier_regex", ""),
				Limit:             limit,
				InputContextRegex: strField(filter, "input_context_regex", ""),
				MaxAgeMinutes:     maxAge,
				BodyContains:      strSliceField(filter, "body_contains", nil),
				BodyRegex:         strField(filter, "body_regex", ""),
				CheckNames:        strSliceField(filter, "check_names", nil),
				Conclusions:       lowerAll(strSliceField(filter, "conclusions", nil)),
				ReviewStates:      lowerAll(strSliceField(filter, "review_states", nil)),
				ReviewAuthors:     strSliceField(filter, "review_authors", nil),
				MergeableStates:   lowerAll(strSliceField(filter, "mergeable_states", nil)),
			},
			Policy: AutomationPolicyConfig{
				// Gap §5.2 — `auto_switch` is the preferred YAML key for
				// rate_limited triggers; falls back to `auto_resume` for
				// backwards compatibility. Either being true sets
				// AutoResume=true; only one need be present in the file.
				AutoResume:      boolField(policy, "auto_resume", false) || boolField(policy, "auto_switch", false),
				SwitchToProfile: strField(policy, "switch_to_profile", ""),
				SwitchToBackend: strField(policy, "switch_to_backend", ""),
				CooldownMinutes: intField(policy, "cooldown_minutes", 0),
				MoveToState:     strField(policy, "move_to_state", ""),
			},
		})
	}
	if len(automations) == 0 {
		return nil
	}
	return automations
}

// lowerAll lower-cases and trims every entry (nil stays nil).
func lowerAll(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strings.ToLower(strings.TrimSpace(v)))
	}
	return out
}

func normalizeAutomationMatchMode(value string) string {
	switch value {
	case AutomationFilterMatchAny:
		return AutomationFilterMatchAny
	case "", AutomationFilterMatchAll:
		return AutomationFilterMatchAll
	default:
		return AutomationFilterMatchAll
	}
}

func parseSchedules(raw any) []ScheduleConfig {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	schedules := make([]ScheduleConfig, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strField(m, "id", "")
		cronExpr := strField(m, "cron", "")
		profile := strField(m, "profile", "")
		if id == "" || cronExpr == "" || profile == "" {
			continue
		}
		filter := nestedMap(m, "filter")
		limit := intField(filter, "limit", 0)
		if limit < 0 {
			limit = 0
		}
		schedules = append(schedules, ScheduleConfig{
			ID:       id,
			Enabled:  boolField(m, "enabled", true),
			Cron:     cronExpr,
			Timezone: strField(m, "timezone", ""),
			Profile:  profile,
			Filter: ScheduleFilterConfig{
				States:          strSliceField(filter, "states", nil),
				LabelsAny:       strSliceField(filter, "labels_any", nil),
				IdentifierRegex: strField(filter, "identifier_regex", ""),
				Limit:           limit,
			},
		})
	}
	if len(schedules) == 0 {
		return nil
	}
	return schedules
}

func legacySchedulesToAutomations(schedules []ScheduleConfig) []AutomationConfig {
	if len(schedules) == 0 {
		return nil
	}
	automations := make([]AutomationConfig, 0, len(schedules))
	for _, schedule := range schedules {
		automations = append(automations, AutomationConfig{
			ID:      schedule.ID,
			Enabled: schedule.Enabled,
			Profile: schedule.Profile,
			Trigger: AutomationTriggerConfig{
				Type:     AutomationTriggerCron,
				Cron:     schedule.Cron,
				Timezone: schedule.Timezone,
			},
			Filter: AutomationFilterConfig{
				States:          schedule.Filter.States,
				LabelsAny:       schedule.Filter.LabelsAny,
				IdentifierRegex: schedule.Filter.IdentifierRegex,
				Limit:           schedule.Filter.Limit,
			},
		})
	}
	return automations
}
