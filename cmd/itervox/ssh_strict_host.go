package main

import (
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// applySSHStrictHostConfig pushes the WORKFLOW.md StrictHostKeyChecking
// settings into the agent package. run() calls it once per generation, so it
// runs at startup and again on every reload.
//
// The apply is symmetric (CORE-140): an unset agent.ssh_strict_host_checking
// restores agent.DefaultSSHStrictHostMode ("accept-new") rather than leaving
// whatever an earlier generation set, exactly as a nil per-host map clears
// the per-host overrides. Otherwise an operator who removed the key would
// keep the old — possibly insecure "no" — mode until the process restarts.
func applySSHStrictHostConfig(cfg *config.Config) {
	mode := cfg.Agent.SSHStrictHostChecking
	if mode == "" {
		mode = agent.DefaultSSHStrictHostMode
	}
	agent.SetSSHStrictHostDefault(mode)
	agent.SetSSHStrictHostOverrides(cfg.Agent.SSHStrictHostByHost)
}
