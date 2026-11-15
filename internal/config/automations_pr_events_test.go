package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestPREventsConfigParses (#173): the PR-event triggers and their filters
// parse from WORKFLOW.md (enumerated values lower-cased), and
// pr_events.webhook_secret resolves from the environment; both default off.
func TestPREventsConfigParses(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	assert.Empty(t, cfg.PREvents.WebhookSecret)

	t.Setenv("ITERVOX_TEST_PR_SECRET", "pr-secret-0123456789")
	cfg, err = config.Load(workflowWithContent(t, minimal(`pr_events:
  webhook_secret: $ITERVOX_TEST_PR_SECRET
automations:
  - id: fix-ci
    profile: fixer
    trigger:
      type: pr_check_completed
    filter:
      check_names: [CI, Chromatic]
      conclusions: [Failure, timed_out]
  - id: greptile
    profile: fixer
    trigger:
      type: pr_review_submitted
    filter:
      review_authors: ["greptile-apps[bot]"]
      review_states: [COMMENTED, changes_requested]
  - id: conflict
    profile: fixer
    trigger:
      type: pr_mergeable_changed
    filter:
      mergeable_states: [dirty]
  - id: on-push
    profile: fixer
    trigger:
      type: pr_synchronized
`)))
	require.NoError(t, err)
	assert.Equal(t, "pr-secret-0123456789", cfg.PREvents.WebhookSecret)
	require.Len(t, cfg.Automations, 4)
	assert.Equal(t, config.AutomationTriggerPRCheckCompleted, cfg.Automations[0].Trigger.Type)
	assert.Equal(t, []string{"CI", "Chromatic"}, cfg.Automations[0].Filter.CheckNames)
	assert.Equal(t, []string{"failure", "timed_out"}, cfg.Automations[0].Filter.Conclusions)
	assert.Equal(t, []string{"greptile-apps[bot]"}, cfg.Automations[1].Filter.ReviewAuthors)
	assert.Equal(t, []string{"commented", "changes_requested"}, cfg.Automations[1].Filter.ReviewStates)
	assert.Equal(t, []string{"dirty"}, cfg.Automations[2].Filter.MergeableStates)
	assert.Equal(t, config.AutomationTriggerPRSynchronized, cfg.Automations[3].Trigger.Type)

	profiles := map[string]config.AgentProfile{"fixer": {}}
	require.NoError(t, config.ValidateAutomations(cfg.Automations, profiles))

	_, err = config.Load(workflowWithContent(t, minimal("pr_events:\n  webhook_secret: short\n")))
	assert.ErrorContains(t, err, "pr_events.webhook_secret is shorter than")
}

// TestValidatePREventFilters (#173): each PR-event filter only on its own
// trigger, and enumerated filters only with values GitHub reports.
func TestValidatePREventFilters(t *testing.T) {
	profiles := map[string]config.AgentProfile{"fixer": {}}
	rule := func(trigger string, f config.AutomationFilterConfig) []config.AutomationConfig {
		return []config.AutomationConfig{{ID: "a", Enabled: true, Profile: "fixer",
			Trigger: config.AutomationTriggerConfig{Type: trigger}, Filter: f}}
	}
	for _, tc := range []struct {
		trigger string
		filter  config.AutomationFilterConfig
		wantErr string
	}{
		{config.AutomationTriggerPRCheckCompleted, config.AutomationFilterConfig{CheckNames: []string{"ci"}, Conclusions: []string{"failure"}}, ""},
		{config.AutomationTriggerPRReviewSubmitted, config.AutomationFilterConfig{ReviewStates: []string{"approved"}}, ""},
		{config.AutomationTriggerPRMergeableChanged, config.AutomationFilterConfig{MergeableStates: []string{"behind"}}, ""},
		{config.AutomationTriggerPRSynchronized, config.AutomationFilterConfig{}, ""},
		{config.AutomationTriggerPRSynchronized, config.AutomationFilterConfig{CheckNames: []string{"ci"}}, "filter.check_names is only meaningful on pr_check_completed"},
		{config.AutomationTriggerPROpened, config.AutomationFilterConfig{Conclusions: []string{"failure"}}, "filter.conclusions is only meaningful on pr_check_completed"},
		{config.AutomationTriggerPRCheckCompleted, config.AutomationFilterConfig{ReviewAuthors: []string{"x"}}, "filter.review_authors is only meaningful on pr_review_submitted"},
		{config.AutomationTriggerRunFailed, config.AutomationFilterConfig{MergeableStates: []string{"dirty"}}, "filter.mergeable_states is only meaningful on pr_mergeable_changed"},
		{config.AutomationTriggerPRCheckCompleted, config.AutomationFilterConfig{Conclusions: []string{"red"}}, `filter.conclusions value "red" is not one of`},
		{config.AutomationTriggerPRReviewSubmitted, config.AutomationFilterConfig{ReviewStates: []string{"pending"}}, `filter.review_states value "pending"`},
		{config.AutomationTriggerPRMergeableChanged, config.AutomationFilterConfig{MergeableStates: []string{"unknown"}}, `filter.mergeable_states value "unknown"`},
	} {
		err := config.ValidateAutomations(rule(tc.trigger, tc.filter), profiles)
		if tc.wantErr == "" {
			assert.NoError(t, err, "%s %+v", tc.trigger, tc.filter)
			continue
		}
		assert.ErrorContains(t, err, tc.wantErr, "%s %+v", tc.trigger, tc.filter)
	}
}
