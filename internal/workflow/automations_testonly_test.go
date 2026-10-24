package workflow

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The automations block writer below was production code until #239 moved
// the automations list to the layered settings files (cmd/itervox's
// automationsSetting, written by config.SetSettings). Its tests are kept
// (the block rendering and lock coverage) through this test-only copy.

// PatchAutomationsBlock replaces (or inserts) the top-level automations block in
// the YAML front matter of the file at path. Passing nil or an empty slice
// removes the automations block entirely. Legacy schedules blocks are removed
// when writing automations so the file has a single source of truth.
func PatchAutomationsBlock(path string, automations []AutomationEntry) error {
	return ApplyAndWriteFrontMatter(path, MutateAutomationsBlock(automations))
}

// MutateAutomationsBlock returns a Mutator that replaces (or inserts) the
// top-level automations: block. See PatchAutomationsBlock.
func MutateAutomationsBlock(automations []AutomationEntry) Mutator {
	return func(frontLines []string) ([]string, error) {
		automationsStart := -1
		automationsEnd := -1
		legacySchedulesStart := -1
		legacySchedulesEnd := -1
		for i, line := range frontLines {
			if line != "automations:" && line != "schedules:" {
				continue
			}
			if line == "automations:" {
				automationsStart = i
			} else {
				legacySchedulesStart = i
			}
			j := i + 1
			for j < len(frontLines) {
				l := frontLines[j]
				if l == "" {
					j++
					continue
				}
				trimmed := strings.TrimLeft(l, " ")
				indent := len(l) - len(trimmed)
				if indent > 0 {
					j++
				} else {
					break
				}
			}
			if line == "automations:" {
				automationsEnd = j
			} else {
				legacySchedulesEnd = j
			}
		}

		var replacement []string
		if len(automations) > 0 {
			replacement = append(replacement, "automations:")
			for _, automation := range automations {
				replacement = append(replacement, "  - id: "+automation.ID)
				replacement = append(replacement, "    enabled: "+strconv.FormatBool(automation.Enabled))
				replacement = append(replacement, "    profile: "+automation.Profile)
				if automation.Instructions != "" {
					replacement = append(replacement, "    instructions: "+strconv.Quote(automation.Instructions))
				}
				replacement = append(replacement, "    trigger:")
				replacement = append(replacement, "      type: "+automation.Trigger.Type)
				if automation.Trigger.Cron != "" {
					replacement = append(replacement, "      cron: "+strconv.Quote(automation.Trigger.Cron))
				}
				if automation.Trigger.Timezone != "" {
					replacement = append(replacement, "      timezone: "+strconv.Quote(automation.Trigger.Timezone))
				}
				if automation.Trigger.State != "" {
					replacement = append(replacement, "      state: "+strconv.Quote(automation.Trigger.State))
				}
				filterLines := buildAutomationFilterLines(automation.Filter)
				if len(filterLines) > 0 {
					replacement = append(replacement, "    filter:")
					replacement = append(replacement, filterLines...)
				}
				policyLines := buildAutomationPolicyLines(automation.Trigger.Type, automation.Policy)
				if len(policyLines) > 0 {
					replacement = append(replacement, "    policy:")
					replacement = append(replacement, policyLines...)
				}
			}
		}

		var newFrontLines []string
		switch {
		case automationsStart >= 0:
			newFrontLines = append(newFrontLines, frontLines[:automationsStart]...)
			newFrontLines = append(newFrontLines, replacement...)
			newFrontLines = append(newFrontLines, frontLines[automationsEnd:]...)
		case legacySchedulesStart >= 0:
			newFrontLines = append(newFrontLines, frontLines[:legacySchedulesStart]...)
			newFrontLines = append(newFrontLines, replacement...)
			newFrontLines = append(newFrontLines, frontLines[legacySchedulesEnd:]...)
		case len(automations) > 0:
			newFrontLines = append(frontLines, replacement...)
		default:
			return frontLines, nil
		}
		return newFrontLines, nil
	}
}

func buildAutomationFilterLines(filter AutomationFilterEntry) []string {
	var lines []string
	if filter.MatchMode != "" && filter.MatchMode != "all" {
		lines = append(lines, "      match_mode: "+strconv.Quote(filter.MatchMode))
	}
	if len(filter.States) > 0 {
		lines = append(lines, "      states: "+marshalStringSliceInline(filter.States))
	}
	if len(filter.LabelsAny) > 0 {
		lines = append(lines, "      labels_any: "+marshalStringSliceInline(filter.LabelsAny))
	}
	if len(filter.Groups) > 0 {
		lines = append(lines, "      groups: "+filter.Groups.YAMLFlow()) // #198
	}
	if filter.IdentifierRegex != "" {
		lines = append(lines, "      identifier_regex: "+strconv.Quote(filter.IdentifierRegex))
	}
	if filter.Limit > 0 {
		lines = append(lines, "      limit: "+strconv.Itoa(filter.Limit))
	}
	if filter.InputContextRegex != "" {
		lines = append(lines, "      input_context_regex: "+strconv.Quote(filter.InputContextRegex))
	}
	if filter.MaxAgeMinutes > 0 {
		lines = append(lines, "      max_age_minutes: "+strconv.Itoa(filter.MaxAgeMinutes))
	}
	// #173 PR-event filters.
	for _, f := range []struct {
		key    string
		values []string
	}{
		{"check_names", filter.CheckNames},
		{"conclusions", filter.Conclusions},
		{"review_states", filter.ReviewStates},
		{"review_authors", filter.ReviewAuthors},
		{"mergeable_states", filter.MergeableStates},
	} {
		if len(f.values) > 0 {
			lines = append(lines, "      "+f.key+": "+marshalStringSliceInline(f.values))
		}
	}
	return lines
}

func buildAutomationPolicyLines(triggerType string, policy AutomationPolicyEntry) []string {
	var lines []string
	if policy.AutoResume {
		if triggerType == "rate_limited" {
			lines = append(lines, "      auto_switch: true")
		} else {
			lines = append(lines, "      auto_resume: true")
		}
	}
	if policy.SwitchToProfile != "" {
		lines = append(lines, "      switch_to_profile: "+strconv.Quote(policy.SwitchToProfile))
	}
	if policy.SwitchToBackend != "" {
		lines = append(lines, "      switch_to_backend: "+strconv.Quote(policy.SwitchToBackend))
	}
	if policy.CooldownMinutes > 0 {
		lines = append(lines, "      cooldown_minutes: "+strconv.Itoa(policy.CooldownMinutes))
	}
	return lines
}

func marshalStringSliceInline(values []string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(data)
}
