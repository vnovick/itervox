package skills

import (
	"sort"
	"time"
)

// BuildAnalytics combines a static Inventory with optional runtime evidence
// into the AnalyticsSnapshot the dashboard's Analytics tab consumes.
//
// When `runtime` is nil or empty, falls back to T-84 estimates and emits
// `RuntimeVerified=false` on every CapabilityStat. When runtime evidence is
// present, `Uses` / `RuntimeLoads` / `LastSeenAt` are populated.
//
// `profileNames` may be empty — in that case, no ProfileCost entries are
// produced (the inventory still surfaces correctly in the Analytics tab,
// just without per-profile attribution).
func BuildAnalytics(inv *Inventory, runtime *RuntimeEvidenceSnapshot, profileNames []string) *AnalyticsSnapshot {
	if inv == nil {
		return nil
	}
	out := &AnalyticsSnapshot{
		GeneratedAt:        time.Now(),
		HasRuntimeEvidence: runtimeHasEvidence(runtime),
	}

	out.SkillStats = computeSkillStats(inv, runtime)
	out.HookStats = computeHookStats(inv, runtime)
	out.ProfileCosts = computeProfileCosts(inv, runtime, profileNames)
	// Recommendations are produced by RecommendAnalytics (T-101) and folded
	// in by the caller — kept separate so Build vs. Recommend are unit-testable
	// in isolation.
	return out
}

func runtimeHasEvidence(runtime *RuntimeEvidenceSnapshot) bool {
	return runtime != nil && (runtime.HasRuntimeEvidence || len(runtime.SourceLogPaths) > 0)
}

// computeSkillStats blends static skill descriptors with runtime load counts.
func computeSkillStats(inv *Inventory, runtime *RuntimeEvidenceSnapshot) []CapabilityStat {
	allSkills := make([]Skill, 0, len(inv.Skills))
	allSkills = append(allSkills, inv.Skills...)
	for _, p := range inv.Plugins {
		allSkills = append(allSkills, p.Skills...)
	}
	stats := make([]CapabilityStat, 0, len(allSkills))
	for _, s := range allSkills {
		stat := CapabilityStat{
			CapabilityID: s.Name,
			ApproxTokens: s.ApproxTokens,
			Configured:   true,
		}
		if runtime != nil {
			loads := runtime.CapabilityLoads[s.Name]
			stat.RuntimeLoads = loads
			if loads > 0 {
				stat.RuntimeVerified = true
				now := runtime.GeneratedAt
				stat.LastSeenAt = &now
			}
			// Tool calls also count as "uses" if the skill name matches a
			// recorded tool — rare but real for skill-defined tools.
			stat.Uses = runtime.ToolCallCount[s.Name]
		}
		stats = append(stats, stat)
	}
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].CapabilityID < stats[j].CapabilityID })
	return stats
}

// computeHookStats aggregates static hook entries with runtime execution counts.
func computeHookStats(inv *Inventory, runtime *RuntimeEvidenceSnapshot) []CapabilityStat {
	allHooks := make([]HookEntry, 0, len(inv.Hooks))
	allHooks = append(allHooks, inv.Hooks...)
	for _, p := range inv.Plugins {
		allHooks = append(allHooks, p.Hooks...)
	}
	stats := make([]CapabilityStat, 0, len(allHooks))
	for _, h := range allHooks {
		key := h.Event
		if h.Command != "" {
			key = h.Event + "|" + h.Command
		}
		stat := CapabilityStat{
			CapabilityID: key,
			ApproxTokens: h.ApproxTokens,
			Configured:   true,
		}
		if runtime != nil {
			runs := runtime.HookExecutionCount[key]
			stat.RuntimeLoads = runs
			stat.Uses = runs
			if runs > 0 {
				stat.RuntimeVerified = true
				now := runtime.GeneratedAt
				stat.LastSeenAt = &now
			}
		}
		stats = append(stats, stat)
	}
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].CapabilityID < stats[j].CapabilityID })
	return stats
}

// computeProfileCosts refines the T-84 ProfileCost with runtime-derived MCP
// tool counts. When runtime is nil, falls back to the 800-tokens-per-server
// heuristic.
func computeProfileCosts(inv *Inventory, runtime *RuntimeEvidenceSnapshot, profileNames []string) []ProfileCost {
	if len(profileNames) == 0 {
		return nil
	}
	instructionTokens := 0
	for _, d := range inv.Instructions {
		instructionTokens += d.ApproxTokens
	}
	skillTokens := 0
	for _, s := range inv.Skills {
		skillTokens += s.ApproxTokens
	}
	for _, p := range inv.Plugins {
		for _, s := range p.Skills {
			skillTokens += s.ApproxTokens
		}
	}
	hookTokens := 0
	for _, h := range inv.Hooks {
		hookTokens += h.ApproxTokens
	}
	for _, p := range inv.Plugins {
		for _, h := range p.Hooks {
			hookTokens += h.ApproxTokens
		}
	}

	// MCP token count: prefer runtime-observed unique tools, fall back to
	// 800 × server count when runtime is empty.
	var mcpTokens int
	if runtime != nil {
		uniqueMCPTools := 0
		for name := range runtime.CapabilityLoads {
			// Treat any capability load that matches an MCP server name as
			// MCP-related. This is approximate — if Phase-3 adds tool-name
			// metadata to MCP entries, we can be more precise.
			for _, srv := range inv.MCPServers {
				if name == srv.Name {
					uniqueMCPTools++
					break
				}
			}
		}
		if uniqueMCPTools > 0 {
			// 400 tokens-per-tool when we have runtime evidence (more accurate
			// than the static 800-per-server fallback).
			mcpTokens = uniqueMCPTools * 400
		}
	}
	if mcpTokens == 0 {
		mcpTokens = len(inv.MCPServers) * 800
	}

	costs := make([]ProfileCost, 0, len(profileNames))
	for _, name := range profileNames {
		c := ProfileCost{
			ProfileName:         name,
			InstructionTokens:   instructionTokens,
			SkillTokens:         skillTokens,
			HookTokens:          hookTokens,
			MCPToolSchemaTokens: mcpTokens,
		}
		c.TotalApproxTokens = c.InstructionTokens + c.SkillTokens + c.HookTokens + c.MCPToolSchemaTokens
		costs = append(costs, c)
	}
	sort.SliceStable(costs, func(i, j int) bool { return costs[i].ProfileName < costs[j].ProfileName })
	return costs
}
