package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// IsolationCheck is doctor's "Isolation" section (#258): the sandbox
// mechanisms this machine has, the profiles that ask for process
// isolation, and the policy lists a sandboxed run gets.
type IsolationCheck struct {
	// Mechanisms is every mechanism this platform knows, in preference
	// order, with why it is unavailable (nil Err = available).
	Mechanisms []agent.IsolationMechanismStatus
	// Profiles are the enabled profiles running with isolation "process"
	// ("(default)" for agent.command under agent.isolation: process).
	Profiles []string
	// WorkspaceRoot and ProjectDir name where the lists apply.
	WorkspaceRoot string
	ProjectDir    string
	// EgressProfiles are the enabled profiles running with egress:
	// allowlist ("(default)" for agent.command under agent.egress).
	EgressProfiles []string
	// EgressMechanism is the sandbox that can close the network here
	// (bubblewrap with network and pid namespaces, sandbox-exec), "" when
	// none can; EgressUnavailable is why.
	EgressMechanism   string
	EgressUnavailable string
}

// Available is the mechanism runs would use, "" when none works.
func (c IsolationCheck) Available() string {
	for _, m := range c.Mechanisms {
		if m.Err == nil {
			return m.Name
		}
	}
	return ""
}

// Failing reports profiles asking for process isolation on a machine where
// no sandbox works, or for egress where no sandbox can close the network:
// every run of theirs is refused.
func (c IsolationCheck) Failing() bool {
	return (len(c.Profiles) > 0 && c.Available() == "") || c.egressFailing()
}

func (c IsolationCheck) egressFailing() bool {
	return len(c.EgressProfiles) > 0 && c.EgressMechanism == ""
}

// isolationMechanismReport is agent.IsolationMechanismReport; tests swap it.
var isolationMechanismReport = agent.IsolationMechanismReport

// egressAvailability is agent.EgressAvailability; tests swap it.
var egressAvailability = agent.EgressAvailability

func checkIsolation(cfg *config.Config) IsolationCheck {
	c := IsolationCheck{Mechanisms: isolationMechanismReport()}
	if name, err := egressAvailability(); err != nil {
		c.EgressUnavailable = err.Error()
	} else {
		c.EgressMechanism = name
	}
	if cfg != nil {
		c.Profiles = config.IsolatedProfiles(cfg.Agent)
		c.EgressProfiles = config.EgressProfiles(cfg.Agent)
		c.WorkspaceRoot = cfg.Workspace.Root
		c.ProjectDir = filepath.Dir(cfg.WorkflowPath)
		if abs, err := filepath.Abs(c.ProjectDir); err == nil {
			c.ProjectDir = abs
		}
	}
	return c
}

func renderIsolationCheck(b *strings.Builder, c IsolationCheck) {
	b.WriteString("\nIsolation\n")
	if len(c.Mechanisms) == 0 {
		b.WriteString("  mechanisms: none supported on this operating system\n")
	}
	for _, m := range c.Mechanisms {
		if m.Err == nil {
			fmt.Fprintf(b, "  %s: available\n", m.Name)
		} else {
			fmt.Fprintf(b, "  %s: unavailable — %v\n", m.Name, m.Err)
		}
	}
	if c.EgressMechanism != "" {
		fmt.Fprintf(b, "  egress enforcement: available via %s\n", c.EgressMechanism)
	} else {
		fmt.Fprintf(b, "  egress enforcement: unavailable — %s\n", strings.TrimPrefix(c.EgressUnavailable, agent.ErrIsolationUnavailable.Error()+": "))
	}
	if len(c.Profiles) == 0 {
		b.WriteString("  profiles with isolation: process: none\n")
		return
	}
	fmt.Fprintf(b, "  profiles with isolation: process: %s\n", strings.Join(c.Profiles, ", "))
	if c.Available() == "" {
		fmt.Fprintf(b, "ERROR: %s ask for process isolation but no sandbox works on this machine; their runs do not start — install bubblewrap (Linux) or set isolation: none\n",
			strings.Join(c.Profiles, ", "))
		return
	}
	fmt.Fprintf(b, "  sandbox: %s\n", c.Available())
	write := []string{"<workspace>", "<worktree git dir>, <shared git objects, refs, logs>", "<temp dir>"}
	write = append(write, homeRel(agent.IsolationHomeWrite)...)
	fmt.Fprintf(b, "  writable: %s (+ isolation_extra_write)\n", strings.Join(write, ", "))
	deny := homeRel(agent.IsolationHomeDeny)
	for _, p := range agent.IsolationProjectDeny {
		deny = append(deny, filepath.Join(c.ProjectDir, p))
	}
	if c.WorkspaceRoot != "" {
		deny = append(deny, "every other workspace under "+c.WorkspaceRoot)
	}
	fmt.Fprintf(b, "  unreadable: %s (+ isolation_extra_deny)\n", strings.Join(deny, ", "))
	renderEgressCheck(b, c)
	b.WriteString("  hooks: not sandboxed\n")
}

// renderEgressCheck is the network part of the Isolation section.
func renderEgressCheck(b *strings.Builder, c IsolationCheck) {
	if len(c.EgressProfiles) == 0 {
		b.WriteString("  network: allowed (no profile sets egress: allowlist)\n")
		return
	}
	fmt.Fprintf(b, "  profiles with egress: allowlist: %s\n", strings.Join(c.EgressProfiles, ", "))
	if c.egressFailing() {
		fmt.Fprintf(b, "ERROR: %s ask for egress: allowlist but no sandbox here can close the network; their runs do not start — install bubblewrap with user namespaces (Linux) or set egress: off\n",
			strings.Join(c.EgressProfiles, ", "))
		return
	}
	fmt.Fprintf(b, "  network: closed for them but the allowlist, through a per-run proxy (%s); git credentials hidden: %s, the SSH agent socket\n",
		c.EgressMechanism, strings.Join(homeRel(append(append([]string{}, agent.IsolationPatchOutHomeDeny...), agent.IsolationEgressHomeDeny...)), ", "))
	fmt.Fprintf(b, "  default allowlist: model API hosts per backend (claude: %s; codex: %s), registries: %s (agent.egress_defaults)\n",
		strings.Join(config.EgressModelHosts[config.BackendClaude], ", "), strings.Join(config.EgressModelHosts[config.BackendCodex], ", "),
		strings.Join(config.EgressRegistryHosts, ", "))
}

func homeRel(rels []string) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = "~/" + r
	}
	return out
}
