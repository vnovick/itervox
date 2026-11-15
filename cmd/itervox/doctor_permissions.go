package main

import (
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// safetyGuideURL is the safety page doctor and `config show` point to.
const safetyGuideURL = "https://itervox.dev/guides/safety/"

// PermissionsCheck is doctor's "Permissions" section: how each enabled
// profile's agent runs, and which of them nothing confines. Full
// permissions warn; they never change the exit code, since bypass is the
// default every headless setup starts from.
type PermissionsCheck struct {
	Modes []profileRunMode
	// SSHHosts is set when runs go to agent.ssh_hosts, so the warning
	// names those machines instead of this one.
	SSHHosts bool
}

// Full are the run modes with full permissions.
func (c PermissionsCheck) Full() []string { return fullPermissionNames(c.Modes) }

func checkPermissions(cfg *config.Config) PermissionsCheck {
	if cfg == nil {
		return PermissionsCheck{}
	}
	return PermissionsCheck{Modes: runModes(cfg.Agent), SSHHosts: len(cfg.Agent.SSHHosts) > 0}
}

func renderPermissionsCheck(b *strings.Builder, c PermissionsCheck) {
	if len(c.Modes) == 0 {
		return
	}
	b.WriteString("\nPermissions\n")
	for _, m := range c.Modes {
		fmt.Fprintf(b, "  %s: %s\n", m.Name, describeRunMode(m))
	}
	full := c.Full()
	if len(full) == 0 {
		return
	}
	where := "this machine"
	if c.SSHHosts {
		where = "the SSH worker hosts"
	}
	fmt.Fprintf(b, "WARNING: %s run with full permissions: their agents can run any command and read or change any file the daemon's user can on %s, "+
		"including credentials in your home directory. Run Itervox on a disposable machine, VM or container, or confine these profiles "+
		"with isolation: process or permission_mode: sandbox. See %s\n",
		strings.Join(full, ", "), where, safetyGuideURL)
}

// describeRunMode is one profile's line: backend, permission mode,
// isolation, and whether anything confines it.
func describeRunMode(m profileRunMode) string {
	backend := m.Backend
	if backend == "" {
		backend = "custom command"
	}
	s := fmt.Sprintf("%s, permission_mode %s, isolation %s", backend, m.Permission, m.Isolation)
	switch {
	case m.Full:
		return s + " — full permissions"
	case m.Isolation == config.IsolationProcess:
		return s + " — operating-system sandbox"
	default:
		// sandbox mode limits what the agent does without asking, not what
		// its process can reach (internal/agent/permission.go).
		return s + " — restricted permission mode, no operating-system sandbox"
	}
}
