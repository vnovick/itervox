package skills

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
)

func TestEstimateProfileCost_FullInventory(t *testing.T) {
	t.Parallel()
	inv := &Inventory{
		Instructions: []InstructionDoc{{ApproxTokens: 100}, {ApproxTokens: 200}},
		Skills:       []Skill{{ApproxTokens: 50}, {ApproxTokens: 75}},
		Hooks:        []HookEntry{{ApproxTokens: 10}, {ApproxTokens: 20}},
		MCPServers:   []MCPServer{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Plugins: []Plugin{
			{Skills: []Skill{{ApproxTokens: 25}}, Hooks: []HookEntry{{ApproxTokens: 5}}},
		},
	}
	cost := estimateProfileCost("default", config.AgentProfile{}, inv, 4000)
	if cost.InstructionTokens != 300 {
		t.Errorf("InstructionTokens = %d; want 300", cost.InstructionTokens)
	}
	if cost.SkillTokens != 50+75+25 {
		t.Errorf("SkillTokens = %d; want 150", cost.SkillTokens)
	}
	if cost.HookTokens != 10+20+5 {
		t.Errorf("HookTokens = %d; want 35", cost.HookTokens)
	}
	if cost.MCPToolSchemaTokens != 3*800 {
		t.Errorf("MCPToolSchemaTokens = %d; want 2400", cost.MCPToolSchemaTokens)
	}
	if cost.WorkflowTemplateTokens != 1000 {
		t.Errorf("WorkflowTemplateTokens = %d; want 1000", cost.WorkflowTemplateTokens)
	}
	want := 300 + 150 + 35 + 2400 + 1000
	if cost.TotalApproxTokens != want {
		t.Errorf("TotalApproxTokens = %d; want %d", cost.TotalApproxTokens, want)
	}
}

func TestEstimateProfileCost_EmptyInventory(t *testing.T) {
	t.Parallel()
	cost := estimateProfileCost("p", config.AgentProfile{}, &Inventory{}, 0)
	if cost.TotalApproxTokens != 0 {
		t.Errorf("expected 0 total, got %d", cost.TotalApproxTokens)
	}
	if cost.ProfileName != "p" {
		t.Errorf("ProfileName = %q; want %q", cost.ProfileName, "p")
	}
}

func TestEstimateProfileCost_NilInventoryReturnsZero(t *testing.T) {
	t.Parallel()
	cost := estimateProfileCost("p", config.AgentProfile{}, nil, 1000)
	if cost.TotalApproxTokens != 0 {
		t.Errorf("expected 0 total for nil inventory, got %d", cost.TotalApproxTokens)
	}
}

func TestEstimateProfileCost_OnlyWorkflowTemplate(t *testing.T) {
	t.Parallel()
	cost := estimateProfileCost("p", config.AgentProfile{}, &Inventory{}, 4000)
	if cost.WorkflowTemplateTokens != 1000 {
		t.Errorf("WorkflowTemplateTokens = %d; want 1000", cost.WorkflowTemplateTokens)
	}
	if cost.TotalApproxTokens != 1000 {
		t.Errorf("TotalApproxTokens = %d; want 1000", cost.TotalApproxTokens)
	}
}
