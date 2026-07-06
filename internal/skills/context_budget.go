package skills

import "github.com/vnovick/itervox/internal/config"

// estimateProfileCost is the per-design-draft token-budget heuristic. All
// numbers are LABELLED APPROXIMATE — they're meant to support "is this
// profile too big?" intuition, not to claim per-token accuracy.
//
// Heuristics (from the design draft "Per-skill cost model"):
//   - InstructionTokens: sum of every CLAUDE.md / AGENTS.md token estimate
//     applicable to this profile. Without per-profile instruction filtering
//     in the current config schema, we sum all instructions.
//   - SkillTokens: every Claude/Codex skill in scope (their description is
//     loaded into context to drive AI invocation).
//   - HookTokens: sum of HookEntry.ApproxTokens for every configured hook
//     (already includes the ×2 shell-context multiplier from T-81).
//   - MCPToolSchemaTokens: 800 × number of configured MCP servers — average
//     observed cost per server's tool catalog. Refined in Phase 2 when
//     runtime evidence is available.
//   - WorkflowTemplateTokens: provided by the caller as `workflowBodyBytes`,
//     normalized via the standard /4 heuristic.
//
// Edge case: a profile with no instructions / skills / hooks / MCP returns
// a ProfileCost with only the workflow template body counted.
func estimateProfileCost(profileName string, _ config.AgentProfile, inv *Inventory, workflowBodyBytes int) ProfileCost {
	if inv == nil {
		return ProfileCost{ProfileName: profileName}
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

	mcpTokens := len(inv.MCPServers) * 800

	workflowTemplateTokens := workflowBodyBytes / 4

	cost := ProfileCost{
		ProfileName:            profileName,
		InstructionTokens:      instructionTokens,
		SkillTokens:            skillTokens,
		HookTokens:             hookTokens,
		MCPToolSchemaTokens:    mcpTokens,
		WorkflowTemplateTokens: workflowTemplateTokens,
	}
	cost.TotalApproxTokens = cost.InstructionTokens + cost.SkillTokens + cost.HookTokens + cost.MCPToolSchemaTokens + cost.WorkflowTemplateTokens
	return cost
}
