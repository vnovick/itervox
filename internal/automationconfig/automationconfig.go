package automationconfig

import (
	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/config"
)

// ConfigsFromDefinitions converts serializable automation definitions into
// runtime config automation values.
func ConfigsFromDefinitions(defs []automationdef.Definition) []config.AutomationConfig {
	if len(defs) == 0 {
		return nil
	}
	automations := make([]config.AutomationConfig, 0, len(defs))
	for _, def := range defs {
		automations = append(automations, config.AutomationConfig{
			ID:           def.ID,
			Enabled:      def.Enabled,
			Profile:      def.Profile,
			Instructions: def.Instructions,
			Trigger: config.AutomationTriggerConfig{
				Type:     def.Trigger.Type,
				Cron:     def.Trigger.Cron,
				Timezone: def.Trigger.Timezone,
				State:    def.Trigger.State,
			},
			Filter: config.AutomationFilterConfig{
				MatchMode:         def.Filter.MatchMode,
				States:            append([]string{}, def.Filter.States...),
				LabelsAny:         append([]string{}, def.Filter.LabelsAny...),
				IdentifierRegex:   def.Filter.IdentifierRegex,
				Limit:             def.Filter.Limit,
				InputContextRegex: def.Filter.InputContextRegex,
				MaxAgeMinutes:     def.Filter.MaxAgeMinutes,
			},
			Policy: config.AutomationPolicyConfig{
				AutoResume:      def.Policy.AutoResume,
				SwitchToProfile: def.Policy.SwitchToProfile,
				SwitchToBackend: def.Policy.SwitchToBackend,
				CooldownMinutes: def.Policy.CooldownMinutes,
			},
		})
	}
	return automations
}

// DefinitionsFromConfigs converts runtime automation config values into the
// serializable definition shape used by HTTP and WORKFLOW.md patching.
func DefinitionsFromConfigs(automations []config.AutomationConfig) []automationdef.Definition {
	if len(automations) == 0 {
		return nil
	}
	defs := make([]automationdef.Definition, 0, len(automations))
	for _, automation := range automations {
		defs = append(defs, automationdef.Definition{
			ID:           automation.ID,
			Enabled:      automation.Enabled,
			Profile:      automation.Profile,
			Instructions: automation.Instructions,
			Trigger: automationdef.Trigger{
				Type:     automation.Trigger.Type,
				Cron:     automation.Trigger.Cron,
				Timezone: automation.Trigger.Timezone,
				State:    automation.Trigger.State,
			},
			Filter: automationdef.Filter{
				MatchMode:         automation.Filter.MatchMode,
				States:            append([]string{}, automation.Filter.States...),
				LabelsAny:         append([]string{}, automation.Filter.LabelsAny...),
				IdentifierRegex:   automation.Filter.IdentifierRegex,
				Limit:             automation.Filter.Limit,
				InputContextRegex: automation.Filter.InputContextRegex,
				MaxAgeMinutes:     automation.Filter.MaxAgeMinutes,
			},
			Policy: automationdef.Policy{
				AutoResume:      automation.Policy.AutoResume,
				SwitchToProfile: automation.Policy.SwitchToProfile,
				SwitchToBackend: automation.Policy.SwitchToBackend,
				CooldownMinutes: automation.Policy.CooldownMinutes,
			},
		})
	}
	return defs
}
