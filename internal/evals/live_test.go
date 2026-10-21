package evals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/profiles"
)

// fakeClaude is a stand-in for the claude CLI (#189 tests): it records its
// argv and the prompt it received, behaves like a minimal merge-bot through
// the sandbox's gh / itervox shims, and prints a canned stream-json
// transcript. It never calls an API. FAKE_TOKENS / FAKE_COST / FAKE_SLEEP
// let budget tests push it over a cap.
const fakeClaude = `#!/bin/sh
printf '%s\n' "$*" > "$FAKE_OUT/args-$ITERVOX_ISSUE_IDENTIFIER"
cat > "$FAKE_OUT/prompt-$ITERVOX_ISSUE_IDENTIFIER"
if [ -n "$FAKE_SLEEP" ]; then sleep "$FAKE_SLEEP"; fi
echo '{"type":"system","subtype":"init","session_id":"fake-session"}'
printf '{"type":"assistant","session_id":"fake-session","message":{"content":[{"type":"text","text":"checking the PR"}],"usage":{"input_tokens":%s,"output_tokens":10}}}\n' "${FAKE_TOKENS:-100}"
pr=$(gh pr list --state open --json number | sed -n 's/.*"number": *\([0-9][0-9]*\).*/\1/p' | head -n 1)
if [ -z "$pr" ]; then
  "$ITERVOX_BIN" action comment --body "merge-bot: no open PR found for this branch. Nothing to merge; stopping." >/dev/null
elif gh pr checks "$pr" --required >/dev/null 2>&1; then
  "$ITERVOX_BIN" action merge-pr --pr "$pr" >/dev/null
  itervox action comment --body "merge-bot: merged PR-$pr at abc1234 via squash." >/dev/null
else
  itervox action comment --body "merge-bot: refusing to merge PR-$pr — required check ci/test is failing." >/dev/null
fi
printf '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"fake-session","total_cost_usd":%s}\n' "${FAKE_COST:-0.01}"
`

// liveFixture is a throwaway copy of the eval project plus a chosen set of
// real fixtures, and a fake claude CLI. A decoy `claude` first on PATH
// records any attempt to run the real CLI by name.
type liveFixture struct {
	Fixtures, Workflow, FakeOut, Fake, Decoy string
}

func newLiveFixture(t *testing.T, scenarios ...string) liveFixture {
	t.Helper()
	tmp := t.TempDir()
	f := liveFixture{
		Fixtures: filepath.Join(tmp, "fixtures"),
		Workflow: filepath.Join(tmp, "project", "WORKFLOW.md"),
		FakeOut:  filepath.Join(tmp, "fake-out"),
		Fake:     filepath.Join(tmp, "fakebin", "claude"),
		Decoy:    filepath.Join(tmp, "decoy-invoked"),
	}
	copyTree(t, "project", filepath.Join(tmp, "project"))
	for _, sc := range scenarios {
		copyTree(t, filepath.Join("fixtures", sc), filepath.Join(f.Fixtures, sc))
	}
	mustWrite(t, f.Fake, fakeClaude, 0o755)
	decoyDir := filepath.Join(tmp, "decoybin")
	mustWrite(t, filepath.Join(decoyDir, "claude"), "#!/bin/sh\ntouch "+f.Decoy+"\nexit 97\n", 0o755)
	if err := os.MkdirAll(f.FakeOut, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoyDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_OUT", f.FakeOut)
	t.Setenv(LiveOptInEnv, "1")
	t.Cleanup(func() {
		if _, err := os.Stat(f.Decoy); err == nil {
			t.Error("a test reached the `claude` on PATH; live tests must pass the fake CLI explicitly")
		}
	})
	return f
}

func (f liveFixture) options(out string) LiveOptions {
	return LiveOptions{
		FixturesRoot: f.Fixtures,
		Workflow:     f.Workflow,
		Command:      f.Fake,
		Backend:      "claude",
		OutDir:       out,
		Budget:       Budget{MaxTurns: 3, Timeout: 30 * time.Second},
	}
}

func TestLiveModeRecordsAndJudgesWithFakeBackend(t *testing.T) {
	f := newLiveFixture(t, "merge-bot/green-ci-approval", "merge-bot/red-ci-approval", "merge-bot/no-matching-pr")
	// A project rules layer, to prove the composed prompt carries the
	// resolved rules exactly where a dispatched worker gets them.
	mustWrite(t, filepath.Join(filepath.Dir(f.Workflow), ".itervox", "rules.json"),
		`{"rules":[{"id":"no-force-push","severity":"must","text":"Never force-push."}]}`, 0o644)
	out := t.TempDir()

	res, err := RunLive(context.Background(), f.options(out))
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if pass, total := res.Report.Aggregate(); pass != total || total != 3 {
		t.Fatalf("want 3/3 live scenarios to pass; got %d/%d\n%s", pass, total, res.Report.Format())
	}

	// The transcript is recorded in the replayable recording format.
	green := filepath.Join(out, "merge-bot", "green-ci-approval", "recording.jsonl")
	data, err := os.ReadFile(green)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"meta"`, `"source":"live"`, `"prompt_sha256"`, `{"event":"action","action":"merge_pr","args":{"pr":"42"}}`, `"body":"merge-bot: merged PR-42 at abc1234 via squash."`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("green recording missing %s:\n%s", want, data)
		}
	}
	rec, err := LoadRecordingFile(green)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.Actions, ",") != "merge_pr,comment" || len(rec.Comments) != 1 {
		t.Errorf("recording must replay as merge_pr then comment; got actions=%v comments=%v", rec.Actions, rec.Comments)
	}
	red, err := LoadRecordingFile(filepath.Join(out, "merge-bot", "red-ci-approval", "recording.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(red.Actions, ",") != "comment" {
		t.Errorf("red CI must only comment (the gh shim reported the failing check); got %v", red.Actions)
	}

	// The prompt is the daemon's own assembly: WORKFLOW.md body, operator
	// envelope, rules, the built-in merge-bot SOUL and INSTRUCTIONS, the
	// automation's instructions with the trigger comment, the action notice.
	prompt := readFile(t, filepath.Join(f.FakeOut, "prompt-ENG-51"))
	order := []string{
		"You are working on issue **ENG-51: Red CI with approval comment**",
		"## Operator Reply Channel",
		"## Rules",
		"Never force-push.",
		"You are merge-bot — the final gate before code reaches main.",
		"You operate the final merge gate.",
		"> AI review passed — ready for merge",
		"`itervox action merge-pr --pr <number>`",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(prompt[at:], want)
		if i < 0 {
			t.Fatalf("prompt missing %q after offset %d:\n%s", want, at, prompt)
		}
		at += i + len(want)
	}
	if args := readFile(t, filepath.Join(f.FakeOut, "args-ENG-51")); !strings.Contains(args, "--max-turns 3") || !strings.Contains(args, "--output-format stream-json") {
		t.Errorf("fake CLI must be invoked through the claude runner with the turn cap; args: %s", args)
	}
}

func TestLiveModeRefusesWithoutOptIn(t *testing.T) {
	f := newLiveFixture(t, "merge-bot/green-ci-approval")
	for _, value := range []string{"", "0", "true", "yes"} {
		t.Setenv(LiveOptInEnv, value)
		_, err := RunLive(context.Background(), f.options(t.TempDir()))
		if !errors.Is(err, ErrLiveNotEnabled) {
			t.Errorf("%s=%q: want ErrLiveNotEnabled; got %v", LiveOptInEnv, value, err)
		}
	}
	if entries, _ := os.ReadDir(f.FakeOut); len(entries) != 0 {
		t.Errorf("the backend CLI must not run without the opt-in; it left %d files", len(entries))
	}
}

func TestLiveModeEnforcesBudget(t *testing.T) {
	t.Run("token cap", func(t *testing.T) {
		f := newLiveFixture(t, "merge-bot/green-ci-approval")
		t.Setenv("FAKE_TOKENS", "50000")
		opts := f.options(t.TempDir())
		opts.Budget.MaxTokens = 1000
		res, err := RunLive(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		v := res.Report.Verdicts[0]
		if v.Pass() || !strings.Contains(v.Error, "budget") || !strings.Contains(v.Error, "tokens") {
			t.Errorf("a run over the token cap must fail with a budget error; got %+v", v)
		}
	})
	t.Run("wall-clock timeout", func(t *testing.T) {
		f := newLiveFixture(t, "merge-bot/green-ci-approval")
		t.Setenv("FAKE_SLEEP", "20")
		opts := f.options(t.TempDir())
		opts.Budget.Timeout = 700 * time.Millisecond
		start := time.Now()
		res, err := RunLive(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took > 15*time.Second {
			t.Errorf("the timeout must stop the agent; the run took %s", took)
		}
		v := res.Report.Verdicts[0]
		if v.Pass() || !strings.Contains(v.Error, "timeout") {
			t.Errorf("a run past its wall-clock budget must fail with a timeout; got %+v", v)
		}
	})
	t.Run("suite cost cap", func(t *testing.T) {
		f := newLiveFixture(t, "merge-bot/green-ci-approval", "merge-bot/red-ci-approval")
		t.Setenv("FAKE_COST", "0.75")
		opts := f.options(t.TempDir())
		opts.Budget.MaxCostUSD = 0.5
		res, err := RunLive(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Runs) != 1 {
			t.Errorf("only the first scenario may run once the cost cap is spent; ran %d", len(res.Runs))
		}
		second := res.Report.Verdicts[1]
		if second.Pass() || !strings.Contains(second.Error, "suite cost cap") {
			t.Errorf("the scenario after the cap must fail unrun; got %+v", second)
		}
		if _, err := os.Stat(filepath.Join(f.FakeOut, "args-ENG-51")); err == nil {
			t.Error("the backend must not start once the suite budget is spent")
		}
	})
}

func TestRecordCandidateNeverOverwritesFixture(t *testing.T) {
	f := newLiveFixture(t, "merge-bot/green-ci-approval")
	dir := filepath.Join(f.Fixtures, "merge-bot", "green-ci-approval")
	before := readFile(t, filepath.Join(dir, "recording.jsonl"))
	opts := f.options("")
	opts.Candidate = true

	res, err := RunLive(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report.Verdicts[0].Pass() {
		t.Fatalf("candidate run should pass:\n%s", res.Report.Format())
	}
	if after := readFile(t, filepath.Join(dir, "recording.jsonl")); after != before {
		t.Fatalf("re-recording must never touch the promoted fixture\nbefore:\n%s\nafter:\n%s", before, after)
	}
	candidate := readFile(t, filepath.Join(dir, CandidateName))
	if !strings.Contains(candidate, `"source":"live"`) || res.Runs[0].RecordingPath != filepath.Join(dir, CandidateName) {
		t.Errorf("the live run must land in %s; got path %q:\n%s", CandidateName, res.Runs[0].RecordingPath, candidate)
	}

	// A run that fails its budget never becomes a candidate.
	if err := os.Remove(filepath.Join(dir, CandidateName)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_TOKENS", "50000")
	opts.Budget.MaxTokens = 1000
	if _, err := RunLive(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, CandidateName)); !os.IsNotExist(err) {
		t.Errorf("an over-budget run must not leave a candidate (stat err: %v)", err)
	}
	if after := readFile(t, filepath.Join(dir, "recording.jsonl")); after != before {
		t.Error("a failed re-record must not touch the promoted fixture")
	}
}

func TestPromoteCandidate(t *testing.T) {
	setup := func(t *testing.T, candidate string) (Scenario, string) {
		t.Helper()
		tmp := t.TempDir()
		copyTree(t, filepath.Join("fixtures", "reviewer", "rejects-and-moves-back"), filepath.Join(tmp, "reviewer", "rejects-and-moves-back"))
		scenarios, err := LoadScenarios(tmp, "reviewer")
		if err != nil || len(scenarios) != 1 {
			t.Fatalf("LoadScenarios: %v (%d)", err, len(scenarios))
		}
		if candidate != "" {
			mustWrite(t, CandidatePath(scenarios[0]), candidate, 0o644)
		}
		return scenarios[0], readFile(t, RecordingPath(scenarios[0]))
	}
	good := `{"event":"meta","source":"live"}
{"event":"action","action":"comment","args":{}}
{"event":"comment","body":"AI review failed — 1 blocking finding:\n1. Missing test."}
{"event":"action","action":"move_state","args":{"state":"In Progress"}}
`
	bad := good + `{"event":"comment","body":"/ai-approved"}` + "\n"

	t.Run("passing candidate is promoted", func(t *testing.T) {
		sc, _ := setup(t, good)
		if _, err := PromoteCandidate(sc, false); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, RecordingPath(sc)); got != good {
			t.Errorf("recording.jsonl must be the candidate; got:\n%s", got)
		}
		if _, err := os.Stat(CandidatePath(sc)); !os.IsNotExist(err) {
			t.Error("the promoted candidate must be gone")
		}
	})
	t.Run("failing candidate is refused", func(t *testing.T) {
		sc, before := setup(t, bad)
		v, err := PromoteCandidate(sc, false)
		if err == nil || !strings.Contains(err.Error(), "forbidden phrase present in comments: /ai-approved") || v.Pass() {
			t.Fatalf("a candidate failing its judges must be refused; err=%v verdict=%+v", err, v)
		}
		if readFile(t, RecordingPath(sc)) != before {
			t.Error("a refused promotion must leave recording.jsonl untouched")
		}
	})
	t.Run("force promotes a failing candidate", func(t *testing.T) {
		sc, _ := setup(t, bad)
		if _, err := PromoteCandidate(sc, true); err != nil {
			t.Fatal(err)
		}
		if readFile(t, RecordingPath(sc)) != bad {
			t.Error("force must promote the candidate as-is")
		}
	})
	t.Run("no candidate", func(t *testing.T) {
		sc, _ := setup(t, "")
		if _, err := PromoteCandidate(sc, false); !errors.Is(err, ErrNoCandidate) {
			t.Errorf("want ErrNoCandidate; got %v", err)
		}
	})
}

// TestAbsenceCheckFailsWhenPhrasePresent pins the README's absence check:
// the reviewer's reject path must never carry the "/ai-approved" marker.
// The committed fixture passes; the same fixture with the marker injected
// into a comment fails on exactly that phrase.
func TestAbsenceCheckFailsWhenPhrasePresent(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "reviewer", "rejects-and-moves-back")
	copyTree(t, filepath.Join("fixtures", "reviewer", "rejects-and-moves-back"), dir)
	scenarios, err := LoadScenarios(tmp, "reviewer")
	if err != nil || len(scenarios) != 1 {
		t.Fatalf("LoadScenarios: %v (%d)", err, len(scenarios))
	}
	sc := scenarios[0]
	if v := EvaluateScenario(sc); !v.Pass() {
		t.Fatalf("the committed fixture must pass before injection: %+v", v)
	}
	if got := withExpectation(sc).Expect.ForbiddenPhrases; !strings.Contains(strings.Join(got, "\n"), "/ai-approved") {
		t.Fatalf("expected.yaml must declare /ai-approved as a forbidden phrase; got %q", got)
	}

	f, err := os.OpenFile(RecordingPath(sc), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":"comment","body":"Leaving /ai-approved anyway"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	v := EvaluateScenario(sc)
	if v.Pass() || v.Structural.Pass {
		t.Fatalf("a comment carrying a forbidden phrase must fail the structural judge: %+v", v)
	}
	if v.Structural.Reason != "forbidden phrase present in comments: /ai-approved" {
		t.Errorf("reason must name the phrase; got %q", v.Structural.Reason)
	}
	if !strings.Contains(EvaluateAll([]Scenario{sc}).Format(), "forbidden phrase present in comments: /ai-approved") {
		t.Error("the report must surface the absence failure")
	}
}

func TestSandboxEventsFromCallsLog(t *testing.T) {
	log := strings.Join([]string{
		"gh" + fieldSep + "pr" + fieldSep + "view" + fieldSep + "51",
		"gh" + fieldSep + "pr" + fieldSep + "review" + fieldSep + "61" + fieldSep + "--comment" + fieldSep + "--body" + fieldSep + "/ai-approved",
		"gh" + fieldSep + "pr" + fieldSep + "comment" + fieldSep + "61" + fieldSep + "--body-file" + fieldSep + "/tmp/x.md" + contentSep + "from a file",
		"gh" + fieldSep + "pr" + fieldSep + "merge" + fieldSep + "61",
		"itervox" + fieldSep + "action" + fieldSep + "move-state" + fieldSep + "--state=In Progress",
	}, recordSep) + recordSep
	var got []string
	for _, e := range sandboxEvents(parseCallsLog(log)) {
		got = append(got, e.Event+":"+e.Action+e.Body)
	}
	want := []string{"action:gh_pr_review", "comment:/ai-approved", "action:gh_pr_comment", "comment:from a file", "action:gh_pr_merge", "action:move_state"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestEvalProjectCoversEveryFixture keeps the eval project and the fixtures
// in step: every fixture's input.yaml parses, and every fixture profile is a
// profile of project/WORKFLOW.md with its SOUL.md and INSTRUCTIONS.md loaded
// (merge-bot from the shipped built-in, reviewer from project/agents/).
func TestEvalProjectCoversEveryFixture(t *testing.T) {
	cfg, err := config.Load(filepath.Join("project", "WORKFLOW.md"))
	if err != nil {
		t.Fatalf("load eval project: %v", err)
	}
	scenarios, err := LoadScenarios("fixtures", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) < 8 {
		t.Fatalf("expected every merge-bot and reviewer fixture; got %d", len(scenarios))
	}
	for _, sc := range scenarios {
		in, err := ParseInput(sc.Dir)
		if err != nil {
			t.Errorf("%s/%s: %v", sc.Profile, sc.Name, err)
			continue
		}
		if in.IssueIdentifier == "" || in.BranchName == "" {
			t.Errorf("%s/%s: input.yaml must set issue.identifier and issue.branch_name", sc.Profile, sc.Name)
		}
		p, ok := cfg.Agent.Profiles[sc.Profile]
		if !ok || p.Soul == "" || p.Instructions == "" {
			t.Errorf("%s/%s: profile %q missing from the eval project or without SOUL/INSTRUCTIONS", sc.Profile, sc.Name, sc.Profile)
		}
	}
	if bp := profiles.Lookup("merge-bot"); cfg.Agent.Profiles["merge-bot"].Soul != bp.Soul {
		t.Error("merge-bot must evaluate the shipped built-in SOUL.md")
	}
	if !strings.Contains(cfg.Agent.Profiles["reviewer"].Instructions, "/ai-approved") {
		t.Error("the reviewer profile must carry the merge-handshake instructions")
	}
	noPR, err := ParseInput(filepath.Join("fixtures", "merge-bot", "no-matching-pr"))
	if err != nil {
		t.Fatal(err)
	}
	if !noPR.HasCandidates || len(noPR.pullRequests()) != 0 {
		t.Errorf("an explicit empty pr.candidates must mean no open PR; got %+v", noPR.pullRequests())
	}
}
