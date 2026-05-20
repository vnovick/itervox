package skills

import (
	"sort"
	"strings"
)

// RecommendAnalytics produces analytics-tab Recommendations from the
// AnalyticsSnapshot + the static Inventory. Implements four design-draft
// rules:
//
//   - HIGH_COST_LOW_USAGE   — warn: a capability has high token cost but
//     fewer than 2 runtime loads in the lookback window.
//   - HOOK_STORM            — warn: a single hook fired ≥ 50 times in the
//     lookback (high overhead, possible duplicate execution).
//   - CONFIGURED_NOT_LOADED — info: configured but never observed at runtime
//     (indicates dead config or recently-added capability).
//   - LOADED_NOT_CONFIGURED — info: observed at runtime but not present in
//     the static inventory (indicates ambient install or stale cache).
//
// Output is sorted by (Severity desc, ID asc) so the dashboard renders the
// most-actionable rows first.
func RecommendAnalytics(snap *AnalyticsSnapshot, inv *Inventory) []Recommendation {
	if snap == nil || inv == nil || !snap.HasRuntimeEvidence {
		return nil
	}
	const (
		highTokenThreshold = 2_000
		lowUsageThreshold  = 2
		hookStormThreshold = 50
	)
	var out []Recommendation

	configuredNames := configuredCapabilityNames(inv)

	// Group skill stats by CapabilityID before applying per-name rules.
	// Without grouping, a skill that exists in multiple scopes (e.g. graphify
	// in both `~/.claude/skills/` and `~/.agents/skills/`) fires every rule
	// once per copy, swamping the recommendations panel.
	type aggregated struct {
		maxTokens   int
		totalLoads  int
		anyVerified bool
	}
	bySkill := make(map[string]aggregated, len(snap.SkillStats))
	for _, stat := range snap.SkillStats {
		agg := bySkill[stat.CapabilityID]
		if stat.ApproxTokens > agg.maxTokens {
			agg.maxTokens = stat.ApproxTokens
		}
		agg.totalLoads += stat.RuntimeLoads
		agg.anyVerified = agg.anyVerified || stat.RuntimeVerified
		bySkill[stat.CapabilityID] = agg
	}

	for name, agg := range bySkill {
		// HIGH_COST_LOW_USAGE — fires once per skill name across all scopes.
		if agg.maxTokens >= highTokenThreshold && agg.totalLoads < lowUsageThreshold {
			out = append(out, Recommendation{
				ID:          "HIGH_COST_LOW_USAGE",
				Severity:    "warn",
				Category:    "cost",
				Title:       "High-cost skill with low runtime usage",
				Description: name + " carries " + itoa(agg.maxTokens) + " tokens of static cost but loaded " + itoa(agg.totalLoads) + " times in the lookback window.",
				Affected:    []string{name},
			})
		}
		// CONFIGURED_NOT_LOADED — once per name, regardless of scope count.
		if !agg.anyVerified {
			out = append(out, Recommendation{
				ID:          "CONFIGURED_NOT_LOADED",
				Severity:    "info",
				Category:    "staleness",
				Title:       "Configured capability never observed at runtime",
				Description: name + " is configured but absent from runtime evidence — possibly dead config.",
				Affected:    []string{name},
			})
		}
	}

	// LOADED_NOT_CONFIGURED — every key in CapabilityLoads not in configuredNames.
	for name := range loadedNamesFromStats(snap) {
		if _, ok := configuredNames[name]; !ok {
			out = append(out, Recommendation{
				ID:          "LOADED_NOT_CONFIGURED",
				Severity:    "info",
				Category:    "runtime-drift",
				Title:       "Capability loaded at runtime but not in static inventory",
				Description: name + " was observed at runtime but isn't present in the configured set — likely an ambient install or stale scan cache.",
				Affected:    []string{name},
			})
		}
	}

	// HOOK_STORM — any hook with a load count above the threshold.
	for _, stat := range snap.HookStats {
		if stat.RuntimeLoads >= hookStormThreshold {
			out = append(out, Recommendation{
				ID:          "HOOK_STORM",
				Severity:    "warn",
				Category:    "cost",
				Title:       "Hook fires very frequently",
				Description: stat.CapabilityID + " executed " + itoa(stat.RuntimeLoads) + " times in the lookback window — investigate for duplicates or pruning.",
				Affected:    []string{stat.CapabilityID},
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func configuredCapabilityNames(inv *Inventory) map[string]struct{} {
	out := make(map[string]struct{}, len(inv.Skills)+len(inv.MCPServers))
	for _, s := range inv.Skills {
		out[s.Name] = struct{}{}
	}
	for _, p := range inv.Plugins {
		for _, s := range p.Skills {
			out[s.Name] = struct{}{}
		}
	}
	for _, srv := range inv.MCPServers {
		out[srv.Name] = struct{}{}
	}
	return out
}

// loadedNamesFromStats returns the union of capability IDs seen in skill
// stats with non-zero runtime loads. We intentionally exclude hook stats
// (their IDs are "Event|Command" composites, not capability names).
func loadedNamesFromStats(snap *AnalyticsSnapshot) map[string]struct{} {
	out := make(map[string]struct{})
	for _, stat := range snap.SkillStats {
		if stat.RuntimeLoads > 0 && !strings.Contains(stat.CapabilityID, "|") {
			out[stat.CapabilityID] = struct{}{}
		}
	}
	return out
}

func severityRank(s string) int {
	switch s {
	case "error":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}
