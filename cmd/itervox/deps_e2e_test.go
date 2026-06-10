package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/depsanalysis"
)

// fakeAnalyzerRunner implements agent.Runner with canned JSON output. Used
// by the deps-analyze E2E test to drive the runDepsAnalyze pipeline end to
// end without spawning a real claude/codex subprocess.
type fakeAnalyzerRunner struct {
	resultJSON string
	called     int
}

func (f *fakeAnalyzerRunner) RunTurn(
	_ context.Context,
	_ agent.Logger,
	_ func(agent.TurnResult),
	sessionID *string,
	_, _, _, _, _ string,
	_, _ int,
) (agent.TurnResult, error) {
	f.called++
	sid := "deps-e2e-session"
	if sessionID != nil {
		*sessionID = sid
	}
	return agent.TurnResult{
		SessionID:  sid,
		ResultText: f.resultJSON,
	}, nil
}

// TestE2E_RunDepsAnalyzeWritesSidecarWithInferredEdges drives the
// `itervox deps analyze` pipeline end-to-end:
//
//  1. Seeds a temp project with a schema-2 WORKFLOW.md using the memory
//     tracker (10 demo issues from tracker.GenerateDemoIssues).
//  2. Scaffolds the deps-analyzer profile files on disk so the config
//     loader resolves them.
//  3. Injects a fake runner via the initDepsAnalysisRunner seam — emits
//     canned JSON for two inferred edges between demo identifiers.
//  4. Calls runDepsAnalyze with the temp workflow.
//  5. Asserts .itervox/dependencies.json was written with schema version 1
//     and the canned edges.
//
// This closes the UNMET gap from the prior session:
// `runDepsAnalyze` going through `runInitDepsAnalysis → buildTracker →
// RunAgentPass → SaveSidecar` was previously grep-verified only.
func TestE2E_RunDepsAnalyzeWritesSidecarWithInferredEdges(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := writeE2EWorkflow(t, tmp, wf); err != nil {
		t.Fatal(err)
	}
	// Swap in a fake runner that emits a known edges payload.
	fake := &fakeAnalyzerRunner{
		resultJSON: `{"edges": [
			{"source": "DEMO-1", "target": "DEMO-2", "evidence": "demo-1 references demo-2 in its body"},
			{"source": "DEMO-3", "target": "DEMO-4", "evidence": "title mentions blocks demo-4"}
		]}`,
	}
	prevRunner := initDepsAnalysisRunner
	initDepsAnalysisRunner = func() agent.Runner { return fake }
	t.Cleanup(func() { initDepsAnalysisRunner = prevRunner })

	// Drive the production code path the CLI subcommand uses.
	issueCount, edgeCount, sidecarPath, err := runInitDepsAnalysis(wf)
	if err != nil {
		t.Fatalf("runInitDepsAnalysis: %v", err)
	}

	// Sanity: the fake runner was actually invoked (not a fallback path).
	if fake.called != 1 {
		t.Errorf("fake runner call count = %d; want 1", fake.called)
	}
	if issueCount == 0 {
		t.Error("memory tracker should have surfaced demo issues")
	}
	if edgeCount != 2 {
		t.Errorf("expected 2 inferred edges; got %d", edgeCount)
	}

	// Sidecar file shape.
	sc, err := depsanalysis.LoadSidecar(sidecarPath)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if sc == nil {
		t.Fatal("sidecar was nil — schema-mismatch or missing file")
	}
	if sc.Version != depsanalysis.SidecarSchemaVersion {
		t.Errorf("schema version = %d; want %d", sc.Version, depsanalysis.SidecarSchemaVersion)
	}
	if sc.Profile != "deps-analyzer" {
		t.Errorf("profile = %q; want deps-analyzer", sc.Profile)
	}
	gotEdges := map[string]string{}
	for _, e := range sc.Edges {
		gotEdges[e.Source+"->"+e.Target] = e.Evidence
	}
	if gotEdges["DEMO-1->DEMO-2"] == "" {
		t.Errorf("expected DEMO-1->DEMO-2 edge; got %+v", sc.Edges)
	}
	if gotEdges["DEMO-3->DEMO-4"] == "" {
		t.Errorf("expected DEMO-3->DEMO-4 edge; got %+v", sc.Edges)
	}
}

// TestE2E_RunDepsAnalyzeReturnsErrorOnInvalidJSON — when the fake runner
// emits text without a parseable edges-block, the pipeline must surface
// the error rather than write an empty sidecar (which would be
// indistinguishable from a successful zero-edges pass).
func TestE2E_RunDepsAnalyzeReturnsErrorOnInvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := writeE2EWorkflow(t, tmp, wf); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnalyzerRunner{resultJSON: "the analyzer rambles without a JSON block"}
	prevRunner := initDepsAnalysisRunner
	initDepsAnalysisRunner = func() agent.Runner { return fake }
	t.Cleanup(func() { initDepsAnalysisRunner = prevRunner })

	_, _, _, err := runInitDepsAnalysis(wf)
	if err == nil {
		t.Fatal("expected an error when the analyzer emits no JSON")
	}
	if !strings.Contains(err.Error(), "agent pass") {
		t.Errorf("error should mention agent pass; got %v", err)
	}
}

// writeE2EWorkflow builds the minimal on-disk surface runInitDepsAnalysis
// needs:
//   - WORKFLOW.md with schema 2 + memory tracker + deps_analyzer_profile
//   - .itervox/agents/deps-analyzer/{SOUL,INSTRUCTIONS}.md scaffolds
func writeE2EWorkflow(t *testing.T, projectDir, workflowPath string) error {
	t.Helper()
	agentDir := filepath.Join(projectDir, ".itervox", "agents", "deps-analyzer")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.md"),
		[]byte("# deps-analyzer SOUL\nYou identify dependency relations.\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(agentDir, "INSTRUCTIONS.md"),
		[]byte("# deps-analyzer INSTRUCTIONS\nEmit strict JSON of the form {\"edges\":[...]}\n"), 0o644); err != nil {
		return err
	}
	content := `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
  backlog_states: ["Backlog"]
agent:
  command: claude
  deps_analyzer_profile: deps-analyzer
  profiles:
    deps-analyzer:
      command: claude
      backend: claude
      soul_file: .itervox/agents/deps-analyzer/SOUL.md
      instructions_file: .itervox/agents/deps-analyzer/INSTRUCTIONS.md
workspace:
  root: ` + projectDir + `/.itervox/workspaces
server:
  port: 0
---

You are working on {{ issue.identifier }}.
`
	return os.WriteFile(workflowPath, []byte(content), 0o644)
}
