package main

import (
	"testing"

	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

func TestAutomationDefConfigConversionsPreserveInputRequiredAndRateLimitFields(t *testing.T) {
	t.Parallel()

	defs := []server.AutomationDef{{
		ID:      "input-pr",
		Enabled: true,
		Profile: "codex",
		Trigger: server.AutomationTriggerDef{Type: "input_required"},
		Filter: server.AutomationFilterDef{
			InputContextRegex: "tests",
			MaxAgeMinutes:     30,
		},
		Policy: server.AutomationPolicyDef{AutoResume: true},
	}, {
		ID:      "rate-limit-switch",
		Enabled: true,
		Profile: "claude",
		Trigger: server.AutomationTriggerDef{Type: "rate_limited"},
		Policy: server.AutomationPolicyDef{
			SwitchToProfile: "codex",
			SwitchToBackend: "codex",
			CooldownMinutes: 20,
		},
	}, {
		ID:      "unblock-backlog-to-todo",
		Enabled: true,
		Profile: "pm",
		Trigger: server.AutomationTriggerDef{Type: "blockers_resolved"},
		Policy: server.AutomationPolicyDef{
			MoveToState: "Todo",
		},
	}}

	cfgs := automationconfig.ConfigsFromDefinitions(defs)
	if got := cfgs[0].Filter.MaxAgeMinutes; got != 30 {
		t.Fatalf("MaxAgeMinutes not preserved def->config: %d", got)
	}
	if !cfgs[0].Policy.AutoResume {
		t.Fatalf("AutoResume not preserved def->config")
	}
	if got := cfgs[1].Policy.SwitchToProfile; got != "codex" {
		t.Fatalf("SwitchToProfile not preserved def->config: %q", got)
	}
	if got := cfgs[1].Policy.SwitchToBackend; got != "codex" {
		t.Fatalf("SwitchToBackend not preserved def->config: %q", got)
	}
	if got := cfgs[1].Policy.CooldownMinutes; got != 20 {
		t.Fatalf("CooldownMinutes not preserved def->config: %d", got)
	}
	if got := cfgs[2].Policy.MoveToState; got != "Todo" {
		t.Fatalf("MoveToState not preserved def->config: %q", got)
	}

	roundTrip := automationconfig.DefinitionsFromConfigs([]config.AutomationConfig{{
		ID:      "rate-limit-switch",
		Enabled: true,
		Profile: "claude",
		Trigger: config.AutomationTriggerConfig{Type: "rate_limited"},
		Policy: config.AutomationPolicyConfig{
			SwitchToProfile: "codex",
			SwitchToBackend: "codex",
			CooldownMinutes: 20,
		},
	}, {
		ID:      "unblock-backlog-to-todo",
		Enabled: true,
		Profile: "pm",
		Trigger: config.AutomationTriggerConfig{Type: "blockers_resolved"},
		Policy: config.AutomationPolicyConfig{
			MoveToState: "Todo",
		},
	}})
	if got := roundTrip[0].Policy.SwitchToProfile; got != "codex" {
		t.Fatalf("SwitchToProfile not preserved config->def: %q", got)
	}
	if got := roundTrip[0].Policy.SwitchToBackend; got != "codex" {
		t.Fatalf("SwitchToBackend not preserved config->def: %q", got)
	}
	if got := roundTrip[0].Policy.CooldownMinutes; got != 20 {
		t.Fatalf("CooldownMinutes not preserved config->def: %d", got)
	}
	if got := roundTrip[1].Policy.MoveToState; got != "Todo" {
		t.Fatalf("MoveToState not preserved config->def: %q", got)
	}
}

func TestAutomationsToEntriesPreservesWorkflowPersistenceFields(t *testing.T) {
	t.Parallel()

	entries := []server.AutomationDef{{
		ID:      "input-pr",
		Enabled: true,
		Profile: "input-responder",
		Trigger: server.AutomationTriggerDef{Type: "input_required"},
		Filter: server.AutomationFilterDef{
			InputContextRegex: "tests",
			MaxAgeMinutes:     30,
		},
		Policy: server.AutomationPolicyDef{AutoResume: true},
	}, {
		ID:      "rate-limit-switch",
		Enabled: true,
		Profile: "default",
		Trigger: server.AutomationTriggerDef{Type: "rate_limited"},
		Policy: server.AutomationPolicyDef{
			AutoResume:      true,
			SwitchToProfile: "fallback",
			SwitchToBackend: "codex",
			CooldownMinutes: 45,
		},
	}}

	if got := entries[0].Filter.MaxAgeMinutes; got != 30 {
		t.Fatalf("MaxAgeMinutes not preserved for workflow write: %d", got)
	}
	if !entries[0].Policy.AutoResume {
		t.Fatalf("AutoResume not preserved for workflow write")
	}
	if got := entries[1].Policy.SwitchToProfile; got != "fallback" {
		t.Fatalf("SwitchToProfile not preserved for workflow write: %q", got)
	}
	if got := entries[1].Policy.SwitchToBackend; got != "codex" {
		t.Fatalf("SwitchToBackend not preserved for workflow write: %q", got)
	}
	if got := entries[1].Policy.CooldownMinutes; got != 45 {
		t.Fatalf("CooldownMinutes not preserved for workflow write: %d", got)
	}
}

func TestAutomationProfileCascadesIncludeRateLimitedSwitchProfile(t *testing.T) {
	t.Parallel()

	automations := []config.AutomationConfig{{
		ID:      "rate-limit-switch",
		Enabled: true,
		Profile: "default",
		Trigger: config.AutomationTriggerConfig{Type: "rate_limited"},
		Policy: config.AutomationPolicyConfig{
			AutoResume:      true,
			SwitchToProfile: "fallback",
		},
	}, {
		ID:      "nightly",
		Enabled: true,
		Profile: "qa",
		Trigger: config.AutomationTriggerConfig{Type: "cron"},
	}}

	disabled, changed := disableAutomationsForProfile(automations, "fallback")
	if !changed {
		t.Fatalf("expected disabling switch_to_profile reference to change automations")
	}
	if disabled[0].Enabled {
		t.Fatalf("rate_limited automation must be disabled when switch_to_profile is disabled")
	}
	if !disabled[1].Enabled {
		t.Fatalf("unrelated automation should remain enabled")
	}

	renamed, changed := renameAutomationsProfile(automations, "fallback", "codex-fallback")
	if !changed {
		t.Fatalf("expected switch_to_profile rename to change automations")
	}
	if got := renamed[0].Policy.SwitchToProfile; got != "codex-fallback" {
		t.Fatalf("switch_to_profile not renamed: %q", got)
	}

	removed, changed := removeAutomationsForProfile(automations, "fallback")
	if !changed {
		t.Fatalf("expected switch_to_profile delete to remove dependent automation")
	}
	if len(removed) != 1 || removed[0].ID != "nightly" {
		t.Fatalf("unexpected remaining automations after delete cascade: %#v", removed)
	}
}
