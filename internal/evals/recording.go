package evals

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// LoadRecording reads a recording.jsonl file from a scenario directory and
// flattens it into action / comment slices the judges can inspect.
func LoadRecording(scenario Scenario) (Recording, error) {
	path := RecordingPath(scenario)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Recording{}, ErrRecordingMissing
		}
		return Recording{}, err
	}
	defer func() { _ = f.Close() }()

	var rec Recording
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event struct {
			Event  string `json:"event"`
			Action string `json:"action"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Event {
		case "action":
			rec.Actions = append(rec.Actions, event.Action)
		case "comment":
			rec.Comments = append(rec.Comments, event.Body)
		}
	}
	return rec, scanner.Err()
}

// EvaluateScenario loads the recording, expectation, and runs the
// deterministic + structural judges, returning a ScenarioVerdict.
func EvaluateScenario(scenario Scenario) ScenarioVerdict {
	verdict := ScenarioVerdict{Profile: scenario.Profile, Scenario: scenario.Name}
	recording, err := LoadRecording(scenario)
	if err != nil {
		verdict.Deterministic = JudgeVerdict{Pass: false, Reason: "recording missing"}
		verdict.Structural = JudgeVerdict{Pass: false, Reason: "recording missing"}
		verdict.Stale = true
		return verdict
	}
	if scenario.Expect.RequiredActionCalls == nil &&
		scenario.Expect.MarkerPhrases == nil &&
		scenario.Expect.ForbiddenActions == nil {
		// expected.yaml not yet populated for this fixture. Load it.
		_ = readExpectedYAML(scenario.Dir, &scenario.Expect)
	}
	// gaps_11 G-4 — surface stale recordings instead of silently trusting
	// them: a recording older than its source files still passes the judges,
	// but the verdict carries Stale=true so Report.Format flags it with
	// "(stale recording)". SOUL.md / INSTRUCTIONS.md siblings are optional
	// per-fixture sources (absent → mtime 0, never newer); the input.yaml
	// comparison lives inside IsRecordingStale itself.
	verdict.Stale = IsRecordingStale(scenario,
		fileMtimeUnix(filepath.Join(scenario.Dir, "SOUL.md")),
		fileMtimeUnix(filepath.Join(scenario.Dir, "INSTRUCTIONS.md")))
	verdict.Deterministic = JudgeDeterministic(scenario, recording)
	verdict.Structural = JudgeStructural(scenario, recording)
	return verdict
}

// fileMtimeUnix returns the Unix mtime of path, or 0 when the file is
// absent or unreadable — an absent source file can never out-date a
// recording.
func fileMtimeUnix(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

func readExpectedYAML(dir string, into *ScenarioExpect) error {
	data, err := os.ReadFile(dir + "/expected.yaml")
	if err != nil {
		return err
	}
	// Hand-parse — pulling in yaml.v3 just for this would balloon the
	// package's import surface, and the expected.yaml shape is tiny.
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trim, "- "):
			value := strings.Trim(strings.TrimPrefix(trim, "- "), "\"")
			switch current {
			case "required_action_calls":
				into.RequiredActionCalls = append(into.RequiredActionCalls, value)
			case "forbidden_actions":
				into.ForbiddenActions = append(into.ForbiddenActions, value)
			case "marker_phrases":
				into.MarkerPhrases = append(into.MarkerPhrases, value)
			case "blocked_labels":
				into.BlockedLabels = append(into.BlockedLabels, value)
			}
		case strings.HasSuffix(trim, ":"):
			current = strings.TrimSuffix(trim, ":")
		}
	}
	return nil
}

// EvaluateAll runs EvaluateScenario across every scenario and returns the
// aggregate Report.
func EvaluateAll(scenarios []Scenario) Report {
	report := Report{Verdicts: make([]ScenarioVerdict, 0, len(scenarios))}
	for _, s := range scenarios {
		report.Verdicts = append(report.Verdicts, EvaluateScenario(s))
	}
	return report
}
