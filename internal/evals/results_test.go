package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// failingVerdict judges a recording that misses its marker and carries a
// forbidden phrase, so both structural assertions fail.
func failingVerdict(t *testing.T, digest string) ScenarioVerdict {
	t.Helper()
	sc := Scenario{Profile: "reviewer", Name: "rejects", Expect: ScenarioExpect{
		RequiredActionCalls: []string{"comment"},
		ForbiddenPhrases:    []string{"/ai-approved"},
		MarkerPhrases:       []string{"changes requested"},
	}}
	rec := Recording{Actions: []string{"comment"}, Comments: []string{"/ai-approved looks fine"}}
	return ScenarioVerdict{Profile: sc.Profile, Scenario: sc.Name,
		Deterministic: JudgeDeterministic(sc, rec), Structural: JudgeStructural(sc, rec),
		PromptDigest: digest, DigestSource: DigestSourceLive}
}

func passingVerdict(profile, scenario, digest string) ScenarioVerdict {
	return ScenarioVerdict{Profile: profile, Scenario: scenario,
		Deterministic: JudgeVerdict{Pass: true}, Structural: JudgeVerdict{Pass: true},
		PromptDigest: digest, DigestSource: DigestSourceLive}
}

// TestEvalReportPersistedAsJSON (#201): a judged report is written as JSON
// to <dir>/<ISO8601>.json with every failed assertion and the prompt digest,
// the directory is gitignored, only the newest MaxResultFiles are kept, and
// LoadResults reads the newest back.
func TestEvalReportPersistedAsJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".itervox", "evals")
	digest := strings.Repeat("ab", 32)
	report := Report{Verdicts: []ScenarioVerdict{
		passingVerdict("merge-bot", "green", digest),
		failingVerdict(t, digest),
	}}
	finished := time.Date(2026, 10, 21, 9, 30, 15, 250e6, time.UTC)
	f := NewResultFile(ModeLive, report, []LiveRun{{Profile: "reviewer", Scenario: "rejects", Tokens: 1200, CostUSD: 0.02}},
		"WORKFLOW.md", finished.Add(-time.Minute), finished)
	path, err := WriteResultFile(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(path), "20261021T093015.250Z.json"; got != want {
		t.Errorf("file name = %s, want %s (ISO 8601 basic, UTC)", got, want)
	}
	var back map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, data)
	}
	if back["mode"] != ModeLive || back["pass"] != 1.0 || back["total"] != 2.0 || back["version"] != 1.0 {
		t.Errorf("header = mode %v pass %v total %v version %v", back["mode"], back["pass"], back["total"], back["version"])
	}
	results := back["results"].([]any)
	failed := results[1].(map[string]any)
	if failed["pass"] != false || failed["promptDigest"] != digest || failed["digestSource"] != DigestSourceLive || failed["tokens"] != 1200.0 {
		t.Errorf("failed case = %v", failed)
	}
	wantFailures := []any{"forbidden phrase present in comments: /ai-approved", "marker phrase missing from comments: changes requested"}
	if got := failed["failures"]; !jsonEqual(got, wantFailures) {
		t.Errorf("failures = %v, want every failed assertion %v", got, wantFailures)
	}
	if _, ok := results[0].(map[string]any)["failures"]; ok {
		t.Error("a passing case lists no failures")
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !regexp.MustCompile(`(?m)^\*$`).Match(ignore) {
		t.Errorf("results dir must ignore everything in it; .gitignore = %q (%v)", ignore, err)
	}

	// Retention: the newest MaxResultFiles survive.
	for i := range MaxResultFiles + 2 {
		at := finished.Add(time.Duration(i+1) * time.Hour)
		if _, err := WriteResultFile(dir, NewResultFile(ModeRecorded, Report{Verdicts: []ScenarioVerdict{passingVerdict("merge-bot", "green", "")}}, nil, "", at, at)); err != nil {
			t.Fatal(err)
		}
	}
	names, err := resultFileNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != MaxResultFiles {
		t.Fatalf("kept %d reports, want %d", len(names), MaxResultFiles)
	}
	if names[0] != finished.Add(time.Duration(MaxResultFiles+2)*time.Hour).Format(resultTimeLayout)+".json" {
		t.Errorf("newest report = %s", names[0])
	}
	// Same-millisecond reports do not overwrite each other.
	newest := finished.Add(100 * time.Hour)
	p1, _ := WriteResultFile(dir, NewResultFile(ModeRecorded, Report{}, nil, "", newest, newest))
	p2, _ := WriteResultFile(dir, NewResultFile(ModeRecorded, Report{}, nil, "", newest, newest))
	if p1 == p2 {
		t.Error("a second report in the same millisecond must get its own file")
	}

	sum, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Reports) != MaxResultFiles {
		t.Errorf("LoadResults read %d reports, want the newest %d", len(sum.Reports), MaxResultFiles)
	}
	missing, err := LoadResults(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(missing.Cases) != 0 {
		t.Errorf("a missing dir is an empty summary; got %+v, %v", missing, err)
	}
}

// TestEvalResultsMarkRegressionAcrossDigests (#201): a case that passed on
// the previous prompt digest and fails on the current one is a regression;
// a case failing on both digests, failing again on the same digest, or
// without a digest is not.
func TestEvalResultsMarkRegressionAcrossDigests(t *testing.T) {
	dir := t.TempDir()
	prev, cur := strings.Repeat("1", 64), strings.Repeat("2", 64)
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	write := func(at time.Time, verdicts ...ScenarioVerdict) {
		t.Helper()
		if _, err := WriteResultFile(dir, NewResultFile(ModeLive, Report{Verdicts: verdicts}, nil, "", at, at)); err != nil {
			t.Fatal(err)
		}
	}
	regressed := failingVerdict(t, cur)
	alwaysFailing := failingVerdict(t, cur)
	alwaysFailing.Scenario = "always-failing"
	prevFailing := failingVerdict(t, prev)
	prevFailing.Scenario = "always-failing"
	undigested := failingVerdict(t, "")
	undigested.Scenario = "undigested"

	// Oldest: everything passes on prev (always-failing already fails).
	write(t0, passingVerdict("reviewer", "rejects", prev), prevFailing, passingVerdict("reviewer", "undigested", ""))
	// A flaky failure on prev, then a pass on prev: the newest prev result decides.
	write(t0.Add(time.Hour), passingVerdict("reviewer", "rejects", prev))
	// Newest: cur fails.
	write(t0.Add(2*time.Hour), regressed, alwaysFailing, undigested, passingVerdict("merge-bot", "green", cur))

	sum, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]CaseSummary{}
	for _, c := range sum.Cases {
		byName[c.Profile+"/"+c.Scenario] = c
	}
	r := byName["reviewer/rejects"]
	if !r.Regression || r.PreviousDigest != prev || r.PreviousAt == nil || !r.PreviousAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("reviewer/rejects passed on %s and fails on %s: want a regression from the newest prev result; got %+v", prev[:8], cur[:8], r)
	}
	if r.Latest.PromptDigest != cur || r.Latest.Pass || r.Results != 3 || r.Mode != ModeLive {
		t.Errorf("latest = %+v", r)
	}
	if c := byName["reviewer/always-failing"]; c.Regression {
		t.Error("failing on both digests is not a regression")
	}
	if c := byName["reviewer/undigested"]; c.Regression {
		t.Error("a result without a digest never marks a regression")
	}
	if c := byName["merge-bot/green"]; c.Regression || !c.Latest.Pass {
		t.Errorf("a passing case is not a regression: %+v", c)
	}
	// Failing again on the same digest (a re-run) is still the regression
	// it was, judged against the previous digest.
	write(t0.Add(3*time.Hour), regressed)
	sum, _ = LoadResults(dir)
	for _, c := range sum.Cases {
		if c.Scenario == "rejects" && !c.Regression {
			t.Error("re-running the failing digest keeps the regression marker")
		}
	}
	if sum.Cases[0].Profile != "merge-bot" {
		t.Errorf("cases sorted by profile then scenario; first = %s", sum.Cases[0].Profile)
	}
}

// TestLiveVerdictCarriesPromptDigest (#201): a live run's verdict and its
// recording's meta line carry the daemon's prompt digest, and recorded mode
// reads it back from the recording.
func TestLiveVerdictCarriesPromptDigest(t *testing.T) {
	f := newLiveFixture(t, "merge-bot/green-ci-approval")
	out := t.TempDir()
	res, err := RunLive(context.Background(), f.options(out))
	if err != nil {
		t.Fatal(err)
	}
	v := res.Report.Verdicts[0]
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(v.PromptDigest) || v.DigestSource != DigestSourceLive {
		t.Fatalf("live verdict digest = %q (%s)", v.PromptDigest, v.DigestSource)
	}
	rec := filepath.Join(out, "merge-bot", "green-ci-approval", "recording.jsonl")
	if data := readFile(t, rec); !strings.Contains(data, `"prompt_digest":"`+v.PromptDigest+`"`) {
		t.Errorf("recording meta must carry the digest:\n%s", data)
	}
	// Replayed as a fixture, the recording reports the digest it was made with.
	sc := Scenario{Profile: "merge-bot", Name: "green-ci-approval", Dir: filepath.Dir(rec)}
	mustWrite(t, filepath.Join(sc.Dir, "input.yaml"), "issue: {identifier: ENG-50}\n", 0o644)
	mustWrite(t, filepath.Join(sc.Dir, "expected.yaml"), "required_action_calls:\n  - merge_pr\n", 0o644)
	replayed := EvaluateScenario(sc)
	if replayed.PromptDigest != v.PromptDigest || replayed.DigestSource != DigestSourceRecording {
		t.Errorf("recorded mode digest = %q (%s), want %q from the recording", replayed.PromptDigest, replayed.DigestSource, v.PromptDigest)
	}
	// A hand-written fixture has none.
	fixture := EvaluateScenario(Scenario{Profile: "merge-bot", Name: "green-ci-approval", Dir: filepath.Join("fixtures", "merge-bot", "green-ci-approval")})
	if fixture.PromptDigest != "" || fixture.DigestSource != "" {
		t.Errorf("a fixture without a meta line has no digest; got %q", fixture.PromptDigest)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
