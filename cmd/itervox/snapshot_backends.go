package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/app"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-055 — backend health (the CORE-053 breakers) and auto-switch
// provenance for the snapshot and HEARTBEAT.md. Split out of
// snapshot_build.go by responsibility.

// knownBackends is the sorted set of backends this daemon can dispatch to:
// agent.command's, every enabled profile's (config.ProfileRunsBackend), and
// the backend_fallback chain when enabled. Wrapper commands with no
// recognisable backend contribute nothing.
func knownBackends(cfg *config.Config, profiles map[string]config.AgentProfile) []string {
	set := map[string]struct{}{}
	add := func(b string) {
		if config.IsSupportedBackend(b) {
			set[b] = struct{}{}
		}
	}
	add(configuredBackend(cfg.Agent.Command, cfg.Agent.Backend))
	for _, p := range profiles {
		if config.ProfileEnabled(p) {
			add(config.ProfileRunsBackend(p, cfg.Agent.Command))
		}
	}
	if cfg.Agent.BackendFallback.Enabled {
		for _, b := range cfg.Agent.BackendFallback.Chain {
			add(b)
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

// backendHealthRows renders one row per known backend (healthy unless it
// has a local breaker) plus one per breaker, sorted by backend then host.
// LimitedUntil is set only for a vendor-published reset.
func backendHealthRows(s orchestrator.State, known []string) []server.BackendHealthRow {
	held := map[string]int{}
	for ident, h := range s.BackendLimitedHolds {
		// BH-M3-8: only a hold the breaker still enforces counts.
		if _, running := runningIdentifier(s, ident); !running && orchestrator.BackendHoldActive(s, ident) {
			held[h.Key]++
		}
	}
	rerouted := map[string]int{}
	for _, rec := range s.AutoSwitchInfo {
		if rec.Source == orchestrator.AutoSwitchSourceBackendFallback {
			rerouted[rec.FromKey]++
		}
	}
	rows := make([]server.BackendHealthRow, 0, len(known)+len(s.BackendHealth))
	for _, b := range known {
		if _, ok := s.BackendHealth[b]; !ok {
			rows = append(rows, server.BackendHealthRow{Backend: b, Status: server.BackendHealthHealthy,
				HeldIssues: held[b], ReroutedIssues: rerouted[b]})
		}
	}
	for key, e := range s.BackendHealth {
		row := server.BackendHealthRow{
			Backend: e.Backend, Host: e.Host, Status: e.Status, Kind: e.Kind, LimitType: e.LimitType,
			ProbeIssue: e.ProbeIssue, HeldIssues: held[key], ReroutedIssues: rerouted[key],
		}
		if e.Status == orchestrator.BackendStatusLimited || e.Status == orchestrator.BackendStatusProbing {
			until := e.LimitedUntil
			row.RetryAt = &until
			if e.ResetKnown {
				row.LimitedUntil = &until
			}
		}
		if !e.Since.IsZero() {
			since := e.Since
			row.Since = &since
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Backend != rows[j].Backend {
			return rows[i].Backend < rows[j].Backend
		}
		return rows[i].Host < rows[j].Host
	})
	return rows
}

func runningIdentifier(s orchestrator.State, identifier string) (string, bool) {
	for id, r := range s.Running {
		if r.Issue.Identifier == identifier {
			return id, true
		}
	}
	return "", false
}

// autoSwitchRows lists every automatic override with its provenance, sorted
// by identifier. An override persisted before provenance existed has Source
// "unknown" and only its target.
func autoSwitchRows(s orchestrator.State) []server.AutoSwitchRow {
	if len(s.AutoSwitchedIdentifiers) == 0 {
		return nil
	}
	out := make([]server.AutoSwitchRow, 0, len(s.AutoSwitchedIdentifiers))
	for ident := range s.AutoSwitchedIdentifiers {
		out = append(out, app.AutoSwitchRowFor(s, ident))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identifier < out[j].Identifier })
	return out
}

// heartbeatBackendLine renders one "## Backends" line.
func heartbeatBackendLine(r server.BackendHealthRow) string {
	name := orchestrator.BackendHealthKey(r.Backend, r.Host)
	var b strings.Builder
	fmt.Fprintf(&b, "- %s: %s", name, r.Status)
	switch r.Status {
	case server.BackendHealthLimited:
		switch {
		case r.LimitedUntil != nil:
			fmt.Fprintf(&b, " until %s", r.LimitedUntil.UTC().Format(time.RFC3339))
		case r.RetryAt != nil:
			fmt.Fprintf(&b, ", reset unknown, retry at %s", r.RetryAt.UTC().Format(time.RFC3339))
		default:
			b.WriteString(", reset unknown")
		}
		detail := []string{}
		for _, d := range []string{r.Kind, r.LimitType} {
			if d != "" {
				detail = append(detail, d)
			}
		}
		if len(detail) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(detail, ", "))
		}
	case server.BackendHealthProbing:
		if r.ProbeIssue != "" {
			fmt.Fprintf(&b, " (probe %s)", r.ProbeIssue)
		}
	}
	if r.HeldIssues > 0 || r.ReroutedIssues > 0 {
		fmt.Fprintf(&b, "; held %d, rerouted %d", r.HeldIssues, r.ReroutedIssues)
	}
	return b.String()
}
