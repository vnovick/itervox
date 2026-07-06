package main

import (
	"fmt"
	"slices"
	"strings"
)

// adapter_retries.go houses the retry-budget settings methods of
// orchestratorAdapter. Extracted from adapter_settings.go to keep that file
// under its size-budget cap. All methods follow the persist-then-mutate
// convention enforced by adapter_convention_test.go.

// SetMaxRetries persists agent.max_retries to WORKFLOW.md, then applies the
// change to the orchestrator. Negative values are clamped to 0 (unlimited).
func (a *orchestratorAdapter) SetMaxRetries(n int) error {
	if n < 0 {
		n = 0
	}
	if err := persistMaxRetries(a.workflowPath, n); err != nil {
		return fmt.Errorf("persist max_retries: %w", err)
	}
	a.orch.SetMaxRetriesCfg(n)
	return nil
}

func (a *orchestratorAdapter) MaxRetries() int {
	return a.orch.MaxRetriesCfg()
}

// SetFailedState validates that stateName is a known tracker state (or empty,
// meaning "pause"), persists tracker.failed_state to WORKFLOW.md, then applies
// the change to the orchestrator.
func (a *orchestratorAdapter) SetFailedState(stateName string) error {
	stateName = strings.TrimSpace(stateName)
	if stateName != "" {
		active, terminal, _ := a.orch.TrackerStatesCfg()
		// BacklogStates is read-only after startup (CLAUDE.md), so direct
		// cfg read is safe.
		known := append(append(append([]string{}, active...), terminal...), a.cfg.Tracker.BacklogStates...)
		if !slices.Contains(known, stateName) {
			return fmt.Errorf("failed_state %q is not in tracker.active_states / terminal_states / backlog_states", stateName)
		}
	}
	if err := persistFailedState(a.workflowPath, stateName); err != nil {
		return fmt.Errorf("persist failed_state: %w", err)
	}
	a.orch.SetFailedStateCfg(stateName)
	return nil
}

func (a *orchestratorAdapter) FailedState() string {
	return a.orch.FailedStateCfg()
}

// SetMaxSwitchesPerIssuePerWindow persists agent.max_switches_per_issue_per_window
// to WORKFLOW.md, then applies the change to the orchestrator. Negative values
// are clamped to 0 (= unlimited). Gap E.
func (a *orchestratorAdapter) SetMaxSwitchesPerIssuePerWindow(n int) error {
	if n < 0 {
		n = 0
	}
	if err := persistMaxSwitchesPerIssuePerWindow(a.workflowPath, n); err != nil {
		return fmt.Errorf("persist max_switches_per_issue_per_window: %w", err)
	}
	a.orch.SetMaxSwitchesPerIssuePerWindowCfg(n)
	return nil
}

func (a *orchestratorAdapter) MaxSwitchesPerIssuePerWindow() int {
	return a.orch.MaxSwitchesPerIssuePerWindowCfg()
}

// SetSwitchWindowHours persists agent.switch_window_hours to WORKFLOW.md, then
// applies the change to the orchestrator. Values <= 0 normalise to 6h. Gap E.
func (a *orchestratorAdapter) SetSwitchWindowHours(h int) error {
	if h <= 0 {
		h = 6
	}
	if err := persistSwitchWindowHours(a.workflowPath, h); err != nil {
		return fmt.Errorf("persist switch_window_hours: %w", err)
	}
	a.orch.SetSwitchWindowHoursCfg(h)
	return nil
}

func (a *orchestratorAdapter) SwitchWindowHours() int {
	return a.orch.SwitchWindowHoursCfg()
}
