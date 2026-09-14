package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workflow"
)

// adapter_settings.go houses the settings/config-mutating methods of
// orchestratorAdapter. Extracted from main.go (G-12, gaps_280426_2) so the
// entry-point file stays under its size-budget cap. All methods follow the
// persist-then-mutate convention enforced by adapter_convention_test.go.

// SetWorkers persists max_concurrent_agents to WORKFLOW.md FIRST, then
// applies the change to the orchestrator. Persist-then-mutate matches the
// convention used by every other setter in this adapter (UpsertProfile,
// SetAutomations, etc.) — without it, an HTTP 200 could be returned while
// the WORKFLOW.md write silently failed, and the value would revert at the
// next daemon restart, confusing the user.
func (a *orchestratorAdapter) SetWorkers(n int) error {
	clamped := max(1, min(n, 50))
	if err := workflow.PatchIntField(a.workflowPath, "max_concurrent_agents", clamped); err != nil {
		return fmt.Errorf("persist max_concurrent_agents: %w", err)
	}
	a.orch.SetMaxWorkers(clamped)
	return nil
}

func (a *orchestratorAdapter) BumpWorkers(delta int) (int, error) {
	current := a.orch.MaxWorkers()
	next := max(1, min(current+delta, 50))
	if err := workflow.PatchIntField(a.workflowPath, "max_concurrent_agents", next); err != nil {
		return 0, fmt.Errorf("persist max_concurrent_agents: %w", err)
	}
	a.orch.SetMaxWorkers(next)
	return next, nil
}

func (a *orchestratorAdapter) SetIssueProfile(identifier, profile string) {
	a.orch.SetIssueProfile(identifier, profile)
}

func (a *orchestratorAdapter) SetIssueBackend(identifier, backend string) {
	a.orch.SetIssueBackend(identifier, backend)
}

// BumpCommentCount delegates to the orchestrator's per-identifier counter
// (T-6). HTTP handlers call this after a successful agent-comment action so
// the dashboard's snapshot row reflects review activity.
func (a *orchestratorAdapter) BumpCommentCount(identifier string) {
	a.orch.BumpCommentCount(identifier)
}

// TestAutomation delegates to the orchestrator's one-off test dispatcher
// (T-10). The HTTP handler invokes it on POST /api/v1/automations/{id}/test
// to fire a single run tagged TriggerType="test" without waiting for the
// rule's normal schedule.
func (a *orchestratorAdapter) TestAutomation(ctx context.Context, automationID, identifier string) error {
	return a.orch.TestAutomation(ctx, automationID, identifier)
}

func (a *orchestratorAdapter) ReviewerConfig() (string, bool) {
	return a.orch.ReviewerCfg()
}

func (a *orchestratorAdapter) SetReviewerConfig(profile string, autoReview bool) error {
	prevProfile, prevAutoReview := a.orch.ReviewerCfg()
	if err := config.ValidateReviewerAutoReview(profile, autoReview); err != nil {
		return err
	}
	if err := config.ValidateReviewerProfile(a.orch.ProfilesCfg(), profile); err != nil {
		return err
	}
	if err := config.ValidateAutoClearAutoReview(a.orch.AutoClearWorkspaceCfg(), profile, autoReview); err != nil {
		return err
	}
	if err := persistReviewerConfig(a.workflowPath, profile, autoReview); err != nil {
		return err
	}
	if err := a.orch.SetReviewerCfg(profile, autoReview); err != nil {
		_ = persistReviewerConfig(a.workflowPath, prevProfile, prevAutoReview)
		return err
	}
	a.notify()
	return nil
}

func (a *orchestratorAdapter) AvailableModels() map[string][]server.ModelOption {
	models := a.orch.AvailableModelsCfg()
	result := make(map[string][]server.ModelOption, len(models))
	for backend, opts := range models {
		converted := make([]server.ModelOption, len(opts))
		for i, m := range opts {
			converted[i] = server.ModelOption{ID: m.ID, Label: m.Label}
		}
		result[backend] = converted
	}
	return result
}

func (a *orchestratorAdapter) SetAutomations(automations []server.AutomationDef) error {
	if err := workflow.PatchAutomationsBlock(a.workflowPath, automations); err != nil {
		return err
	}
	// no rollback needed: SetAutomationsCfg is infallible (slice assignment under cfgMu).
	a.orch.SetAutomationsCfg(automationconfig.ConfigsFromDefinitions(automations))
	a.notify()
	return nil
}

func (a *orchestratorAdapter) ClearAllWorkspaces() error {
	// Clear run history (in-memory + disk) so Timeline resets.
	a.orch.ClearHistory()

	root := a.cfg.Workspace.Root
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clear workspaces: read dir %s: %w", root, err)
	}
	var firstErr error
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *orchestratorAdapter) SetAutoClearWorkspace(enabled bool) error {
	reviewerProfile, autoReview := a.orch.ReviewerCfg()
	prevEnabled := a.orch.AutoClearWorkspaceCfg()
	if err := config.ValidateAutoClearAutoReview(enabled, reviewerProfile, autoReview); err != nil {
		return err
	}
	if err := workflow.PatchWorkspaceBoolField(a.workflowPath, "auto_clear", enabled); err != nil {
		return err
	}
	if err := a.orch.SetAutoClearWorkspaceCfg(enabled); err != nil {
		_ = workflow.PatchWorkspaceBoolField(a.workflowPath, "auto_clear", prevEnabled)
		return err
	}
	a.notify()
	return nil
}

func (a *orchestratorAdapter) UpdateTrackerStates(active, terminal []string, completion string) error {
	if err := persistTrackerStates(a.workflowPath, active, terminal, completion); err != nil {
		return err
	}
	// no rollback needed: SetTrackerStatesCfg is infallible (3 field assignments under cfgMu).
	a.orch.SetTrackerStatesCfg(active, terminal, completion)
	a.notify()
	return nil
}

func (a *orchestratorAdapter) AddSSHHost(host, description string) error {
	hosts, descs := a.orch.SSHHostsCfg()
	if descs == nil {
		descs = make(map[string]string)
	}
	if !slices.Contains(hosts, host) {
		hosts = append(hosts, host)
	}
	if description == "" {
		delete(descs, host)
	} else {
		descs[host] = description
	}
	// T-30/T-40: one atomic write covering both `ssh_hosts` and
	// `ssh_host_descriptions`. The previous two-Patch* sequence had a torn-write
	// window on SIGKILL between the two writes.
	if err := workflow.NewDoc(a.workflowPath).SetSSHHosts(hosts, descs).Save(); err != nil {
		return err
	}
	// no rollback needed: AddSSHHostCfg is infallible (slice append + map assignment under cfgMu).
	a.orch.AddSSHHostCfg(host, description)
	a.notify()
	return nil
}

func (a *orchestratorAdapter) RemoveSSHHost(host string) error {
	hosts, descs := a.orch.SSHHostsCfg()
	nextHosts := make([]string, 0, len(hosts))
	for _, existing := range hosts {
		if existing != host {
			nextHosts = append(nextHosts, existing)
		}
	}
	delete(descs, host)
	// T-30/T-40: atomic cascade — same rationale as AddSSHHost above.
	if err := workflow.NewDoc(a.workflowPath).SetSSHHosts(nextHosts, descs).Save(); err != nil {
		return err
	}
	// no rollback needed: RemoveSSHHostCfg is infallible (slice rebuild + map delete under cfgMu).
	a.orch.RemoveSSHHostCfg(host)
	a.notify()
	return nil
}

func (a *orchestratorAdapter) SetDispatchStrategy(strategy string) error {
	if err := workflow.PatchAgentStringField(a.workflowPath, "dispatch_strategy", strategy); err != nil {
		return err
	}
	// no rollback needed: SetDispatchStrategyCfg is infallible (string assignment under cfgMu).
	a.orch.SetDispatchStrategyCfg(strategy)
	a.notify()
	return nil
}

func (a *orchestratorAdapter) ProvideInput(identifier, message string) bool {
	return a.orch.ProvideInput(identifier, message)
}

func (a *orchestratorAdapter) DismissInput(identifier string) bool {
	return a.orch.DismissInput(identifier)
}

func (a *orchestratorAdapter) SetDepsOverride(identifier string, enabled bool) bool {
	return a.orch.SetDepsOverride(identifier, enabled)
}

func (a *orchestratorAdapter) SetInlineInput(enabled bool) error {
	if err := workflow.PatchAgentBoolField(a.workflowPath, "inline_input", enabled); err != nil {
		return err
	}
	a.orch.SetInlineInputCfg(enabled)
	a.notify()
	return nil
}
