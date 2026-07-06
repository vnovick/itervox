package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/agent"
)

func TestReadAvailableModels_ReturnsEmptyForMissingBlock(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
agent:
  command: claude
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAvailableModels(wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}

func TestReadAvailableModels_ParsesExisting(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
agent:
  command: claude
  available_models:
    claude:
      - id: claude-sonnet-4-6
        label: Sonnet 4.6
      - id: claude-haiku-4-5
        label: Haiku 4.5
    codex:
      - id: gpt-5.3-codex
        label: GPT-5.3-Codex
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAvailableModels(wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["claude"]) != 2 {
		t.Errorf("expected 2 claude models; got %d", len(got["claude"]))
	}
	if len(got["codex"]) != 1 {
		t.Errorf("expected 1 codex model; got %d", len(got["codex"]))
	}
	if got["claude"][0].ID != "claude-sonnet-4-6" {
		t.Errorf("first claude id = %q", got["claude"][0].ID)
	}
}

func TestMergeAvailableModelsIntoWorkflow_AddsBlock(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
agent:
  command: claude
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	discovered := map[string][]agent.ModelOption{
		"claude": {{ID: "claude-sonnet-4-7", Label: "Sonnet 4.7"}},
	}
	merged, err := mergeAvailableModelsIntoWorkflow(wf, discovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged["claude"]) != 1 {
		t.Errorf("merged claude count = %d", len(merged["claude"]))
	}
	data, _ := os.ReadFile(wf)
	if !strings.Contains(string(data), "claude-sonnet-4-7") {
		t.Errorf("WORKFLOW.md missing new model id; got:\n%s", string(data))
	}
	if !strings.Contains(string(data), "body") {
		t.Errorf("body lost; got:\n%s", string(data))
	}
}

func TestMergeAvailableModelsIntoWorkflow_PreservesOtherBackends(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
agent:
  available_models:
    claude:
      - id: claude-haiku-4-5
        label: Haiku 4.5
    codex:
      - id: gpt-5.1-codex
        label: GPT-5.1-Codex
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Refresh only claude; codex must stay.
	discovered := map[string][]agent.ModelOption{
		"claude": {{ID: "claude-sonnet-4-7", Label: "Sonnet 4.7"}},
	}
	merged, err := mergeAvailableModelsIntoWorkflow(wf, discovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged["codex"]) != 1 || merged["codex"][0].ID != "gpt-5.1-codex" {
		t.Errorf("codex models not preserved; got %v", merged["codex"])
	}
	if len(merged["claude"]) != 1 || merged["claude"][0].ID != "claude-sonnet-4-7" {
		t.Errorf("claude models not refreshed; got %v", merged["claude"])
	}
}

func TestIsAcceptedModelBackend(t *testing.T) {
	for _, ok := range []string{"claude", "codex", "all"} {
		if !IsAcceptedModelBackend(ok) {
			t.Errorf("expected %q to be accepted", ok)
		}
	}
	if IsAcceptedModelBackend("gemini") {
		t.Error("unknown backend must be rejected")
	}
}
