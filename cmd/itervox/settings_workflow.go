package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

func profilesToEntries(profiles map[string]config.AgentProfile) map[string]workflow.ProfileEntry {
	if len(profiles) == 0 {
		return nil
	}
	out := make(map[string]workflow.ProfileEntry, len(profiles))
	for name, profile := range profiles {
		enabled := config.ProfileEnabled(profile)
		var enabledField *bool
		if !enabled {
			enabledField = &enabled
		}
		out[name] = workflow.ProfileEntry{
			Command:          profile.Command,
			Prompt:           profile.Prompt,
			SoulFile:         strings.TrimSpace(profile.SoulFile),
			InstructionsFile: strings.TrimSpace(profile.InstructionsFile),
			Backend:          profile.Backend,
			Enabled:          enabledField,
			AllowedActions:   config.NormalizeAllowedActions(profile.AllowedActions),
			CreateIssueState: strings.TrimSpace(profile.CreateIssueState),
		}
	}
	return out
}

func disableAutomationsForProfile(
	automations []config.AutomationConfig,
	profileName string,
) ([]config.AutomationConfig, bool) {
	if len(automations) == 0 || strings.TrimSpace(profileName) == "" {
		return automations, false
	}

	updated := make([]config.AutomationConfig, 0, len(automations))
	changed := false
	for _, automation := range automations {
		cp := automation
		if (strings.TrimSpace(automation.Profile) == profileName ||
			strings.TrimSpace(automation.Policy.SwitchToProfile) == profileName) &&
			automation.Enabled {
			cp.Enabled = false
			changed = true
		}
		if len(automation.Filter.States) > 0 {
			cp.Filter.States = append([]string{}, automation.Filter.States...)
		}
		if len(automation.Filter.LabelsAny) > 0 {
			cp.Filter.LabelsAny = append([]string{}, automation.Filter.LabelsAny...)
		}
		updated = append(updated, cp)
	}
	return updated, changed
}

func renameAutomationsProfile(
	automations []config.AutomationConfig,
	oldName, newName string,
) ([]config.AutomationConfig, bool) {
	if len(automations) == 0 || strings.TrimSpace(oldName) == "" || strings.TrimSpace(newName) == "" || oldName == newName {
		return automations, false
	}

	updated := make([]config.AutomationConfig, 0, len(automations))
	changed := false
	for _, automation := range automations {
		cp := automation
		if strings.TrimSpace(automation.Profile) == oldName {
			cp.Profile = newName
			changed = true
		}
		if strings.TrimSpace(automation.Policy.SwitchToProfile) == oldName {
			cp.Policy.SwitchToProfile = newName
			changed = true
		}
		if len(automation.Filter.States) > 0 {
			cp.Filter.States = append([]string{}, automation.Filter.States...)
		}
		if len(automation.Filter.LabelsAny) > 0 {
			cp.Filter.LabelsAny = append([]string{}, automation.Filter.LabelsAny...)
		}
		updated = append(updated, cp)
	}
	return updated, changed
}

func removeAutomationsForProfile(
	automations []config.AutomationConfig,
	profileName string,
) ([]config.AutomationConfig, bool) {
	if len(automations) == 0 || strings.TrimSpace(profileName) == "" {
		return automations, false
	}

	updated := make([]config.AutomationConfig, 0, len(automations))
	changed := false
	for _, automation := range automations {
		if strings.TrimSpace(automation.Profile) == profileName ||
			strings.TrimSpace(automation.Policy.SwitchToProfile) == profileName {
			changed = true
			continue
		}
		cp := automation
		if len(automation.Filter.States) > 0 {
			cp.Filter.States = append([]string{}, automation.Filter.States...)
		}
		if len(automation.Filter.LabelsAny) > 0 {
			cp.Filter.LabelsAny = append([]string{}, automation.Filter.LabelsAny...)
		}
		updated = append(updated, cp)
	}
	return updated, changed
}

func persistReviewerConfig(path, profile string, autoReview bool) error {
	return workflow.PatchReviewerConfig(path, profile, autoReview)
}

func persistTrackerStates(path string, active, terminal []string, completion string) error {
	return workflow.PatchTrackerStates(path, active, terminal, completion)
}

func persistMaxRetries(path string, n int) error {
	return workflow.PatchAgentMaxRetries(path, n)
}

func persistFailedState(path, stateName string) error {
	return workflow.PatchTrackerFailedState(path, stateName)
}

// persistMaxSwitchesPerIssuePerWindow / persistSwitchWindowHours (gap E)
// reuse the same MutateAgentIntField shape as G's max_retries patcher.
func persistMaxSwitchesPerIssuePerWindow(path string, n int) error {
	return workflow.ApplyAndWriteFrontMatter(path,
		workflow.MutateAgentIntField("max_switches_per_issue_per_window", n))
}

func persistSwitchWindowHours(path string, h int) error {
	return workflow.ApplyAndWriteFrontMatter(path,
		workflow.MutateAgentIntField("switch_window_hours", h))
}

// reloadPlanForRunExit classifies why run() returned and returns the message
// and post-exit delay the outer loop should use.
//
// A clean reload (run() returned because runCtx was cancelled) is identified
// by err == nil OR errors.Is(err, context.Canceled). The wrapped form matters:
// run() may wrap context.Canceled as it propagates up from a worker or
// orchestrator goroutine, and a naive == check would miss that and log the
// reload as an error.
func reloadPlanForRunExit(err error) (string, time.Duration) {
	if err == nil || errors.Is(err, context.Canceled) {
		return "WORKFLOW.md changed — reloading config", 200 * time.Millisecond
	}
	return "run returned with error — retrying", time.Second
}
