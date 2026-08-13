package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/depsanalysis"
)

// fakeAnalyzerRunner implements agent.Runner with canned JSON output. Used
// by the deps-analyze E2E test to drive the runDepsAnalyze pipeline end to
// end without spawning a real claude/codex subprocess. failOn (1-based, 0 =
// never) fails the Nth call — used to prove chunk failures fail the whole
// run on this CLI path the same way they do on the daemon path.
type fakeAnalyzerRunner struct {
	resultJSON string
	called     int
	failOn     int
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
	if f.failOn > 0 && f.called == f.failOn {
		return agent.TurnResult{}, errors.New("fake analyzer chunk failure")
	}
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
//     tracker (10 demo issues from tracker.GenerateDemoIssues) and
//     deps_analyzer_chunk_size: 4, so the 10 issues split into 3 chunks
//     (4, 4, 2) — this exercises the chunked loop this CLI path gained in
//     review round 1 (I-4), not just the single-turn path.
//  2. Scaffolds the deps-analyzer profile files on disk so the config
//     loader resolves them.
//  3. Injects a fake runner via the initDepsAnalysisRunner seam — emits
//     canned JSON for two inferred edges between demo identifiers on every
//     chunk call.
//  4. Calls runDepsAnalyze with the temp workflow.
//  5. Asserts .itervox/dependencies.json was written with schema version 1
//     and the canned edges, deduplicated across the 3 chunks that each
//     reported them.
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
	issueCount, analyzedCount, edgeCount, sidecarPath, guarded, err := runInitDepsAnalysis(wf, "auto")
	if err != nil {
		t.Fatalf("runInitDepsAnalysis: %v", err)
	}
	if guarded {
		t.Fatal("empty-fetch guard must not fire when the fetch actually returned issues")
	}

	// The fake runner must be called once per chunk: 10 demo issues at
	// chunk size 4 is 3 chunks (4, 4, 2). Before I-4 this path sent the
	// entire backlog in one turn — a fixed call count of 1 here would have
	// hidden that regression.
	if fake.called != 3 {
		t.Errorf("fake runner call count = %d; want 3 (10 issues at chunk size 4)", fake.called)
	}
	if issueCount == 0 {
		t.Error("memory tracker should have surfaced demo issues")
	}
	// #52 IssuesScanned honesty — no prior sidecar exists yet, so
	// PlanIncremental resolves to full mode and analyzedCount must equal
	// issueCount (every fetched issue goes to the agent).
	if analyzedCount != issueCount {
		t.Errorf("analyzedCount = %d; want %d (full mode analyzes every fetched issue)", analyzedCount, issueCount)
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
		// Explicit return: staticcheck's SA5011 does not model t.Fatal as
		// terminating, so without it every sc.* dereference below is flagged
		// as a possible nil deref.
		t.Fatal("sidecar was nil — schema-mismatch or missing file")
		return
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

// TestE2E_RunDepsAnalyzePrintsScannedAnalyzedRevalidated (#52 IssuesScanned
// honesty) — `itervox deps analyze`'s stdout must state scanned/analyzed/
// revalidated explicitly rather than the old "analyzed %d issue(s)" wording,
// which actually meant "scanned". No prior sidecar exists here, so
// PlanIncremental resolves to full mode: analyzed == scanned, revalidated ==
// 0.
func TestE2E_RunDepsAnalyzePrintsScannedAnalyzedRevalidated(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := writeE2EWorkflow(t, tmp, wf); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnalyzerRunner{resultJSON: `{"edges": []}`}
	prevRunner := initDepsAnalysisRunner
	initDepsAnalysisRunner = func() agent.Runner { return fake }
	t.Cleanup(func() { initDepsAnalysisRunner = prevRunner })

	stdout, _ := captureStdout(t, func() {
		runDepsAnalyze([]string{"--workflow", wf})
	})

	if !strings.Contains(stdout, "scanned 10 issue(s) (10 analyzed, 0 revalidated)") {
		t.Errorf("expected the scanned/analyzed/revalidated wording on stdout; got %q", stdout)
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

	_, _, _, _, _, err := runInitDepsAnalysis(wf, "auto")
	if err == nil {
		t.Fatal("expected an error when the analyzer emits no JSON")
	}
	if !strings.Contains(err.Error(), "agent pass") {
		t.Errorf("error should mention agent pass; got %v", err)
	}
}

// TestE2E_RunDepsAnalyzeFailsWholeRunOnChunkFailure (I-4, review round 1) —
// a chunk failure partway through the CLI path's chunked loop must fail the
// WHOLE run and write no sidecar, matching depsAnalyzerService.run's
// behaviour on the daemon path. 10 demo issues at chunk size 4 is 3 chunks;
// the fake runner fails on the 2nd call.
func TestE2E_RunDepsAnalyzeFailsWholeRunOnChunkFailure(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := writeE2EWorkflow(t, tmp, wf); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnalyzerRunner{
		resultJSON: `{"edges": []}`,
		failOn:     2,
	}
	prevRunner := initDepsAnalysisRunner
	initDepsAnalysisRunner = func() agent.Runner { return fake }
	t.Cleanup(func() { initDepsAnalysisRunner = prevRunner })

	_, _, _, _, _, err := runInitDepsAnalysis(wf, "auto")
	if err == nil {
		t.Fatal("expected an error when a chunk fails")
	}
	if !strings.Contains(err.Error(), "chunk 2/3") {
		t.Errorf("error should identify the failing chunk; got %v", err)
	}
	if fake.called != 2 {
		t.Errorf("fake runner call count = %d; want 2 (stops at the failing chunk, does not run chunk 3)", fake.called)
	}

	sidecarPath := depsanalysis.SidecarPath(tmp)
	if _, statErr := os.Stat(sidecarPath); !os.IsNotExist(statErr) {
		t.Error("no sidecar may be written when a chunk fails mid-run")
	}
}

// writeE2EWorkflow builds the minimal on-disk surface runInitDepsAnalysis
// needs:
//   - WORKFLOW.md with schema 2 + memory tracker + deps_analyzer_profile
//   - .itervox/agents/deps-analyzer/{SOUL,INSTRUCTIONS}.md scaffolds
//
// active_states/terminal_states/backlog_states are "Todo"/"In Progress",
// "Done", "Backlog" — matching every state tracker.GenerateDemoIssues(10)
// produces (Todo/In Progress only), so the fetch always sees all 10 demo
// issues.
func writeE2EWorkflow(t *testing.T, projectDir, workflowPath string) error {
	t.Helper()
	return writeE2EWorkflowWithStates(t, projectDir, workflowPath,
		`["Todo", "In Progress"]`, `["Done"]`, `["Backlog"]`)
}

// writeE2EWorkflowWithStates is writeE2EWorkflow with the state filters
// exposed, so a test can request a state set that matches none of the demo
// tracker's issues — the E2E-realistic way to produce an empty fetch (10
// issues genuinely exist in the memory tracker; FetchIssues just filters all
// of them out) rather than seeding a tracker with zero issues, which the
// production code path never actually sees.
func writeE2EWorkflowWithStates(t *testing.T, projectDir, workflowPath, activeStatesYAML, terminalStatesYAML, backlogStatesYAML string) error {
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
  active_states: ` + activeStatesYAML + `
  terminal_states: ` + terminalStatesYAML + `
  backlog_states: ` + backlogStatesYAML + `
agent:
  command: claude
  deps_analyzer_profile: deps-analyzer
  deps_analyzer_chunk_size: 4
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

// TestInitDepsAnalysisEmptyFetchDoesNotWipeSidecar is the RED test for the
// mandatory empty-fetch guard on the CLI/init path (mirrors
// TestServiceRunEmptyFetchDoesNotWipeSidecar for the daemon path — see
// docs/superpowers/specs/2026-08-04-analyzer-autonomy-design.md "Empty-fetch
// guard"). Seeds a real sidecar on disk with one edge, then runs
// runInitDepsAnalysis against a workflow whose state filters match none of
// the memory tracker's 10 demo issues (so FetchIssues genuinely returns
// zero, not because the tracker itself is empty). The sidecar file must be
// byte-identical before and after, and emptyFetchGuarded must be true.
func TestInitDepsAnalysisEmptyFetchDoesNotWipeSidecar(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	// "NoSuchState" matches none of GenerateDemoIssues's "Todo"/"In Progress" states.
	if err := writeE2EWorkflowWithStates(t, tmp, wf, `["NoSuchState"]`, `[]`, `[]`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnalyzerRunner{resultJSON: `{"edges": []}`}
	prevRunner := initDepsAnalysisRunner
	initDepsAnalysisRunner = func() agent.Runner { return fake }
	t.Cleanup(func() { initDepsAnalysisRunner = prevRunner })

	sidecarPath := depsanalysis.SidecarPath(tmp)
	prior := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "OLD-1", Target: "OLD-2", Evidence: "prior pass", Confidence: 0.7,
				InferredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
	if err := depsanalysis.SaveSidecar(sidecarPath, prior); err != nil {
		t.Fatalf("seed prior sidecar: %v", err)
	}
	before, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read seeded sidecar: %v", err)
	}

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	issueCount, analyzedCount, edgeCount, gotPath, guarded, err := runInitDepsAnalysis(wf, "auto")
	if err != nil {
		t.Fatalf("runInitDepsAnalysis: %v", err)
	}
	if !guarded {
		t.Fatal("emptyFetchGuarded must be true when the fetch is empty and the prior sidecar has edges")
	}
	if issueCount != 0 {
		t.Errorf("issueCount = %d; want 0", issueCount)
	}
	if analyzedCount != 0 {
		t.Errorf("analyzedCount = %d; want 0 — the guard fires before any plan/agent pass", analyzedCount)
	}
	if edgeCount != 1 {
		t.Errorf("edgeCount = %d; want 1 (the unchanged prior sidecar's edge count)", edgeCount)
	}
	if gotPath != sidecarPath {
		t.Errorf("sidecarPath = %q; want %q", gotPath, sidecarPath)
	}
	if fake.called != 0 {
		t.Errorf("fake runner call count = %d; want 0 — the guard must fire before any agent pass", fake.called)
	}

	after, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar after run: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("sidecar bytes changed on disk when the guard should have fired")
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "empty-fetch guard") {
		t.Errorf("expected a warning naming the guard; got log: %s", logged)
	}
	if !strings.Contains(logged, "refusing to overwrite 1 inferred edges with an empty fetch") {
		t.Errorf("expected the edge count in the warning; got log: %s", logged)
	}
}
