package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/agent"
)

// TestE2E_ModelsRefreshAgainstFakeAPIs is the full end-to-end test of the
// `itervox models refresh` pipeline:
//
//  1. Spin up two httptest.Server instances that emulate Anthropic
//     /v1/models and OpenAI /v1/models.
//  2. Point the discovery helpers at them via env overrides.
//  3. Drive the SAME function the `itervox models refresh` CLI subcommand
//     invokes (mergeAvailableModelsIntoWorkflow + ListClaudeModels +
//     ListCodexModels).
//  4. Read the resulting WORKFLOW.md and assert the discovered IDs are
//     present.
//
// This catches integration breaks that the unit tests of either layer
// (the discovery client OR the YAML mutator) would miss in isolation —
// e.g. a regression in the env-override seam, a change in the response
// shape that the mutator no longer accepts, or a YAML serialisation
// that drops one of the discovered models.
func TestE2E_ModelsRefreshAgainstFakeAPIs(t *testing.T) {
	// Fake Anthropic: returns one new sonnet + one opus that don't exist
	// in DefaultClaudeModels so we can distinguish API hits from fallback.
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-5-0-future", "display_name": "Sonnet 5.0 (e2e test)"},
				{"id": "claude-opus-5-0-future",   "display_name": "Opus 5.0 (e2e test)"}
			]
		}`))
	}))
	defer anthropic.Close()

	// Fake OpenAI: same idea — IDs that aren't in DefaultCodexModels.
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-5.5-codex-future"},
				{"id": "gpt-5.6-codex-future"}
			]
		}`))
	}))
	defer openai.Close()

	t.Setenv("ANTHROPIC_API_KEY", "e2e-test-key")
	t.Setenv("OPENAI_API_KEY", "e2e-test-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", anthropic.URL)
	t.Setenv("ITERVOX_OPENAI_API_BASE", openai.URL)

	// Seed a WORKFLOW.md with a legacy available_models block so we can
	// also verify the "refreshed backends replace; other backends keep"
	// invariant: we'll refresh both backends here, so both must be
	// replaced.
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	seed := `---
itervox_schema_version: 2
agent:
  command: claude
  available_models:
    claude:
      - id: claude-sonnet-4-6
        label: Sonnet 4.6
    codex:
      - id: gpt-5.1-codex
        label: GPT-5.1-Codex
server:
  port: 0
---

# Prompt body
`
	if err := os.WriteFile(wf, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	// Exercise the same pipeline the CLI subcommand uses.
	discovered := map[string][]agent.ModelOption{
		"claude": agent.ListClaudeModels(),
		"codex":  agent.ListCodexModels(),
	}
	if len(discovered["claude"]) == 0 || len(discovered["codex"]) == 0 {
		t.Fatalf("discovery returned empty: %+v", discovered)
	}
	merged, err := mergeAvailableModelsIntoWorkflow(wf, discovered)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Assertions on the in-memory merge result.
	gotClaude := idSet(merged["claude"])
	if !gotClaude["claude-sonnet-5-0-future"] || !gotClaude["claude-opus-5-0-future"] {
		t.Errorf("merged claude missing fake-API IDs; got %v", merged["claude"])
	}
	if gotClaude["claude-sonnet-4-6"] {
		t.Errorf("legacy claude ID should have been replaced; got %v", merged["claude"])
	}
	gotCodex := idSet(merged["codex"])
	if !gotCodex["gpt-5.5-codex-future"] || !gotCodex["gpt-5.6-codex-future"] {
		t.Errorf("merged codex missing fake-API IDs; got %v", merged["codex"])
	}
	if gotCodex["gpt-5.1-codex"] {
		t.Errorf("legacy codex ID should have been replaced; got %v", merged["codex"])
	}

	// Assertions on the on-disk WORKFLOW.md (the operator's source of truth).
	data, err := os.ReadFile(wf)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"claude-sonnet-5-0-future",
		"claude-opus-5-0-future",
		"gpt-5.5-codex-future",
		"gpt-5.6-codex-future",
		"# Prompt body", // body survived
	} {
		if !strings.Contains(content, want) {
			t.Errorf("WORKFLOW.md missing %q; got:\n%s", want, content)
		}
	}
	for _, gone := range []string{
		"claude-sonnet-4-6",
		"gpt-5.1-codex",
	} {
		if strings.Contains(content, gone) {
			t.Errorf("WORKFLOW.md still contains replaced %q; got:\n%s", gone, content)
		}
	}
}

// TestE2E_ModelsRefreshClaudeOnlyPreservesCodex — same setup but only the
// claude backend is refreshed; codex entries must survive. This is the
// per-backend semantic the CLI documents.
func TestE2E_ModelsRefreshClaudeOnlyPreservesCodex(t *testing.T) {
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-99","display_name":"99"}]}`))
	}))
	defer anthropic.Close()
	t.Setenv("ANTHROPIC_API_KEY", "e2e-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", anthropic.URL)
	// OPENAI key intentionally unset — the test does not call ListCodexModels.

	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	seed := `---
agent:
  available_models:
    claude:
      - id: claude-old-id
        label: Old
    codex:
      - id: gpt-5.1-codex
        label: GPT-5.1-Codex
---

body
`
	if err := os.WriteFile(wf, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// Refresh ONLY claude.
	discovered := map[string][]agent.ModelOption{
		"claude": agent.ListClaudeModels(),
	}
	merged, err := mergeAvailableModelsIntoWorkflow(wf, discovered)
	if err != nil {
		t.Fatal(err)
	}
	if !idSet(merged["claude"])["claude-sonnet-99"] {
		t.Errorf("claude not refreshed; got %v", merged["claude"])
	}
	if !idSet(merged["codex"])["gpt-5.1-codex"] {
		t.Errorf("codex must be preserved; got %v", merged["codex"])
	}
}

// TestE2E_ModelsRefreshFallsBackOnAPIFailure — when the API returns 500
// the discovered list is the hardcoded defaults; the WORKFLOW.md is still
// rewritten with that fallback so the dashboard picker doesn't go empty.
func TestE2E_ModelsRefreshFallsBackOnAPIFailure(t *testing.T) {
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer anthropic.Close()
	t.Setenv("ANTHROPIC_API_KEY", "e2e-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", anthropic.URL)

	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\nagent:\n  command: claude\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	discovered := map[string][]agent.ModelOption{
		"claude": agent.ListClaudeModels(), // → DefaultClaudeModels on 500
	}
	if len(discovered["claude"]) == 0 {
		t.Fatal("discovery must NOT be empty even on API failure (must fall back to defaults)")
	}
	merged, err := mergeAvailableModelsIntoWorkflow(wf, discovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged["claude"]) != len(agent.DefaultClaudeModels) {
		t.Errorf("expected default catalog to be written; got %d entries", len(merged["claude"]))
	}
}

func idSet(opts []agent.ModelOption) map[string]bool {
	out := make(map[string]bool, len(opts))
	for _, o := range opts {
		out[o.ID] = true
	}
	return out
}
