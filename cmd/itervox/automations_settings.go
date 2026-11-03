package main

import (
	"encoding/json"

	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/config"
)

// automationsSetting is the automations list as a settings file holds it
// (#239: automations is a layered section). The keys are the ones the
// front-matter automations block used, written under the same rules: a
// default (match_mode all, zero numbers, empty lists) is left out, and the
// rate_limited trigger's auto-resume is spelled auto_switch.
func automationsSetting(defs []automationdef.Definition) []any {
	out := make([]any, 0, len(defs))
	for _, d := range defs {
		a := map[string]any{"id": d.ID, "enabled": d.Enabled, "profile": d.Profile}
		if d.Instructions != "" {
			a["instructions"] = d.Instructions
		}
		trigger := map[string]any{"type": d.Trigger.Type}
		putString(trigger, "cron", d.Trigger.Cron)
		putString(trigger, "timezone", d.Trigger.Timezone)
		putString(trigger, "state", d.Trigger.State)
		a["trigger"] = trigger
		if f := automationFilterSetting(d.Filter); len(f) > 0 {
			a["filter"] = f
		}
		if p := automationPolicySetting(d.Trigger.Type, d.Policy); len(p) > 0 {
			a["policy"] = p
		}
		out = append(out, a)
	}
	return out
}

func automationFilterSetting(f automationdef.Filter) map[string]any {
	out := map[string]any{}
	if f.MatchMode != "" && f.MatchMode != "all" {
		out["match_mode"] = f.MatchMode
	}
	putList(out, "states", f.States)
	putList(out, "labels_any", f.LabelsAny)
	if len(f.Groups) > 0 {
		// #198: the tracker-filter group syntax, in the shape its parser reads.
		if b, err := json.Marshal(f.Groups); err == nil {
			if v, err := config.ParseJSONC(b); err == nil {
				out["groups"] = v
			}
		}
	}
	putString(out, "identifier_regex", f.IdentifierRegex)
	putInt(out, "limit", f.Limit)
	putString(out, "input_context_regex", f.InputContextRegex)
	putInt(out, "max_age_minutes", f.MaxAgeMinutes)
	// #173 PR-event filters.
	putList(out, "check_names", f.CheckNames)
	putList(out, "conclusions", f.Conclusions)
	putList(out, "review_states", f.ReviewStates)
	putList(out, "review_authors", f.ReviewAuthors)
	putList(out, "mergeable_states", f.MergeableStates)
	return out
}

func automationPolicySetting(triggerType string, p automationdef.Policy) map[string]any {
	out := map[string]any{}
	if p.AutoResume {
		if triggerType == "rate_limited" {
			out["auto_switch"] = true
		} else {
			out["auto_resume"] = true
		}
	}
	putString(out, "switch_to_profile", p.SwitchToProfile)
	putString(out, "switch_to_backend", p.SwitchToBackend)
	putInt(out, "cooldown_minutes", p.CooldownMinutes)
	putString(out, "move_to_state", p.MoveToState)
	return out
}

func putString(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func putInt(m map[string]any, k string, v int) {
	if v > 0 {
		m[k] = v
	}
}

func putList(m map[string]any, k string, v []string) {
	if len(v) > 0 {
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		m[k] = list
	}
}
