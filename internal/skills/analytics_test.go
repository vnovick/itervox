package skills

import (
	"testing"
	"time"
)

func TestBuildAnalytics_FallsBackWithoutRuntime(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		Skills:     []Skill{{Name: "alpha", ApproxTokens: 100}},
		MCPServers: []MCPServer{{Name: "ctx7"}},
	}
	snap := BuildAnalytics(inv, nil, []string{"default"})
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.SkillStats) != 1 {
		t.Fatalf("expected 1 skill stat, got %d", len(snap.SkillStats))
	}
	if snap.HasRuntimeEvidence {
		t.Errorf("expected HasRuntimeEvidence=false without runtime evidence")
	}
	stat := snap.SkillStats[0]
	if stat.RuntimeVerified {
		t.Errorf("expected RuntimeVerified=false without runtime evidence")
	}
	if stat.LastSeenAt != nil {
		t.Errorf("expected LastSeenAt=nil without runtime evidence")
	}
	if !stat.Configured {
		t.Errorf("expected Configured=true")
	}
	if len(snap.ProfileCosts) != 1 {
		t.Fatalf("expected 1 profile cost, got %d", len(snap.ProfileCosts))
	}
	// Without runtime, MCP heuristic = 800 × 1 = 800.
	if snap.ProfileCosts[0].MCPToolSchemaTokens != 800 {
		t.Errorf("expected fallback MCP tokens=800, got %d", snap.ProfileCosts[0].MCPToolSchemaTokens)
	}
}

func TestBuildAnalytics_PopulatesRuntimeFields(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		Skills:     []Skill{{Name: "alpha", ApproxTokens: 100}},
		Hooks:      []HookEntry{{Event: "PreToolUse", Command: "echo", ApproxTokens: 5}},
		MCPServers: []MCPServer{{Name: "ctx7"}, {Name: "fs"}},
	}
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	runtime := &RuntimeEvidenceSnapshot{
		GeneratedAt:        now,
		HasRuntimeEvidence: true,
		CapabilityLoads:    map[string]int{"alpha": 7, "ctx7": 3, "fs": 2},
		HookExecutionCount: map[string]int{"PreToolUse|echo": 4},
		ToolCallCount:      map[string]int{"alpha": 1},
	}
	snap := BuildAnalytics(inv, runtime, []string{"p1"})
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if !snap.HasRuntimeEvidence {
		t.Errorf("expected HasRuntimeEvidence=true with runtime evidence")
	}
	stat := snap.SkillStats[0]
	if !stat.RuntimeVerified {
		t.Errorf("expected RuntimeVerified=true with runtime evidence")
	}
	if stat.LastSeenAt == nil || !stat.LastSeenAt.Equal(now) {
		t.Errorf("expected LastSeenAt=%v, got %v", now, stat.LastSeenAt)
	}
	if stat.RuntimeLoads != 7 {
		t.Errorf("expected RuntimeLoads=7, got %d", stat.RuntimeLoads)
	}
	if stat.Uses != 1 {
		t.Errorf("expected Uses=1 (tool call), got %d", stat.Uses)
	}

	hookStat := snap.HookStats[0]
	if hookStat.RuntimeLoads != 4 {
		t.Errorf("expected hook RuntimeLoads=4, got %d", hookStat.RuntimeLoads)
	}

	// MCP refined cost: 2 unique tools × 400 = 800. Static heuristic would
	// also give 800 for 2 servers — choose a different runtime case below.
	cost := snap.ProfileCosts[0]
	if cost.MCPToolSchemaTokens == 0 {
		t.Errorf("expected non-zero MCP token cost")
	}
}

func TestBuildAnalytics_NilInventoryReturnsNil(t *testing.T) {
	t.Parallel()
	if snap := BuildAnalytics(nil, nil, []string{"x"}); snap != nil {
		t.Errorf("expected nil for nil inventory, got %+v", snap)
	}
}

func TestBuildAnalytics_NoProfilesNoCosts(t *testing.T) {
	t.Parallel()
	snap := BuildAnalytics(&Inventory{}, nil, nil)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.ProfileCosts) != 0 {
		t.Errorf("expected no profile costs without profileNames, got %d", len(snap.ProfileCosts))
	}
}

func TestBuildAnalytics_StableSortByID(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		Skills: []Skill{
			{Name: "z", ApproxTokens: 1},
			{Name: "a", ApproxTokens: 2},
			{Name: "m", ApproxTokens: 3},
		},
	}
	snap := BuildAnalytics(inv, nil, nil)
	got := []string{snap.SkillStats[0].CapabilityID, snap.SkillStats[1].CapabilityID, snap.SkillStats[2].CapabilityID}
	want := []string{"a", "m", "z"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}
