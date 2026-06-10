package config

import (
	"slices"
	"strings"
	"testing"
)

func TestParseAgentProfiles_BuiltinFallbackUsesEmbeddedSoulAndInstructions(t *testing.T) {
	raw := map[string]any{
		"merge-bot": map[string]any{},
	}
	got, err := parseAgentProfiles(raw, LatestWorkflowSchemaVersion, "")
	if err != nil {
		t.Fatalf("parseAgentProfiles: %v", err)
	}
	p, ok := got["merge-bot"]
	if !ok {
		t.Fatalf("merge-bot profile missing from result: %+v", got)
	}
	if !strings.Contains(p.Soul, "merge-bot") {
		t.Errorf("expected embedded SOUL content; got %q", p.Soul)
	}
	if !strings.Contains(p.Instructions, "gh pr list") {
		t.Errorf("expected embedded INSTRUCTIONS content; got %q", p.Instructions)
	}
	if p.Command == "" {
		t.Error("expected DefaultCommand to be applied from built-in registry")
	}
	if p.Backend != "claude" {
		t.Errorf("expected default Backend = claude, got %q", p.Backend)
	}
	wantActions := []string{"comment", "comment_pr", "merge_pr", "move_state"}
	for _, w := range wantActions {
		if !slices.Contains(p.AllowedActions, w) {
			t.Errorf("AllowedActions %v missing %q", p.AllowedActions, w)
		}
	}
}

func TestParseAgentProfiles_BuiltinPreservesOperatorOverrides(t *testing.T) {
	raw := map[string]any{
		"merge-bot": map[string]any{
			"command":         "claude --model claude-sonnet-4-6",
			"allowed_actions": []any{"comment"},
		},
	}
	got, err := parseAgentProfiles(raw, LatestWorkflowSchemaVersion, "")
	if err != nil {
		t.Fatalf("parseAgentProfiles: %v", err)
	}
	p := got["merge-bot"]
	if p.Command != "claude --model claude-sonnet-4-6" {
		t.Errorf("operator command override lost; got %q", p.Command)
	}
	if len(p.AllowedActions) != 1 || p.AllowedActions[0] != "comment" {
		t.Errorf("operator allowed_actions override lost; got %v", p.AllowedActions)
	}
	if !strings.Contains(p.Soul, "merge-bot") {
		t.Errorf("expected embedded SOUL still applied via fallback; got %q", p.Soul)
	}
}

func TestParseAgentProfiles_BuiltinUnknownProfileStillRequiresSoulInstructions(t *testing.T) {
	raw := map[string]any{
		"custom-profile": map[string]any{
			"command": "claude",
		},
	}
	_, err := parseAgentProfiles(raw, LatestWorkflowSchemaVersion, "")
	if err == nil {
		t.Fatal("expected error for non-built-in profile missing soul_file/instructions_file")
	}
	if !strings.Contains(err.Error(), "soul_file and instructions_file") {
		t.Errorf("unexpected error: %v", err)
	}
}
