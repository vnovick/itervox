//go:build windows

package main

import "context"

// pgidLedger is the CORE-042 orphan ledger's Windows stand-in, and does
// nothing. agent_pgids.go records process groups with ps so a restarted
// daemon can reap what a kill -9'd one left behind. On Windows there is
// nothing to reap: procgroup.Start puts every agent and hook in a Job
// Object with kill-on-close, and the OS closes the dead daemon's job
// handles — killing every process in them — the moment it dies.
type pgidLedger struct{}

// startAgentPGIDLedger installs no observer on Windows (see pgidLedger).
func startAgentPGIDLedger(string) *pgidLedger { return &pgidLedger{} }

// Run returns at once.
func (*pgidLedger) Run(context.Context) {}
