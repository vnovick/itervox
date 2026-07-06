package orchestrator

import (
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// resolveBackendForIssue computes the effective (cmd, runnerCmd, backend)
// triple for an issue, given the agent defaults from cfg, an optional
// profile override, and an optional per-issue backend override.
//
// Resolution priority (lowest → highest):
//  1. Default command + backend-from-command.
//  2. Default backend (cfg.Agent.Backend), if set.
//  3. Profile.Command (replaces command and backend), if profile is set
//     and Command is non-empty.
//  4. Profile.Backend (overrides backend, keeps command), if non-empty.
//  5. issueBackendOverride (overrides backend, keeps command), if non-empty.
//
// Each higher-priority step rebuilds runnerCmd via CommandWithBackendHint
// when the backend changes, matching the behavior the worker and reviewer
// dispatch paths previously duplicated inline (T-14).
//
// The function is pure — no goroutines, no locks, no global reads. Callers
// resolve the profile pointer and the issue backend under their own locks
// (cfgMu / issueProfilesMu / issueBackendsMu) before invoking.
func resolveBackendForIssue(
	defaultCmd, defaultBackend string,
	profile *config.AgentProfile,
	issueBackendOverride string,
) (cmd, runnerCmd, backend string) {
	cmd = defaultCmd
	runnerCmd = defaultCmd
	backend = agent.BackendFromCommand(defaultCmd)

	if defaultBackend != "" {
		backend = defaultBackend
		runnerCmd = agent.CommandWithBackendHint(cmd, defaultBackend)
	}

	if profile != nil {
		if profile.Command != "" {
			cmd = profile.Command
			runnerCmd = cmd
			backend = agent.BackendFromCommand(cmd)
		}
		if profile.Backend != "" {
			backend = profile.Backend
			runnerCmd = agent.CommandWithBackendHint(cmd, profile.Backend)
		}
	}

	if issueBackendOverride != "" {
		backend = issueBackendOverride
		runnerCmd = agent.CommandWithBackendHint(cmd, issueBackendOverride)
	}

	return cmd, runnerCmd, backend
}
