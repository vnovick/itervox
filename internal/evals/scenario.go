// Package evals is the minimum-viable foundation of the v0.2.0 profile
// evaluation suite (P1.a). It defines:
//
//   - Scenario: a fixture pair (input + expected) keyed by profile name.
//   - Recording: a cached LLM transcript stored next to the fixture.
//   - Runner: replays a recording against a scenario and runs deterministic
//     judges (Tier-1 + Tier-2).
//   - Judge: a small interface that returns pass/fail with a one-line reason.
//   - Report: aggregates per-profile pass rate, formats human and JSON output.
//
// LLM-judge (Tier 3) and real Anthropic/OpenAI invocations are deferred to
// P1.c/d; this package is enough to gate prompt regressions deterministically.
package evals

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scenario is a (profile, name) pair pointing at on-disk fixture files.
type Scenario struct {
	Profile string
	Name    string
	Dir     string
	Input   ScenarioInput
	Expect  ScenarioExpect
}

// ScenarioInput is the input fixture (intentionally tiny; the recorded LLM
// response is what carries the model output).
type ScenarioInput struct {
	IssueIdentifier string
	IssueState      string
	IssueTitle      string
	IssueBody       string
	BranchName      string
	CommentBody     string
	PRNumber        int
}

// ScenarioExpect is the structural acceptance criterion. The minimum
// shipping shape is "the agent's recorded transcript called these actions
// with these arguments". Tier-3 LLM judges (deferred) would consult a
// separate `judge.yaml` for prose acceptance.
type ScenarioExpect struct {
	RequiredActionCalls []string
	ForbiddenActions    []string
	MarkerPhrases       []string
	BlockedLabels       []string
}

// Recording is the cached transcript. The minimum-viable shape is a list of
// action calls and emitted comments; the real recorder will capture full
// SDK events.
type Recording struct {
	Actions  []string
	Comments []string
}

// LoadScenarios walks the fixture root and returns all Scenarios it can read.
// Skips directories whose input.yaml or expected.yaml is missing.
func LoadScenarios(root, profileFilter string) ([]Scenario, error) {
	out := []Scenario{}
	profiles, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, profileEntry := range profiles {
		if !profileEntry.IsDir() {
			continue
		}
		if profileFilter != "" && profileEntry.Name() != profileFilter {
			continue
		}
		profileDir := filepath.Join(root, profileEntry.Name())
		scenarios, err := os.ReadDir(profileDir)
		if err != nil {
			continue
		}
		for _, scenarioEntry := range scenarios {
			if !scenarioEntry.IsDir() {
				continue
			}
			scenarioDir := filepath.Join(profileDir, scenarioEntry.Name())
			scenario, err := loadScenario(profileEntry.Name(), scenarioEntry.Name(), scenarioDir)
			if err != nil {
				continue
			}
			out = append(out, scenario)
		}
	}
	return out, nil
}

func loadScenario(profile, name, dir string) (Scenario, error) {
	if _, err := os.Stat(filepath.Join(dir, "input.yaml")); err != nil {
		return Scenario{}, fmt.Errorf("missing input.yaml in %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "expected.yaml")); err != nil {
		return Scenario{}, fmt.Errorf("missing expected.yaml in %s", dir)
	}
	return Scenario{Profile: profile, Name: name, Dir: dir}, nil
}

// ErrRecordingMissing reports that a scenario's recording.jsonl is absent.
// Callers can decide whether to fail (live mode) or warn (recorded mode).
var ErrRecordingMissing = errors.New("recording.jsonl missing")

// RecordingPath returns the canonical recording path for a scenario.
func RecordingPath(scenario Scenario) string {
	return filepath.Join(scenario.Dir, "recording.jsonl")
}

// IsRecordingStale heuristically reports whether the recording is older
// than any of the source files (SOUL.md, INSTRUCTIONS.md, input.yaml).
// True = stale → warn the operator.
func IsRecordingStale(scenario Scenario, soulMtime, instructionsMtime int64) bool {
	info, err := os.Stat(RecordingPath(scenario))
	if err != nil {
		return true
	}
	recordingMtime := info.ModTime().Unix()
	if soulMtime > recordingMtime {
		return true
	}
	if instructionsMtime > recordingMtime {
		return true
	}
	inputInfo, err := os.Stat(filepath.Join(scenario.Dir, "input.yaml"))
	if err == nil && inputInfo.ModTime().Unix() > recordingMtime {
		return true
	}
	return false
}

// JudgeVerdict is the result of evaluating one scenario against its
// expectation. A passing scenario reports pass=true; the reason field is the
// one-line explanation surfaced in the report.
type JudgeVerdict struct {
	Pass   bool
	Reason string
}

// JudgeDeterministic is the Tier-1 judge: check that the recorded transcript
// called the required actions and omitted the forbidden ones.
func JudgeDeterministic(scenario Scenario, recording Recording) JudgeVerdict {
	for _, want := range scenario.Expect.RequiredActionCalls {
		if !containsString(recording.Actions, want) {
			return JudgeVerdict{Pass: false, Reason: "missing required action: " + want}
		}
	}
	for _, banned := range scenario.Expect.ForbiddenActions {
		if containsString(recording.Actions, banned) {
			return JudgeVerdict{Pass: false, Reason: "forbidden action called: " + banned}
		}
	}
	return JudgeVerdict{Pass: true, Reason: "all required actions present"}
}

// JudgeStructural is the Tier-2 judge: check that the agent emitted at least
// one comment containing each marker phrase the scenario expects.
func JudgeStructural(scenario Scenario, recording Recording) JudgeVerdict {
	for _, phrase := range scenario.Expect.MarkerPhrases {
		found := false
		for _, c := range recording.Comments {
			if strings.Contains(c, phrase) {
				found = true
				break
			}
		}
		if !found {
			return JudgeVerdict{Pass: false, Reason: "marker phrase missing from comments: " + phrase}
		}
	}
	return JudgeVerdict{Pass: true, Reason: "marker phrases present"}
}

func containsString(slice []string, needle string) bool {
	for _, s := range slice {
		if s == needle {
			return true
		}
	}
	return false
}

// Report aggregates per-profile pass rates and exit codes.
type Report struct {
	Verdicts []ScenarioVerdict
}

// ScenarioVerdict combines all per-judge verdicts for a single scenario.
type ScenarioVerdict struct {
	Profile       string
	Scenario      string
	Deterministic JudgeVerdict
	Structural    JudgeVerdict
	Stale         bool
}

// Pass returns true when every judge verdict on this scenario passes.
func (v ScenarioVerdict) Pass() bool {
	return v.Deterministic.Pass && v.Structural.Pass
}

// Aggregate counts pass/fail across the report.
func (r Report) Aggregate() (pass, total int) {
	for _, v := range r.Verdicts {
		total++
		if v.Pass() {
			pass++
		}
	}
	return pass, total
}

// ExitCode returns 0 when every verdict passes; non-zero otherwise.
func (r Report) ExitCode() int {
	for _, v := range r.Verdicts {
		if !v.Pass() {
			return 1
		}
	}
	return 0
}

// Format renders the report as a human-friendly multi-line string. The first
// line is the header consumed by `make evals-fast`; later lines list each
// scenario.
func (r Report) Format() string {
	pass, total := r.Aggregate()
	var b strings.Builder
	fmt.Fprintf(&b, "[evals] %d/%d pass\n", pass, total)
	for _, v := range r.Verdicts {
		mark := "PASS"
		if !v.Pass() {
			mark = "FAIL"
		}
		stale := ""
		if v.Stale {
			stale = " (stale recording)"
		}
		fmt.Fprintf(&b, "  %s %s/%s%s\n", mark, v.Profile, v.Scenario, stale)
		if !v.Deterministic.Pass {
			fmt.Fprintf(&b, "    deterministic: %s\n", v.Deterministic.Reason)
		}
		if !v.Structural.Pass {
			fmt.Fprintf(&b, "    structural:    %s\n", v.Structural.Reason)
		}
	}
	return b.String()
}
