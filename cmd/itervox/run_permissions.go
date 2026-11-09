package main

import (
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// defaultProfileLabel names agent.command, which runs every issue that no
// profile claims.
const defaultProfileLabel = "(default)"

// profileRunMode is how one enabled profile's agent runs: its backend, the
// permission mode its turns launch with and its isolation, as the worker
// resolves them at dispatch.
type profileRunMode struct {
	Name       string
	Backend    string // "" when the command is an opaque wrapper
	Permission agent.PermissionMode
	Isolation  string
	// Full is set when nothing confines the agent: approvals are bypassed
	// (or the backend has no confining mode) and no sandbox wraps it. The
	// agent can run any command and read or change any file the daemon's
	// user can.
	Full bool
}

// runModes lists agent.command first, then every enabled profile by name.
func runModes(a config.AgentConfig) []profileRunMode {
	modes := []profileRunMode{runModeFor(defaultProfileLabel, config.AgentProfile{}, a)}
	names := make([]string, 0, len(a.Profiles))
	for name, p := range a.Profiles {
		if config.ProfileEnabled(p) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		modes = append(modes, runModeFor(name, a.Profiles[name], a))
	}
	return modes
}

func runModeFor(name string, p config.AgentProfile, a config.AgentConfig) profileRunMode {
	m := profileRunMode{
		Name:      name,
		Backend:   config.ProfileRunsBackend(p, a.Command),
		Isolation: config.ResolveIsolation(p.Isolation, a.Isolation),
		// The worker's agent.ParsePermissionMode: an empty or unknown value
		// runs as bypass. Resolved here without its warning log.
		Permission: agent.DefaultPermissionMode,
	}
	if agent.PermissionMode(strings.TrimSpace(p.PermissionMode)) == agent.PermissionSandbox {
		m.Permission = agent.PermissionSandbox
	}
	// Cursor passes --force --trust in both modes and a custom backend gets
	// no permission flags at all, so sandbox mode confines neither.
	confinedByMode := m.Permission == agent.PermissionSandbox &&
		m.Backend != config.BackendCursor && m.Backend != config.BackendCustom
	m.Full = m.Isolation != config.IsolationProcess && !confinedByMode
	return m
}

// fullPermissionNames are the names of the run modes nothing confines.
func fullPermissionNames(modes []profileRunMode) []string {
	var out []string
	for _, m := range modes {
		if m.Full {
			out = append(out, m.Name)
		}
	}
	return out
}
