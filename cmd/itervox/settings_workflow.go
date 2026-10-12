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
			PermissionMode:   strings.TrimSpace(profile.PermissionMode),
			RequireEvidence:  config.NormalizeEvidenceChecks(profile.RequireEvidence),
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

// persistReviewerConfig saves agent.reviewer_profile and agent.auto_review
// to .itervox/settings.jsonc (#129, see persistSettings). Both are written
// explicitly, "" and false included, so a value in the user settings file
// cannot show through a cleared reviewer.
func persistReviewerConfig(path, profile string, autoReview bool, extra ...workflow.Mutator) error {
	return persistSettings(path, reviewerSettings(profile, autoReview), extra...)
}

func reviewerSettings(profile string, autoReview bool) []setting {
	return []setting{
		{Key: "agent.reviewer_profile", Value: profile},
		{Key: "agent.auto_review", Value: autoReview},
	}
}

// persistTrackerStates is a self-write (CORE-116/CORE-160): the caller
// applies the lists in memory through SetTrackerStatesCfg AND the tracker
// client's tracker.StateListSetter, so the save needs no reload and never
// stops an in-flight turn. tracker.* is not a layered section, so it stays in
// WORKFLOW.md.
func persistTrackerStates(path string, active, terminal []string, completion string) error {
	return workflow.ApplyAndWriteFrontMatter(path, workflow.MutateTrackerStates(active, terminal, completion))
}

// persistMaxWorkers saves agent.max_concurrent_agents (dashboard SetWorkers,
// BumpWorkers and the TUI's worker keys).
func persistMaxWorkers(path string, n int) error {
	return persistSettings(path, []setting{{Key: "agent.max_concurrent_agents", Value: n}})
}

// persistSSHHosts saves agent.ssh_hosts and agent.ssh_host_descriptions in
// one write of each file (T-30/T-40: no torn pair). Both are written whole:
// an empty list or map is written as such, not removed, so the project file
// states the dashboard's list.
func persistSSHHosts(path string, hosts []string, descs map[string]string) error {
	if hosts == nil {
		hosts = []string{}
	}
	if descs == nil {
		descs = map[string]string{}
	}
	return persistSettings(path, []setting{
		{Key: "agent.ssh_hosts", Value: hosts},
		{Key: "agent.ssh_host_descriptions", Value: descs},
	})
}

func persistMaxRetries(path string, n int) error {
	return persistSettings(path, []setting{{Key: "agent.max_retries", Value: n}})
}

// persistFailedState writes WORKFLOW.md: tracker.* is not layered.
func persistFailedState(path, stateName string) error {
	return workflow.PatchTrackerFailedState(path, stateName)
}

func persistMaxSwitchesPerIssuePerWindow(path string, n int) error {
	return persistSettings(path, []setting{{Key: "agent.max_switches_per_issue_per_window", Value: n}})
}

func persistSwitchWindowHours(path string, h int) error {
	return persistSettings(path, []setting{{Key: "agent.switch_window_hours", Value: h}})
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
