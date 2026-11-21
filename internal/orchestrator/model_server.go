package orchestrator

import (
	"context"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// Local models: a profile's model_server reaches its agent as the variable
// its backend reads (config.ProfileModelServerEnv), and its runs get the
// profile's timeouts (config.ProfileTimeouts).

// withLocalModelServer puts assignment (NAME=value, from
// config.ProfileModelServerEnv) in a local agent's environment through the
// run environment's Set list, which a login shell's profile cannot
// override. An SSH worker gets it from withRemoteModelServer.
func withLocalModelServer(ctx context.Context, assignment, workerHost string) context.Context {
	if workerHost != "" {
		return ctx
	}
	return agent.WithSetEnv(ctx, assignment)
}

// withRemoteModelServer returns the turn's command for an SSH worker with
// the model-server assignment exported in front (agent.ExportStatement):
// the local environment does not reach the remote shell. A statement, not
// `NAME=value command`, so a compound command (`source ~/.nvm/nvm.sh &&
// claude`) gets it for the CLI and not only for its first command. The
// command's backend (its hint, else its program) moves into a leading hint,
// because the runner picks its backend from the command's first word, which
// is now `export`. Only the turn's command line carries it; the command
// recorded for a later resume stays as configured.
func withRemoteModelServer(agentCommand, assignment, workerHost string) string {
	if assignment == "" || workerHost == "" {
		return agentCommand
	}
	backend, rest := config.SplitBackendFromCommand(agentCommand)
	export := agent.ExportStatement(assignment) + strings.TrimSpace(rest)
	if backend == "" {
		return export // the default runner, as for the command alone
	}
	return config.BackendHintPrefix + backend + " " + export
}

// runStallTimeouts returns the stall timeout of each running entry whose
// run differs from agent.stall_timeout_ms (config.ProfileTimeouts for the
// entry's profile and the backend it runs on), keyed like state.Running.
// A run without a profile ("") runs agent.command and keeps the agent
// level. Event loop: agent.profiles is read under cfgMu, agent.command and
// agent.stall_timeout_ms are read-only after startup.
func (o *Orchestrator) runStallTimeouts(state State) map[string]int {
	if len(state.Running) == 0 {
		return nil
	}
	agentStall := o.cfg.Agent.StallTimeoutMs
	defaultCommand := o.cfg.Agent.Command
	out := map[string]int{}
	o.cfgMu.RLock()
	profiles := o.cfg.Agent.Profiles
	for id, entry := range state.Running {
		if entry == nil || entry.ProfileName == "" {
			continue
		}
		p, ok := profiles[entry.ProfileName]
		if !ok {
			continue
		}
		if t := config.ProfileTimeouts(p, entry.Backend, defaultCommand, config.RunTimeouts{StallMs: agentStall}).StallMs; t != agentStall {
			out[id] = t
		}
	}
	o.cfgMu.RUnlock()
	return out
}
