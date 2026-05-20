package skills

import (
	"testing"
	"time"
)

func recIDs(recs []Recommendation) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func recContains(recs []Recommendation, id string) bool {
	for _, r := range recs {
		if r.ID == id {
			return true
		}
	}
	return false
}

func TestRecommendAnalytics_HighCostLowUsage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	stat := CapabilityStat{
		CapabilityID:    "expensive-skill",
		ApproxTokens:    5_000,
		RuntimeLoads:    1,
		Configured:      true,
		RuntimeVerified: true,
		LastSeenAt:      &now,
	}
	snap := &AnalyticsSnapshot{HasRuntimeEvidence: true, SkillStats: []CapabilityStat{stat}}
	inv := &Inventory{Skills: []Skill{{Name: "expensive-skill"}}}
	recs := RecommendAnalytics(snap, inv)
	if !recContains(recs, "HIGH_COST_LOW_USAGE") {
		t.Errorf("expected HIGH_COST_LOW_USAGE, got %v", recIDs(recs))
	}
}

func TestRecommendAnalytics_HookStorm(t *testing.T) {
	t.Parallel()
	stat := CapabilityStat{
		CapabilityID:    "PreToolUse|echo",
		RuntimeLoads:    100,
		Configured:      true,
		RuntimeVerified: true,
	}
	snap := &AnalyticsSnapshot{HasRuntimeEvidence: true, HookStats: []CapabilityStat{stat}}
	recs := RecommendAnalytics(snap, &Inventory{})
	if !recContains(recs, "HOOK_STORM") {
		t.Errorf("expected HOOK_STORM, got %v", recIDs(recs))
	}
}

func TestRecommendAnalytics_ConfiguredNotLoaded(t *testing.T) {
	t.Parallel()
	stat := CapabilityStat{
		CapabilityID:    "dead-skill",
		ApproxTokens:    100,
		Configured:      true,
		RuntimeVerified: false,
	}
	snap := &AnalyticsSnapshot{HasRuntimeEvidence: true, SkillStats: []CapabilityStat{stat}}
	inv := &Inventory{Skills: []Skill{{Name: "dead-skill"}}}
	recs := RecommendAnalytics(snap, inv)
	if !recContains(recs, "CONFIGURED_NOT_LOADED") {
		t.Errorf("expected CONFIGURED_NOT_LOADED, got %v", recIDs(recs))
	}
}

func TestRecommendAnalytics_LoadedNotConfigured(t *testing.T) {
	t.Parallel()
	now := time.Now()
	snap := &AnalyticsSnapshot{
		HasRuntimeEvidence: true,
		SkillStats: []CapabilityStat{
			{
				CapabilityID:    "ambient-skill",
				RuntimeLoads:    5,
				Configured:      false,
				RuntimeVerified: true,
				LastSeenAt:      &now,
			},
		},
	}
	inv := &Inventory{} // no static skills
	recs := RecommendAnalytics(snap, inv)
	if !recContains(recs, "LOADED_NOT_CONFIGURED") {
		t.Errorf("expected LOADED_NOT_CONFIGURED, got %v", recIDs(recs))
	}
}

func TestRecommendAnalytics_NilSafety(t *testing.T) {
	t.Parallel()
	if recs := RecommendAnalytics(nil, &Inventory{}); recs != nil {
		t.Errorf("expected nil for nil snapshot")
	}
	if recs := RecommendAnalytics(&AnalyticsSnapshot{}, nil); recs != nil {
		t.Errorf("expected nil for nil inventory")
	}
}

func TestRecommendAnalytics_SortedBySeverityThenID(t *testing.T) {
	t.Parallel()
	snap := &AnalyticsSnapshot{
		HasRuntimeEvidence: true,
		SkillStats: []CapabilityStat{
			{CapabilityID: "z", ApproxTokens: 5_000, RuntimeLoads: 0, Configured: true, RuntimeVerified: false},
			{CapabilityID: "a", ApproxTokens: 0, RuntimeLoads: 0, Configured: true, RuntimeVerified: false},
		},
	}
	inv := &Inventory{Skills: []Skill{{Name: "z"}, {Name: "a"}}}
	recs := RecommendAnalytics(snap, inv)
	// Expect warn (HIGH_COST_LOW_USAGE) before any info entries.
	if len(recs) < 2 || recs[0].Severity != "warn" {
		t.Errorf("expected first entry to be warn, got %+v", recs)
	}
}

func TestRecommendAnalytics_SuppressedWithoutRuntimeEvidence(t *testing.T) {
	t.Parallel()
	snap := &AnalyticsSnapshot{
		SkillStats: []CapabilityStat{{
			CapabilityID: "fresh-install-skill",
			ApproxTokens: 5000,
			Configured:   true,
		}},
	}
	inv := &Inventory{Skills: []Skill{{Name: "fresh-install-skill"}}}
	if recs := RecommendAnalytics(snap, inv); len(recs) != 0 {
		t.Fatalf("expected no runtime recommendations without evidence, got %+v", recs)
	}
}
