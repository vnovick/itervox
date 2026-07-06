package skills

import (
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/config"
)

func issueIDs(issues []InventoryIssue) []string {
	out := make([]string, len(issues))
	for i, iss := range issues {
		out[i] = iss.ID
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- DUPLICATE_SKILL ---

func TestAnalyze_DuplicateSkill_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Skills: []Skill{
		{Name: "shared-skill", Source: "project"},
		{Name: "shared-skill", Source: "user"},
	}}
	issues := Analyze(inv, AnalyzeInputs{})
	if !contains(issueIDs(issues), "DUPLICATE_SKILL") {
		t.Errorf("expected DUPLICATE_SKILL; got %v", issueIDs(issues))
	}
}

func TestAnalyze_DuplicateSkill_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Skills: []Skill{
		{Name: "alpha", Source: "project"},
		{Name: "beta", Source: "user"},
	}}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "DUPLICATE_SKILL") {
		t.Errorf("did not expect DUPLICATE_SKILL")
	}
}

// --- DUPLICATE_MCP ---

func TestAnalyze_DuplicateMCP_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{MCPServers: []MCPServer{
		{Name: "ctx7", Command: "npx", Source: "project-settings"},
		{Name: "ctx7-dup", Command: "npx", Source: "user-settings"},
	}}
	issues := Analyze(inv, AnalyzeInputs{})
	if !contains(issueIDs(issues), "DUPLICATE_MCP") {
		t.Errorf("expected DUPLICATE_MCP, got %v", issueIDs(issues))
	}
	for _, iss := range issues {
		if iss.ID == "DUPLICATE_MCP" {
			if iss.Fix != nil {
				t.Errorf("DUPLICATE_MCP must stay advisory; got fix %+v", iss.Fix)
			}
		}
	}
}

func TestAnalyze_DuplicateMCP_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{MCPServers: []MCPServer{
		{Name: "a", Command: "npx"},
		{Name: "b", URL: "https://example.com"},
	}}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "DUPLICATE_MCP") {
		t.Errorf("did not expect DUPLICATE_MCP")
	}
}

// --- UNUSED_PROFILE ---

func TestAnalyze_UnusedProfile_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{}
	in := AnalyzeInputs{
		Profiles: map[string]config.AgentProfile{
			"never-used": {Command: "claude"},
			"used":       {Command: "claude"},
		},
		RecentlyActiveProfiles: map[string]struct{}{"used": {}},
	}
	if !contains(issueIDs(Analyze(inv, in)), "UNUSED_PROFILE") {
		t.Errorf("expected UNUSED_PROFILE for never-used")
	}
}

func TestAnalyze_UnusedProfile_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{}
	in := AnalyzeInputs{
		Profiles:               map[string]config.AgentProfile{"a": {}, "b": {}},
		RecentlyActiveProfiles: map[string]struct{}{"a": {}, "b": {}},
	}
	if contains(issueIDs(Analyze(inv, in)), "UNUSED_PROFILE") {
		t.Errorf("did not expect UNUSED_PROFILE")
	}
}

func TestAnalyze_UnusedProfile_SkipsDisabledProfiles(t *testing.T) {
	t.Parallel()
	disabled := false
	inv := &Inventory{}
	in := AnalyzeInputs{
		Profiles: map[string]config.AgentProfile{
			"disabled": {Enabled: &disabled},
		},
		RecentlyActiveProfiles: map[string]struct{}{},
	}
	if contains(issueIDs(Analyze(inv, in)), "UNUSED_PROFILE") {
		t.Errorf("did not expect UNUSED_PROFILE for disabled profile")
	}
}

// --- BLOATED_PROFILE ---

func TestAnalyze_BloatedProfile_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Skills: make([]Skill, 20)}
	for i := range inv.Skills {
		inv.Skills[i] = Skill{Name: "skill-" + itoa(i)}
	}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"x": {}}}
	if !contains(issueIDs(Analyze(inv, in)), "BLOATED_PROFILE") {
		t.Errorf("expected BLOATED_PROFILE")
	}
}

func TestAnalyze_BloatedProfile_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Skills: []Skill{{Name: "alpha"}}}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"x": {}}}
	if contains(issueIDs(Analyze(inv, in)), "BLOATED_PROFILE") {
		t.Errorf("did not expect BLOATED_PROFILE for tiny inventory")
	}
}

// --- LARGE_CONTEXT ---

func TestAnalyze_LargeContext_Positive(t *testing.T) {
	t.Parallel()
	// 70 instruction docs of 1000 tokens each = 70k > default 50k threshold.
	docs := make([]InstructionDoc, 70)
	for i := range docs {
		docs[i] = InstructionDoc{ApproxTokens: 1000}
	}
	inv := &Inventory{Instructions: docs}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"big": {}}}
	if !contains(issueIDs(Analyze(inv, in)), "LARGE_CONTEXT") {
		t.Errorf("expected LARGE_CONTEXT")
	}
}

func TestAnalyze_LargeContext_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Instructions: []InstructionDoc{{ApproxTokens: 100}}}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"x": {}}}
	if contains(issueIDs(Analyze(inv, in)), "LARGE_CONTEXT") {
		t.Errorf("did not expect LARGE_CONTEXT")
	}
}

// --- STALE_SCHEDULE ---

func TestAnalyze_StaleSchedule_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Schedules: []ScheduleConfig{{Name: "ghost", Cron: "0 3 * * *"}}}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"real": {}}}
	if !contains(issueIDs(Analyze(inv, in)), "STALE_SCHEDULE") {
		t.Errorf("expected STALE_SCHEDULE")
	}
}

func TestAnalyze_StaleSchedule_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Schedules: []ScheduleConfig{{Name: "real", Cron: "0 3 * * *"}}}
	in := AnalyzeInputs{Profiles: map[string]config.AgentProfile{"real": {}}}
	if contains(issueIDs(Analyze(inv, in)), "STALE_SCHEDULE") {
		t.Errorf("did not expect STALE_SCHEDULE for valid reference")
	}
}

// --- INSTRUCTION_SHADOWING ---

func TestAnalyze_InstructionShadowing_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Instructions: []InstructionDoc{
		{Name: "CLAUDE.md", Scope: "project", FilePath: "/proj/CLAUDE.md"},
		{Name: "CLAUDE.md", Scope: "user", FilePath: "/home/.claude/CLAUDE.md"},
	}}
	if !contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "INSTRUCTION_SHADOWING") {
		t.Errorf("expected INSTRUCTION_SHADOWING")
	}
}

func TestAnalyze_InstructionShadowing_Negative_SameScope(t *testing.T) {
	t.Parallel()
	inv := &Inventory{Instructions: []InstructionDoc{
		{Name: "CLAUDE.md", Scope: "project", FilePath: "/proj/CLAUDE.md"},
		{Name: "CLAUDE.md", Scope: "project", FilePath: "/proj/internal/CLAUDE.md"},
	}}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "INSTRUCTION_SHADOWING") {
		t.Errorf("nested project CLAUDE.md should not trigger shadowing")
	}
}

// --- ORPHAN_MCP ---

func TestAnalyze_OrphanMCP_Positive(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		MCPServers: []MCPServer{{Name: "mystery-server", Command: "npx"}},
		Skills:     []Skill{{Name: "alpha", Description: "does alpha things"}},
	}
	if !contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "ORPHAN_MCP") {
		t.Errorf("expected ORPHAN_MCP")
	}
}

func TestAnalyze_OrphanMCP_Negative(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		MCPServers: []MCPServer{{Name: "mystery-server", Command: "npx"}},
		Skills:     []Skill{{Name: "alpha", Description: "uses mystery-server tools"}},
	}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "ORPHAN_MCP") {
		t.Errorf("did not expect ORPHAN_MCP when skill mentions server")
	}
}

func TestAnalyze_OrphanMCP_NegativeWhenSkillBodyMentionsServer(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		MCPServers: []MCPServer{{Name: "mystery-server", Command: "npx"}},
		Skills: []Skill{{
			Name:     "alpha",
			bodyText: "Use mystery-server whenever you need repository context.",
		}},
	}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "ORPHAN_MCP") {
		t.Errorf("did not expect ORPHAN_MCP when skill body mentions server")
	}
}

func TestAnalyze_OrphanMCP_NegativeWhenPluginSkillBodyMentionsServer(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		MCPServers: []MCPServer{{Name: "mystery-server", Command: "npx"}},
		Plugins: []Plugin{{
			Name: "plugin",
			Skills: []Skill{{
				Name:     "plugin-skill",
				bodyText: "Use mystery-server for repository context.",
			}},
		}},
	}
	if contains(issueIDs(Analyze(inv, AnalyzeInputs{})), "ORPHAN_MCP") {
		t.Errorf("did not expect ORPHAN_MCP when plugin skill body mentions server")
	}
}

// --- Order determinism ---

func TestAnalyze_OrderIsDeterministic(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		Skills:     []Skill{{Name: "dup", Source: "a"}, {Name: "dup", Source: "b"}},
		MCPServers: []MCPServer{{Name: "m1", Command: "x"}, {Name: "m2", Command: "x"}},
	}
	a := Analyze(inv, AnalyzeInputs{})
	b := Analyze(inv, AnalyzeInputs{})
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("non-deterministic order at %d: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
	// Also confirm sorted-by-ID.
	for i := 1; i < len(a); i++ {
		if strings.Compare(a[i-1].ID, a[i].ID) > 0 {
			t.Errorf("issues not sorted by ID at index %d", i)
		}
	}
}
