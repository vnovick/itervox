package config

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
	// AutomationTriggerRateLimited (gap E) is a specialisation of
	// run_failed that fires only when the orchestrator's terminal-failure
	// classifier tags the exit as rate-limit-driven (vendor 429 / quota
	// exhaustion). The policy carries SwitchToProfile / SwitchToBackend so
	// auto_resume runs can re-dispatch the issue under a different agent
	// instead of giving up.
	AutomationTriggerRateLimited      = "rate_limited"
	AutomationTriggerBlockersResolved = "blockers_resolved"
	AutomationFilterMatchAll          = "all"
	AutomationFilterMatchAny          = "any"
)

type AutomationTriggerConfig struct {
	Type     string
	Cron     string
	Timezone string
	State    string
}

type AutomationFilterConfig struct {
	MatchMode         string
	States            []string
	LabelsAny         []string
	IdentifierRegex   string
	Limit             int
	InputContextRegex string
	// MaxAgeMinutes, when > 0, restricts input_required automations to entries
	// queued less than the given number of minutes ago. Stale entries are
	// skipped (and surfaced as `stale: true` on the dashboard) so a third bot
	// retry doesn't keep hammering an issue that's been blocked for days.
	// 0 means "no age limit" — preserves pre-feature behaviour.
	MaxAgeMinutes int
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
				IdentifierRegex:   strField(filter, "identifier_regex", ""),
				Limit:             limit,
				InputContextRegex: strField(filter, "input_context_regex", ""),
				MaxAgeMinutes:     maxAge,
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
