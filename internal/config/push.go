package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Push modes: who publishes a run's branch. With "agent" (the default) the
// agent's worktree has git and the daemon's credentials, and the agent (or
// Itervox on its behalf) pushes and opens the pull request, with the
// guardrail shims and pre-push hook as guardrails. With "daemon" the agent
// only emits commits ("patches only"): its environment carries no forge
// credentials, the guardrail shims refuse push and pull-request commands,
// and the daemon itself pushes the branch after the run's gates passed
// (after_run_required, verify, require_evidence) and the lanes allow it.
const (
	// PushAgent is the default: behaviour exactly as before push existed.
	PushAgent = "agent"
	// PushDaemon is "patches only: the daemon pushes".
	PushDaemon = "daemon"
)

// ErrPushDaemonNeedsGuardrails is the load error for push: daemon without
// guardrails.enabled: the shims that refuse the agent's push, and the
// lanes the daemon classifies its own push with, are the guardrails'.
var ErrPushDaemonNeedsGuardrails = errors.New("config: push: daemon needs guardrails.enabled: true")

// ErrPushDaemonOnSSHHost is the load error for push: daemon with SSH
// workers: the agent's environment and shims are applied to local runs
// only, so a remote agent would keep its push ability.
var ErrPushDaemonOnSSHHost = errors.New(`config: push "daemon" is not supported on SSH workers yet`)

// ValidatePushMode rejects an unknown push value. allowEmpty is true for a
// profile, where empty means "inherit agent.push". An unknown value is a
// hard error: falling back to "agent" would give the agent the push
// ability the operator asked to take away.
func ValidatePushMode(field, mode string, allowEmpty bool) error {
	switch mode {
	case PushAgent, PushDaemon:
		return nil
	case "":
		if allowEmpty {
			return nil
		}
	}
	return fmt.Errorf("config: %s must be %q or %q, got %q", field, PushAgent, PushDaemon, mode)
}

// ResolvePush returns the push mode a profile runs with: its own value, or
// agent.push when it sets none, or "agent".
func ResolvePush(profileMode, defaultMode string) string {
	if m := strings.TrimSpace(profileMode); m != "" {
		return m
	}
	if m := strings.TrimSpace(defaultMode); m != "" {
		return m
	}
	return PushAgent
}

// PatchOutProfiles returns the enabled profiles that run with push: daemon,
// sorted; "(default)" stands for agent.command when agent.push is daemon.
func PatchOutProfiles(a AgentConfig) []string {
	var out []string
	for name, p := range a.Profiles {
		if ProfileEnabled(p) && ResolvePush(p.Push, a.Push) == PushDaemon {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if a.Push == PushDaemon {
		out = append([]string{"(default)"}, out...)
	}
	return out
}

// ValidatePushDaemon refuses push: daemon where it cannot hold: without
// guardrails.enabled (no shims to refuse the agent's push) or with
// agent.ssh_hosts (the agent environment is local only).
func ValidatePushDaemon(cfg *Config) error {
	names := PatchOutProfiles(cfg.Agent)
	if len(names) == 0 {
		return nil
	}
	if !cfg.Guardrails.Enabled {
		return fmt.Errorf("%w: %s set push: daemon; set guardrails.enabled: true or push: agent for them",
			ErrPushDaemonNeedsGuardrails, strings.Join(names, ", "))
	}
	if len(cfg.Agent.SSHHosts) > 0 {
		return fmt.Errorf("%w: %s set push: daemon while agent.ssh_hosts is set; remove agent.ssh_hosts or set push: agent for them",
			ErrPushDaemonOnSSHHost, strings.Join(names, ", "))
	}
	return nil
}

// profilePushField parses a profile's push key.
func profilePushField(m map[string]any, name string) (string, error) {
	mode := strings.TrimSpace(strField(m, "push", ""))
	if err := ValidatePushMode("agent.profiles."+name+".push", mode, true); err != nil {
		return "", err
	}
	return mode, nil
}
