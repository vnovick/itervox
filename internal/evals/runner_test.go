package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMergeBotEvalsRecordedMode_AllScenariosPass — P1.a acceptance.
// Runs every fixture under internal/evals/fixtures/merge-bot/ in recorded
// mode. Pass rate must be 100% before merge-bot ships.
func TestMergeBotEvalsRecordedMode_AllScenariosPass(t *testing.T) {
	scenarios, err := LoadScenarios("fixtures", "merge-bot")
	if err != nil {
		t.Fatalf("LoadScenarios: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("no merge-bot scenarios discovered")
	}
	report := EvaluateAll(scenarios)
	pass, total := report.Aggregate()
	if pass != total {
		t.Errorf("recorded mode pass rate: %d/%d\nreport:\n%s", pass, total, report.Format())
	}
	// gaps_11 G-4 — staleness is a warning, not a failure: surface any
	// recording that is older than its source files so the operator knows a
	// green run may be judging an out-of-date transcript.
	for _, v := range report.Verdicts {
		if v.Stale {
			t.Logf("warning: stale recording for %s/%s — re-record before trusting this pass", v.Profile, v.Scenario)
		}
	}
}

// TestReviewerEvalsRecordedMode_AllScenariosPass locks the PRODUCER side of
// the merge handshake: the reviewer profile's contract is to leave the
// "/ai-approved" marker (plus the human-readable approval comment) on the
// approve path, and to comment-numbered-failures + move_state on the reject
// path — never merging in either. Recordings are hand-authored contracts
// pending live-recording mode; see fixtures/README.md.
func TestReviewerEvalsRecordedMode_AllScenariosPass(t *testing.T) {
	scenarios, err := LoadScenarios("fixtures", "reviewer")
	if err != nil {
		t.Fatalf("LoadScenarios: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("no reviewer scenarios discovered")
	}
	report := EvaluateAll(scenarios)
	pass, total := report.Aggregate()
	if pass != total {
		t.Errorf("recorded mode pass rate: %d/%d\nreport:\n%s", pass, total, report.Format())
	}
	for _, v := range report.Verdicts {
		if v.Stale {
			t.Logf("warning: stale recording for %s/%s — re-record before trusting this pass", v.Profile, v.Scenario)
		}
	}
}

// gaps_11 G-4 — EvaluateScenario must flag a recording that is older than
// its source files (input.yaml, or optional SOUL.md / INSTRUCTIONS.md
// siblings) via IsRecordingStale, while the judges still run: staleness is
// a warning surface, not a failure.
func TestEvaluateScenario_StaleRecordingFlaggedButStillJudged(t *testing.T) {
	cases := []struct {
		name      string
		newerFile string
	}{
		{name: "input.yaml newer than recording", newerFile: "input.yaml"},
		{name: "SOUL.md newer than recording", newerFile: "SOUL.md"},
		{name: "INSTRUCTIONS.md newer than recording", newerFile: "INSTRUCTIONS.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scenario := writeStaleableScenario(t)
			// Recording two hours older than the (just-written) source file.
			old := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(RecordingPath(scenario), old, old); err != nil {
				t.Fatal(err)
			}
			if tc.newerFile != "input.yaml" {
				if err := os.WriteFile(filepath.Join(scenario.Dir, tc.newerFile), []byte("# source\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				// Keep input.yaml older than the recording so only the
				// case-specific source file triggers staleness.
				if err := os.Chtimes(filepath.Join(scenario.Dir, "input.yaml"), old.Add(-time.Hour), old.Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			}

			v := EvaluateScenario(scenario)

			if !v.Stale {
				t.Error("recording older than source file must be flagged stale")
			}
			if !v.Pass() {
				t.Errorf("staleness must not fail the judges; verdict: %+v", v)
			}
			report := Report{Verdicts: []ScenarioVerdict{v}}
			if !strings.Contains(report.Format(), "(stale recording)") {
				t.Errorf("report must surface the stale warning; got:\n%s", report.Format())
			}
		})
	}
}

func TestEvaluateScenario_FreshRecordingNotStale(t *testing.T) {
	scenario := writeStaleableScenario(t)
	// Sources two hours older than the (just-written) recording.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(scenario.Dir, "input.yaml"), old, old); err != nil {
		t.Fatal(err)
	}

	v := EvaluateScenario(scenario)

	if v.Stale {
		t.Error("recording newer than every source file must not be flagged stale")
	}
	if !v.Pass() {
		t.Errorf("fresh passing scenario expected; verdict: %+v", v)
	}
}

// writeStaleableScenario lays down a minimal passing fixture (input.yaml,
// expected.yaml, recording.jsonl) and returns the loaded Scenario so tests
// can manipulate mtimes to construct (non-)staleness.
func writeStaleableScenario(t *testing.T) Scenario {
	t.Helper()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "merge-bot", "staleable")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"input.yaml":      "issue: {}\n",
		"expected.yaml":   "required_action_calls:\n  - merge_pr\n",
		"recording.jsonl": `{"event":"action","action":"merge_pr"}` + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scenarios, err := LoadScenarios(tmp, "merge-bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("expected 1 scenario; got %d", len(scenarios))
	}
	return scenarios[0]
}

func TestEvaluateScenario_MissingRecordingFlagsStale(t *testing.T) {
	tmp := t.TempDir()
	scenarioDir := filepath.Join(tmp, "merge-bot", "missing-recording")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "input.yaml"), []byte("issue: {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "expected.yaml"), []byte("required_action_calls:\n  - merge_pr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scenarios, err := LoadScenarios(tmp, "merge-bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("expected 1 scenario; got %d", len(scenarios))
	}
	v := EvaluateScenario(scenarios[0])
	if v.Pass() {
		t.Error("scenario with missing recording must not pass")
	}
	if !v.Stale {
		t.Error("missing recording must mark scenario stale")
	}
}

func TestJudgeDeterministic_RequiredActionMissingFails(t *testing.T) {
	scenario := Scenario{
		Profile: "merge-bot",
		Name:    "x",
		Expect:  ScenarioExpect{RequiredActionCalls: []string{"merge_pr"}},
	}
	v := JudgeDeterministic(scenario, Recording{Actions: []string{"comment"}})
	if v.Pass {
		t.Error("missing required action should fail")
	}
	if !strings.Contains(v.Reason, "merge_pr") {
		t.Errorf("reason should mention the missing action; got %q", v.Reason)
	}
}

func TestJudgeStructural_MarkerPhrasePresent(t *testing.T) {
	scenario := Scenario{
		Profile: "merge-bot",
		Name:    "x",
		Expect:  ScenarioExpect{MarkerPhrases: []string{"merged"}},
	}
	v := JudgeStructural(scenario, Recording{Comments: []string{"merge-bot: merged PR-42"}})
	if !v.Pass {
		t.Errorf("marker phrase present should pass; got %q", v.Reason)
	}
}

func TestReport_ExitCode(t *testing.T) {
	good := Report{Verdicts: []ScenarioVerdict{{Deterministic: JudgeVerdict{Pass: true}, Structural: JudgeVerdict{Pass: true}}}}
	if good.ExitCode() != 0 {
		t.Error("all-pass report must have exit code 0")
	}
	bad := Report{Verdicts: []ScenarioVerdict{{Deterministic: JudgeVerdict{Pass: false}}}}
	if bad.ExitCode() == 0 {
		t.Error("failing report must have non-zero exit code")
	}
}

func TestReport_FormatIncludesHeaderLine(t *testing.T) {
	r := Report{Verdicts: []ScenarioVerdict{
		{Profile: "merge-bot", Scenario: "x", Deterministic: JudgeVerdict{Pass: true}, Structural: JudgeVerdict{Pass: true}},
	}}
	out := r.Format()
	if !strings.HasPrefix(out, "[evals] 1/1 pass") {
		t.Errorf("expected header line; got %q", out)
	}
}
