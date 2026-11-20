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
// the model-server assignment in front: the local environment does not
// reach the remote shell. Only the turn's command line carries it; the
// command recorded for a later resume stays as configured.
func withRemoteModelServer(agentCommand, assignment, workerHost string) string {
	if assignment == "" || workerHost == "" {
		return agentCommand
	}
	name, value, _ := strings.Cut(assignment, "=")
	return prependEnvToCommand(agentCommand, map[string]string{name: value})
}

// profileStallTimeouts returns the stall timeout of each profile whose runs
// differ from agent.stall_timeout_ms (config.ProfileTimeouts), keyed by
// profile name; "" is a run without a profile, which runs agent.command.
// Event loop: agent.profiles is read under cfgMu, agent.command and
// agent.stall_timeout_ms are read-only after startup.
func (o *Orchestrator) profileStallTimeouts() map[string]int {
	agentStall := o.cfg.Agent.StallTimeoutMs
	defaultCommand := o.cfg.Agent.Command
	o.cfgMu.RLock()
	profiles := o.cfg.Agent.Profiles
	out := map[string]int{}
	for name, p := range profiles {
		if t := config.ProfileTimeouts(p, defaultCommand, config.RunTimeouts{StallMs: agentStall}).StallMs; t != agentStall {
			out[name] = t
		}
	}
	o.cfgMu.RUnlock()
	if t := config.ProfileTimeouts(config.AgentProfile{}, defaultCommand, config.RunTimeouts{StallMs: agentStall}).StallMs; t != agentStall {
		out[""] = t
	}
	return out
}
