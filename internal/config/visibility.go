package config

import (
	"log/slog"
	"strings"
)

// System visibility (agent.resource_sampling, agent.activity_audit,
// workspace.disk_warn_free_gb, workspace.disk_pause_free_gb). All read-only
// after startup: cmd/itervox builds the run monitor from them once per
// generation.

// Activity audit modes (agent.activity_audit).
const (
	// ActivityAuditOff records no activity.
	ActivityAuditOff = "off"
	// ActivityAuditSampled records what the 2s resource sampler sees: new
	// processes, open files (Linux) and sockets. The default.
	ActivityAuditSampled = "sampled"
	// ActivityAuditPrecise records every exec, open-for-write and connect
	// through eBPF (Linux, CAP_BPF + CAP_PERFMON or root, tracefs mounted);
	// it falls back to sampled, with the reason, when that is unavailable.
	ActivityAuditPrecise = "precise"
)

// DefaultDiskWarnFreeGB is workspace.disk_warn_free_gb when unset.
const DefaultDiskWarnFreeGB = 5

// parseVisibilityConfig reads the system-visibility keys of the agent and
// workspace sections. An unknown activity mode falls back to sampled with
// a warning; negative thresholds are 0 (off).
func parseVisibilityConfig(agent, ws map[string]any, cfg *Config) {
	cfg.Agent.ResourceSampling = boolField(agent, "resource_sampling", true)
	mode := strings.ToLower(strings.TrimSpace(strField(agent, "activity_audit", ActivityAuditSampled)))
	switch mode {
	case ActivityAuditOff, ActivityAuditSampled, ActivityAuditPrecise:
	default:
		slog.Warn("config: agent.activity_audit unrecognized, using sampled",
			"value", mode, "allowed", "off, sampled, precise")
		mode = ActivityAuditSampled
	}
	cfg.Agent.ActivityAudit = mode
	cfg.Workspace.DiskWarnFreeGB = max(0, intField(ws, "disk_warn_free_gb", DefaultDiskWarnFreeGB))
	cfg.Workspace.DiskPauseFreeGB = max(0, intField(ws, "disk_pause_free_gb", 0))
}
