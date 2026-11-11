package main

import (
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

// ModelServers implements server.ModelServerLister for
// GET /api/v1/model-servers: the same extraction doctor probes
// (config.ModelServerUses), without probing. agent.profiles and
// agent.ssh_hosts are cfgMu fields, read through their copying accessors;
// agent.command is read-only after startup. Snapshot is not called.
func (a *orchestratorAdapter) ModelServers() []server.ModelServerRow {
	profiles := a.orch.ProfilesCfg()
	hosts, _ := a.orch.SSHHostsCfg()
	uses := config.ModelServerUses(defaultProfileLabel, a.cfg.Agent.Command, profiles)
	rows := make([]server.ModelServerRow, 0, len(uses))
	for _, u := range uses {
		rows = append(rows, server.ModelServerRow{
			Var:       u.Var,
			URL:       u.URL,
			Profiles:  u.Profiles,
			Pools:     u.Pools,
			Remote:    len(hosts) > 0,
			Unchecked: u.Unchecked,
		})
	}
	return rows
}

var _ server.ModelServerLister = (*orchestratorAdapter)(nil)
