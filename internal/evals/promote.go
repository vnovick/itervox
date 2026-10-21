package evals

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CandidatePath returns where a re-recording of scenario is written.
func CandidatePath(scenario Scenario) string {
	return filepath.Join(scenario.Dir, CandidateName)
}

// ErrNoCandidate reports that a scenario has no recording.candidate.jsonl
// to promote.
var ErrNoCandidate = errors.New("evals: no " + CandidateName + " to promote")

// PromoteCandidate replaces a scenario's recording.jsonl with its reviewed
// candidate (#189). It is the explicit, human-run step after a re-record:
// the candidate is judged against expected.yaml first, and a failing one is
// refused unless force is set (a deliberate contract change still needs
// expected.yaml updated, which force does not do). The rename is atomic, so
// the fixture is either the old recording or the whole candidate.
func PromoteCandidate(scenario Scenario, force bool) (ScenarioVerdict, error) {
	scenario = withExpectation(scenario)
	verdict := ScenarioVerdict{Profile: scenario.Profile, Scenario: scenario.Name}
	candidate := CandidatePath(scenario)
	rec, err := LoadRecordingFile(candidate)
	if err != nil {
		if errors.Is(err, ErrRecordingMissing) {
			return verdict, fmt.Errorf("%w: %s", ErrNoCandidate, candidate)
		}
		return verdict, err
	}
	verdict.Deterministic = JudgeDeterministic(scenario, rec)
	verdict.Structural = JudgeStructural(scenario, rec)
	if !verdict.Pass() && !force {
		return verdict, fmt.Errorf("evals: %s/%s candidate fails its judges (%s; %s); fix the profile or expected.yaml, or promote with force",
			scenario.Profile, scenario.Name, verdict.Deterministic.Reason, verdict.Structural.Reason)
	}
	if err := os.Rename(candidate, RecordingPath(scenario)); err != nil {
		return verdict, err
	}
	return verdict, nil
}
