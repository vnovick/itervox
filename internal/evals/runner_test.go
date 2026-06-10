package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
