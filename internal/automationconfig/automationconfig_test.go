package automationconfig_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/automationdef"
	"github.com/vnovick/itervox/internal/config"
)

func TestAutomationConfigRoundTripPreservesEveryDefinitionField(t *testing.T) {
	t.Parallel()

	original := []automationdef.Definition{{
		ID:           "all-fields",
		Enabled:      true,
		Profile:      "qa",
		Instructions: "check the issue",
		Trigger: automationdef.Trigger{
			Type:     config.AutomationTriggerIssueEnteredState,
			Cron:     "0 9 * * 1-5",
			Timezone: "UTC",
			State:    "Ready for QA",
		},
		Filter: automationdef.Filter{
			MatchMode:         config.AutomationFilterMatchAny,
			States:            []string{"Todo", "Ready for QA"},
			LabelsAny:         []string{"release", "qa"},
			IdentifierRegex:   "^ENG-",
			Limit:             12,
			InputContextRegex: "continue|branch",
			MaxAgeMinutes:     45,
		},
		Policy: automationdef.Policy{
			AutoResume:      true,
			SwitchToProfile: "fallback",
			SwitchToBackend: "codex",
			CooldownMinutes: 30,
		},
	}}

	cfgs := automationconfig.ConfigsFromDefinitions(original)
	require.Equal(t, original, automationconfig.DefinitionsFromConfigs(cfgs))

	cfgs[0].Filter.States[0] = "mutated"
	cfgs[0].Filter.LabelsAny[0] = "mutated"
	require.Equal(t, "Todo", original[0].Filter.States[0])
	require.Equal(t, "release", original[0].Filter.LabelsAny[0])
}
