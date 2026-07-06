package skills

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// AnalyzeInputs is the structured input for Analyze. Decouples the skills
// package from orchestrator/server types — the caller projects whatever it
// needs to feed in.
type AnalyzeInputs struct {
	Profiles map[string]config.AgentProfile
	// RecentlyActiveProfiles is the set of profile names seen in the last N
	// completed runs. Used to detect UNUSED_PROFILE.
	RecentlyActiveProfiles map[string]struct{}
	// LargeContextThreshold is the per-profile token ceiling above which the
	// LARGE_CONTEXT issue fires. Default 50_000 if zero.
	LargeContextThreshold int
}

// Analyze runs every static-analysis rule against the inventory and returns
// the resulting issues. The order is deterministic (sorted by ID) so the
// dashboard can stably diff successive scans.
//
// Phase 1 ships a focused subset of the design draft's 16 rules:
//
//   - DUPLICATE_SKILL          — same skill name across sources
//   - DUPLICATE_MCP            — same command/URL across MCP entries
//   - UNUSED_PROFILE           — profile defined but not in recently-active set
//   - BLOATED_PROFILE          — > 20 MCP tools OR > 15 skills attributed
//   - LARGE_CONTEXT            — estimated profile cost > LargeContextThreshold
//   - STALE_SCHEDULE           — reserved for future schedule inventory inputs
//   - INSTRUCTION_SHADOWING    — same instruction filename across scopes
//   - ORPHAN_MCP               — MCP server name never appears in skill names/descriptions/bodies
//
// The remaining design-draft rules (DUPLICATE_CROSS_RUNTIME_SKILL,
// MISSING_SKILL_REF, HOOK_CONFLICT, REDUNDANT_HOOK, MODEL_MISMATCH,
// CAPABILITY_OVERLAP, MISSING_ACTION) are tracked in deferred_290426.md and
// land in a follow-up — each requires either heavier text similarity or a
// specific feature (teams mode) outside the Phase-1 scope.
func Analyze(inv *Inventory, in AnalyzeInputs) []InventoryIssue {
	if inv == nil {
		return nil
	}
	if in.LargeContextThreshold <= 0 {
		in.LargeContextThreshold = 50_000
	}

	var issues []InventoryIssue
	issues = append(issues, detectDuplicateSkill(inv)...)
	issues = append(issues, detectDuplicateMCP(inv)...)
	issues = append(issues, detectUnusedProfile(inv, in)...)
	issues = append(issues, detectBloatedProfile(inv, in)...)
	issues = append(issues, detectLargeContext(inv, in)...)
	issues = append(issues, detectStaleSchedule(inv, in)...)
	issues = append(issues, detectInstructionShadowing(inv)...)
	issues = append(issues, detectOrphanMCP(inv)...)

	sort.SliceStable(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	return issues
}

// --- Rule implementations ---

func detectDuplicateSkill(inv *Inventory) []InventoryIssue {
	byName := make(map[string][]string, len(inv.Skills))
	for _, s := range inv.Skills {
		byName[s.Name] = append(byName[s.Name], s.Source)
	}
	var out []InventoryIssue
	for name, sources := range byName {
		if len(sources) <= 1 {
			continue
		}
		sort.Strings(sources)
		out = append(out, InventoryIssue{
			ID:          "DUPLICATE_SKILL",
			Severity:    "info",
			Title:       "Same skill name in multiple scopes",
			Description: "Skill " + name + " appears in: " + strings.Join(sources, ", "),
			Affected:    []string{name},
		})
	}
	return out
}

func detectDuplicateMCP(inv *Inventory) []InventoryIssue {
	type key struct{ command, url string }
	byKey := make(map[key][]string)
	for _, s := range inv.MCPServers {
		k := key{command: s.Command, url: s.URL}
		// Skip empty keys — they collide for any unconfigured remote.
		if k.command == "" && k.url == "" {
			continue
		}
		byKey[k] = append(byKey[k], s.Name+"@"+s.Source)
	}
	var out []InventoryIssue
	for k, names := range byKey {
		if len(names) <= 1 {
			continue
		}
		sort.Strings(names)
		desc := "MCP server " + k.command + k.url + " is registered " + strings.Join(names, ", ")
		out = append(out, InventoryIssue{
			ID:          "DUPLICATE_MCP",
			Severity:    "warn",
			Title:       "Duplicate MCP server registration",
			Description: desc,
			Affected:    names,
		})
	}
	return out
}

func detectUnusedProfile(_ *Inventory, in AnalyzeInputs) []InventoryIssue {
	if len(in.Profiles) == 0 || in.RecentlyActiveProfiles == nil {
		return nil
	}
	var out []InventoryIssue
	for name, profile := range in.Profiles {
		if !config.ProfileEnabled(profile) {
			continue
		}
		if _, seen := in.RecentlyActiveProfiles[name]; seen {
			continue
		}
		out = append(out, InventoryIssue{
			ID:          "UNUSED_PROFILE",
			Severity:    "info",
			Title:       "Profile not seen in recent runs",
			Description: "Profile " + name + " is configured but absent from the recent-history sample.",
			Affected:    []string{name},
			Fix: &Fix{
				Label:       "Disable profile",
				Action:      "edit-yaml",
				Target:      "agent.profiles." + name + ".enabled",
				Destructive: false,
			},
		})
	}
	return out
}

func detectBloatedProfile(inv *Inventory, in AnalyzeInputs) []InventoryIssue {
	const (
		maxMCPTools      = 20
		maxSkillsCarried = 15
	)
	// Phase 1: every profile inherits the global skill + MCP set, so the
	// counts are inventory-wide. Refines in Phase 2.
	mcpCount := len(inv.MCPServers)
	skillCount := len(inv.Skills)
	for _, p := range inv.Plugins {
		skillCount += len(p.Skills)
	}
	if mcpCount <= maxMCPTools && skillCount <= maxSkillsCarried {
		return nil
	}
	var affected []string
	for name := range in.Profiles {
		affected = append(affected, name)
	}
	sort.Strings(affected)
	desc := "Inventory carries " + itoa(skillCount) + " skills and " + itoa(mcpCount) + " MCP servers — every profile inherits this surface area."
	return []InventoryIssue{{
		ID:          "BLOATED_PROFILE",
		Severity:    "warn",
		Title:       "Profiles carry a heavy capability surface",
		Description: desc,
		Affected:    affected,
	}}
}

func detectLargeContext(inv *Inventory, in AnalyzeInputs) []InventoryIssue {
	var out []InventoryIssue
	for name, profile := range in.Profiles {
		cost := estimateProfileCost(name, profile, inv, 0)
		if cost.TotalApproxTokens <= in.LargeContextThreshold {
			continue
		}
		out = append(out, InventoryIssue{
			ID:          "LARGE_CONTEXT",
			Severity:    "warn",
			Title:       "Estimated profile context cost exceeds threshold",
			Description: "Profile " + name + " estimated at " + itoa(cost.TotalApproxTokens) + " tokens (threshold " + itoa(in.LargeContextThreshold) + ").",
			Affected:    []string{name},
		})
	}
	return out
}

func detectStaleSchedule(inv *Inventory, in AnalyzeInputs) []InventoryIssue {
	if len(inv.Schedules) == 0 {
		return nil
	}
	var out []InventoryIssue
	for _, sched := range inv.Schedules {
		if sched.Name == "" {
			continue
		}
		// Schedules in the inventory are at the inventory level; we treat
		// `Name` as the profile reference for the Phase-1 check.
		if _, ok := in.Profiles[sched.Name]; ok {
			continue
		}
		out = append(out, InventoryIssue{
			ID:          "STALE_SCHEDULE",
			Severity:    "warn",
			Title:       "Schedule references unknown profile",
			Description: "Schedule " + sched.Cron + " targets profile " + sched.Name + " which is not defined.",
			Affected:    []string{sched.Name},
		})
	}
	return out
}

func detectInstructionShadowing(inv *Inventory) []InventoryIssue {
	byBaseName := make(map[string][]InstructionDoc)
	for _, d := range inv.Instructions {
		base := filepath.Base(d.FilePath)
		byBaseName[base] = append(byBaseName[base], d)
	}
	var out []InventoryIssue
	for base, docs := range byBaseName {
		if len(docs) <= 1 {
			continue
		}
		// Only flag if the same filename appears across DIFFERENT scopes.
		scopes := make(map[string]struct{}, len(docs))
		for _, d := range docs {
			scopes[d.Scope] = struct{}{}
		}
		if len(scopes) <= 1 {
			continue
		}
		paths := make([]string, 0, len(docs))
		for _, d := range docs {
			paths = append(paths, d.FilePath)
		}
		sort.Strings(paths)
		out = append(out, InventoryIssue{
			ID:          "INSTRUCTION_SHADOWING",
			Severity:    "info",
			Title:       "Same instruction filename across scopes",
			Description: base + " appears in " + itoa(len(docs)) + " scopes; project-level wins.",
			Affected:    paths,
		})
	}
	return out
}

func detectOrphanMCP(inv *Inventory) []InventoryIssue {
	if len(inv.MCPServers) == 0 {
		return nil
	}
	// Build a corpus of skill names, descriptions, and bodies; an MCP server
	// name not found is "orphan" (heuristic — MCP tools are usually mentioned
	// by name in skills that consume them).
	var corpus strings.Builder
	for _, s := range inv.Skills {
		corpus.WriteString(s.Name)
		corpus.WriteString(" ")
		corpus.WriteString(s.Description)
		corpus.WriteString(" ")
		corpus.WriteString(s.bodyText)
		corpus.WriteString(" ")
	}
	for _, p := range inv.Plugins {
		for _, s := range p.Skills {
			corpus.WriteString(s.Name)
			corpus.WriteString(" ")
			corpus.WriteString(s.Description)
			corpus.WriteString(" ")
			corpus.WriteString(s.bodyText)
			corpus.WriteString(" ")
		}
	}
	body := strings.ToLower(corpus.String())
	if body == "" {
		return nil
	}
	var out []InventoryIssue
	for _, srv := range inv.MCPServers {
		needle := strings.ToLower(srv.Name)
		if needle == "" || strings.Contains(body, needle) {
			continue
		}
		out = append(out, InventoryIssue{
			ID:          "ORPHAN_MCP",
			Severity:    "info",
			Title:       "MCP server not referenced by any skill",
			Description: "MCP server " + srv.Name + " is configured but never mentioned in any skill name, description, or body.",
			Affected:    []string{srv.Name},
		})
	}
	return out
}

// itoa is a tiny strconv-free shim so the package keeps a minimal import set.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
