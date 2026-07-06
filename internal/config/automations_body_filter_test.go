package config

import (
	"slices"
	"testing"
)

func TestParseAutomations_BodyContainsAndRegexParsed(t *testing.T) {
	raw := []any{
		map[string]any{
			"id":      "merge-on-approval",
			"profile": "merge-bot",
			"trigger": map[string]any{
				"type": "tracker_comment_added",
			},
			"filter": map[string]any{
				"states": []any{"In Review"},
				"body_contains": []any{
					"AI review passed",
					"ready for merge",
				},
				"body_regex": `(?i)PR-\d+`,
			},
		},
	}
	got := parseAutomations(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 automation, got %d", len(got))
	}
	a := got[0]
	wantSubs := []string{"AI review passed", "ready for merge"}
	for _, s := range wantSubs {
		if !slices.Contains(a.Filter.BodyContains, s) {
			t.Errorf("BodyContains missing %q; got %v", s, a.Filter.BodyContains)
		}
	}
	if a.Filter.BodyRegex != `(?i)PR-\d+` {
		t.Errorf("BodyRegex = %q, want %q", a.Filter.BodyRegex, `(?i)PR-\d+`)
	}
}

func TestValidateAutomations_BodyContainsRejectedOnNonCommentTrigger(t *testing.T) {
	profiles := map[string]AgentProfile{
		"any": {Command: "claude"},
	}
	automations := []AutomationConfig{{
		ID:      "x",
		Enabled: true,
		Profile: "any",
		Trigger: AutomationTriggerConfig{Type: AutomationTriggerCron, Cron: "0 9 * * *"},
		Filter:  AutomationFilterConfig{BodyContains: []string{"merge it"}},
	}}
	err := ValidateAutomations(automations, profiles)
	if err == nil {
		t.Fatal("expected error: body_contains on cron trigger")
	}
}

func TestValidateAutomations_InvalidBodyRegexRejected(t *testing.T) {
	profiles := map[string]AgentProfile{"merge-bot": {Command: "claude"}}
	automations := []AutomationConfig{{
		ID:      "x",
		Enabled: true,
		Profile: "merge-bot",
		Trigger: AutomationTriggerConfig{Type: AutomationTriggerTrackerComment},
		Filter:  AutomationFilterConfig{BodyRegex: "([invalid"},
	}}
	err := ValidateAutomations(automations, profiles)
	if err == nil {
		t.Fatal("expected error: invalid body_regex")
	}
}

func TestValidateAutomations_BodyContainsAcceptedOnTrackerComment(t *testing.T) {
	profiles := map[string]AgentProfile{"merge-bot": {Command: "claude"}}
	automations := []AutomationConfig{{
		ID:      "merge-on-approval",
		Enabled: true,
		Profile: "merge-bot",
		Trigger: AutomationTriggerConfig{Type: AutomationTriggerTrackerComment},
		Filter:  AutomationFilterConfig{BodyContains: []string{"ready for merge"}},
	}}
	if err := ValidateAutomations(automations, profiles); err != nil {
		t.Fatalf("expected no error; got %v", err)
	}
}
