package referee

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/referee/adapters"
)

// diffPolicy configures one generic tool, fake, whose command lives at
// tools/fake/bin/fake in each checkout (gitignored: it is installed, not
// committed). Its probes are what the tests compare.
const diffPolicy = `tools:
  fake:
    guard: deploy-guard.txt
    env_scan: tools/fake/**/*
    deploy_vars: [FAKE_*]
    probes:
      - fake --version
      - fake deploy --help
      - fake env
`

// diffRepo is a repository whose main branch has the policy, a branch
// "bump" that changes a lockfile (a dependency bump) and a second checkout
// of main at base (the base install's directory). Neither has the tool
// installed yet.
func diffRepo(t *testing.T, policy string) (r testRepo, base string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("referee diff runs on Linux and macOS")
	}
	r = newTestRepo(t)
	files := map[string]string{
		".gitignore":       "tools/\nhang.pid\n",
		"deploy-guard.txt": "FAKE_TARGET\n",
		"package.json":     `{"dependencies":{"fake":"1.0.0"}}` + "\n",
	}
	if policy != "" {
		files[".referee.yml"] = policy
	}
	r.commit("base", files)
	r.git("checkout", "-q", "-b", "bump")
	r.commit("bump fake", map[string]string{"package.json": `{"dependencies":{"fake":"1.1.0"}}` + "\n"})
	base = filepath.Join(t.TempDir(), "base")
	r.git("worktree", "add", "-q", "--detach", base, "main")
	return r, base
}

// installFake installs a testdata/difftools script as the fake tool in dir.
func installFake(t *testing.T, dir, script string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "difftools", script))
	require.NoError(t, err)
	bin := filepath.Join(dir, "tools", "fake", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fake"), raw, 0o755))
}

func diffJSON(t *testing.T, out string) diffReport {
	t.Helper()
	var rep diffReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep), out)
	return rep
}

func findProbe(t *testing.T, rep diffReport, text string) diffProbe {
	t.Helper()
	for _, tool := range rep.Tools {
		for _, p := range tool.Probes {
			if p.Probe == text {
				return p
			}
		}
	}
	t.Fatalf("no probe %q in %+v", text, rep.Tools)
	return diffProbe{}
}

// TestDiffSameBehaviourExitsZero: a version bump that changes only the
// version, timestamps, durations, colour and paths is not a behaviour
// change. The env probe also differs only in the fresh HOME and the
// checkout path, both normalised.
func TestDiffSameBehaviourExitsZero(t *testing.T) {
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-1.1.0")
	code, out, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base)
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "diff: no behaviour change in 3 probe(s)")
	assert.Contains(t, out, "[fake] fake deploy --help: unchanged (exit 0)")
}

// TestDiffChangedOutputExitsSecondOpinion: a new flag in the deploy help
// is a change a person should look at (exit 10), with the new line.
func TestDiffChangedOutputExitsSecondOpinion(t *testing.T) {
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-newflag")
	summary := filepath.Join(t.TempDir(), "summary.md")
	code, out, errOut := referee(t, map[string]string{"GITHUB_STEP_SUMMARY": summary}, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base)
	require.Equal(t, exitSecondOpinion, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "diff: 1 of 3 probe(s) changed behaviour")
	assert.Contains(t, out, "[fake] fake deploy --help: changed (output)")
	assert.Contains(t, out, "      +   --target <url>  deploy to another deployment")
	md, err := os.ReadFile(summary)
	require.NoError(t, err)
	assert.Contains(t, string(md), "**1 of 3 probe(s) changed behaviour**")
	assert.Contains(t, string(md), "+   --target <url>  deploy to another deployment")

	code, out, _ = referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--json")
	require.Equal(t, exitSecondOpinion, code)
	rep := diffJSON(t, out)
	assert.Equal(t, 1, rep.Version)
	assert.Equal(t, exitSecondOpinion, rep.ExitCode)
	p := findProbe(t, rep, "fake deploy --help")
	assert.True(t, p.Changed)
	assert.Equal(t, []string{"output"}, p.Changes)
	assert.Equal(t, []string{"  --target <url>  deploy to another deployment"}, p.Added)
	assert.Equal(t, 1, p.AddedCount)
	assert.Empty(t, p.Removed)
	assert.Empty(t, p.Prompts)
	assert.Equal(t, "tools/fake/bin/fake", p.Head.Executable)
	assert.False(t, findProbe(t, rep, "fake --version").Changed, "a version-only change is normalised away")
}

// shortIdle shortens the idle window that takes a silent probe to be
// waiting for input, for one test.
func shortIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := probeIdleWindow
	probeIdleWindow = d
	t.Cleanup(func() { probeIdleWindow = old })
}

// TestDiffNewPromptIsFlagged: the head's deploy now asks "[y/N]". Its stdin
// is an open pipe nothing writes to, so it waits for an answer that never
// comes (it never sees end of input): the idle window kills it, and the
// question is reported as a new prompt.
func TestDiffNewPromptIsFlagged(t *testing.T) {
	shortIdle(t, 500*time.Millisecond)
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-prompt")
	code, out, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--json")
	require.Equal(t, exitSecondOpinion, code, "stdout=%s stderr=%s", out, errOut)
	p := findProbe(t, diffJSON(t, out), "fake deploy --help")
	assert.Equal(t, []string{"exit_code", "waiting_for_input", "output", "prompt"}, p.Changes)
	assert.Equal(t, []string{"Deploy to production? [y/N]"}, p.Prompts)
	assert.Equal(t, -1, p.Head.ExitCode)
	assert.False(t, p.Head.TimedOut)
	assert.True(t, p.Head.WaitingForInput)
	assert.Equal(t, "Deploy to production? [y/N]", p.Head.Prompt)
	assert.NotContains(t, p.Added, "Aborted: no answer.", "stdin never reached end of input")
	assert.NotContains(t, strings.Join(p.Added, "\n"), "answer: ", "the prompt was never answered")

	code, out, _ = referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base)
	require.Equal(t, exitSecondOpinion, code)
	assert.Contains(t, out, "new prompt: Deploy to production? [y/N]")
	assert.Contains(t, out, "base exit 0, head waiting for input")
}

// TestDiffWaitingForInputIsAPrompt: the head's deploy prints a question
// with no newline and no prompt pattern ("Deploy to production? ") and
// reads stdin. It is seen waiting within the idle window, far before the
// timeout, its process group (with the child it started) is killed, and
// its last line is reported as a new prompt: exit 10 against a base that
// exited on its own.
func TestDiffWaitingForInputIsAPrompt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	shortIdle(t, 500*time.Millisecond)
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-wait")
	summary := filepath.Join(t.TempDir(), "summary.md")
	start := time.Now()
	code, out, errOut := referee(t, map[string]string{"GITHUB_STEP_SUMMARY": summary},
		"diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--timeout", "20s", "--json")
	require.Equal(t, exitSecondOpinion, code, "stdout=%s stderr=%s", out, errOut)
	assert.Less(t, time.Since(start), 10*time.Second, "the waiting probe must be seen at the idle window, not the timeout")
	rep := diffJSON(t, out)
	assert.Equal(t, 1, rep.Version)
	assert.Equal(t, exitSecondOpinion, rep.ExitCode)
	p := findProbe(t, rep, "fake deploy --help")
	assert.True(t, p.Head.WaitingForInput)
	assert.False(t, p.Head.TimedOut)
	assert.Equal(t, -1, p.Head.ExitCode)
	assert.Equal(t, "Deploy to production?", p.Head.Prompt)
	assert.Less(t, p.Head.DurationMs, int64(5000))
	assert.False(t, p.Base.WaitingForInput)
	assert.Empty(t, p.Base.Prompt)
	assert.Equal(t, 0, p.Base.ExitCode)
	assert.Equal(t, []string{"exit_code", "waiting_for_input", "output", "prompt"}, p.Changes)
	assert.Equal(t, []string{"Deploy to production?"}, p.Prompts)
	assert.NotContains(t, strings.Join(p.Added, "\n"), "answer: ", "the prompt was never answered")
	md, err := os.ReadFile(summary)
	require.NoError(t, err)
	assert.Contains(t, string(md), "new prompt: `Deploy to production?`")
	assert.Contains(t, string(md), "head waiting for input")

	raw, err := os.ReadFile(filepath.Join(r.dir, "hang.pid"))
	require.NoError(t, err, "the waiting probe wrote its child's pid")
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return !processAlive(pid) }, 3*time.Second, 20*time.Millisecond,
		"the waiting probe's child %d survived the kill", pid)

	// Both sides waiting on the same question is not a change.
	installFake(t, base, "fake-wait")
	code, out, errOut = referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--json")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	p = findProbe(t, diffJSON(t, out), "fake deploy --help")
	assert.False(t, p.Changed)
	assert.True(t, p.Base.WaitingForInput)
	assert.True(t, p.Head.WaitingForInput)
}

// TestProbeSteadyOutputIsNotWaiting: a probe that prints a line every
// 0.2s for longer than the idle window is not waiting for input: each
// write restarts the window, and it runs to its own exit.
func TestProbeSteadyOutputIsNotWaiting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("referee diff runs on Linux and macOS")
	}
	shortIdle(t, 600*time.Millisecond)
	dir := t.TempDir()
	installFake(t, dir, "fake-slow")
	exe := filepath.Join(dir, "tools", "fake", "bin", "fake")
	run, err := runProbe(context.Background(), dir, exe, []string{"fake", "deploy"}, 20*time.Second,
		func(string) bool { return false }, withRealPath(dir, "<checkout>"))
	require.NoError(t, err)
	assert.False(t, run.WaitingForInput, "steady output must reset the idle window")
	assert.False(t, run.TimedOut)
	assert.Equal(t, 0, run.ExitCode)
	assert.Empty(t, run.Prompt)
	assert.Greater(t, run.DurationMs, int64(600), "it ran longer than one idle window")
	require.Len(t, run.lines, 9)
	assert.Equal(t, "done", run.lines[8])
}

// TestProbeSilentWaitHasAPlaceholderPrompt: a probe that waits on stdin
// without printing anything is still waiting for input, reported with a
// placeholder prompt.
func TestProbeSilentWaitHasAPlaceholderPrompt(t *testing.T) {
	assert.Equal(t, noOutputPrompt, lastPromptLine(nil))
	assert.Equal(t, noOutputPrompt, lastPromptLine([]string{"", "  "}))
	assert.Equal(t, "Name:", lastPromptLine([]string{"Name:", ""}))
	p := compareRuns(adaptersProbe("fake deploy"),
		probeRun{},
		probeRun{ExitCode: -1, WaitingForInput: true, Prompt: noOutputPrompt})
	assert.Equal(t, []string{"exit_code", "waiting_for_input", "prompt"}, p.Changes)
	assert.Equal(t, []string{noOutputPrompt}, p.Prompts)
}

// TestDiffTimedOutQuestionIsAPrompt: a head run that kept waiting after a
// question (not one of the prompt patterns) until the timeout killed it
// was waiting for an answer: reported as a prompt.
func TestDiffTimedOutQuestionIsAPrompt(t *testing.T) {
	p := compareRuns(
		adaptersProbe("fake deploy"),
		probeRun{lines: []string{"Deploying."}},
		probeRun{ExitCode: -1, TimedOut: true, lines: []string{"Deploying.", "Which deployment should receive this?"}},
	)
	assert.Equal(t, []string{"exit_code", "timed_out", "output", "prompt"}, p.Changes)
	assert.Equal(t, []string{"Which deployment should receive this?"}, p.Prompts)

	// Timing out on both sides is not a new prompt.
	p = compareRuns(adaptersProbe("fake deploy"),
		probeRun{ExitCode: -1, TimedOut: true, lines: []string{"Name:"}},
		probeRun{ExitCode: -1, TimedOut: true, lines: []string{"Name:"}})
	assert.False(t, p.Changed)
}

// TestDiffExitCodeChange: the same output with a different exit code.
func TestDiffExitCodeChange(t *testing.T) {
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-exit")
	code, out, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--json")
	require.Equal(t, exitSecondOpinion, code, "stdout=%s stderr=%s", out, errOut)
	p := findProbe(t, diffJSON(t, out), "fake deploy --help")
	assert.Equal(t, []string{"exit_code"}, p.Changes)
	assert.Equal(t, 0, p.Base.ExitCode)
	assert.Equal(t, 3, p.Head.ExitCode)
}

// TestDiffTimeoutKillsProcessGroup: a probe that hangs is killed at the
// timeout together with the child it started, and reported as timed out.
func TestDiffTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	r, base := diffRepo(t, diffPolicy)
	installFake(t, base, "fake-1.0.0")
	installFake(t, r.dir, "fake-hang")
	start := time.Now()
	code, out, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base, "--timeout", "1s", "--json")
	require.Equal(t, exitSecondOpinion, code, "stdout=%s stderr=%s", out, errOut)
	assert.Less(t, time.Since(start), 30*time.Second, "the hanging probe must not run to its own end")
	p := findProbe(t, diffJSON(t, out), "fake deploy --help")
	assert.True(t, p.Head.TimedOut)
	assert.Equal(t, -1, p.Head.ExitCode)
	assert.Contains(t, p.Changes, "timed_out")

	raw, err := os.ReadFile(filepath.Join(r.dir, "hang.pid"))
	require.NoError(t, err, "the hanging probe wrote its child's pid")
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return !processAlive(pid) }, 3*time.Second, 20*time.Millisecond,
		"the probe's child %d survived the timeout", pid)
}

// processAlive reports whether pid is a live (not zombie) process.
func processAlive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// pid (comm) S …: the state follows the last ')'.
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

// TestDiffProbeEnvironmentIsScrubbed: a probe that prints its environment
// shows none of the runner's variables (deploy-target, credentials, CI),
// only the fixed list, with HOME a fresh directory.
func TestDiffProbeEnvironmentIsScrubbed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("referee diff runs on Linux and macOS")
	}
	t.Setenv("FAKE_DEPLOY_KEY", "deploy-key-value")
	t.Setenv("FAKE_TARGET", "prod")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret-value")
	t.Setenv("GITHUB_TOKEN", "github-token-value")
	t.Setenv("CI", "true")
	dir := t.TempDir()
	installFake(t, dir, "fake-1.0.0")
	exe := filepath.Join(dir, "tools", "fake", "bin", "fake")
	deployVar := func(name string) bool { return strings.HasPrefix(name, "FAKE_") }
	run, err := runProbe(context.Background(), dir, exe, []string{"fake", "env"}, 10*time.Second, deployVar, withRealPath(dir, "<checkout>"))
	require.NoError(t, err)
	require.Equal(t, 0, run.ExitCode)
	got := strings.Join(run.lines, "\n")
	for _, leaked := range []string{"FAKE_", "AWS_", "GITHUB_", "CI=", "deploy-key-value", "aws-secret-value", "github-token-value"} {
		assert.NotContains(t, got, leaked)
	}
	assert.Contains(t, run.lines, "HOME=<home>")
	assert.Contains(t, run.lines, "TMPDIR=<home>/tmp")
	assert.Contains(t, run.lines, "LANG=C")
	allowed := map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}
	for _, n := range envNames(probeEnv("", "", func(string) bool { return false })) {
		allowed[n] = true
	}
	for _, l := range run.lines {
		name, _, _ := strings.Cut(l, "=")
		assert.True(t, allowed[name], "unexpected variable %s in the probe's environment", name)
	}

	// A deploy_vars glob that matches a variable of the fixed list drops it too.
	env := probeEnv("/h", "/bin", func(n string) bool { return n == "HOME" || n == "PATH" })
	assert.NotContains(t, envNames(env), "HOME")
	assert.NotContains(t, envNames(env), "PATH")
}

// TestDiffMissingInstallFailsClosed: no tool in the base checkout (or in
// the head) is an error before anything runs, never a pass.
func TestDiffMissingInstallFailsClosed(t *testing.T) {
	r, base := diffRepo(t, diffPolicy)
	installFake(t, r.dir, "fake-1.0.0")
	code, _, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", base)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "tools.fake: the base install: the install root tools/fake is not a directory")

	r2, base2 := diffRepo(t, diffPolicy)
	installFake(t, base2, "fake-1.0.0")
	code, _, errOut = referee(t, nil, "diff", "--dir", r2.dir, "--base", "main", "--base-dir", base2)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "tools.fake: the head install")

	// An install root without the command is missing too.
	require.NoError(t, os.MkdirAll(filepath.Join(r2.dir, "tools", "fake", "lib"), 0o755))
	code, _, errOut = referee(t, nil, "diff", "--dir", r2.dir, "--base", "main", "--base-dir", base2)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "no executable tools/fake/bin/fake or tools/fake/fake")

	code, _, errOut = referee(t, nil, "diff", "--dir", r2.dir, "--base", "main")
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "--base-dir is required")
}

// TestDiffBaseDirMustBeTheBase: a --base-dir that is the head itself, or a
// checkout of another commit, is an error: it would compare the wrong
// installs and could pass for the wrong reason.
func TestDiffBaseDirMustBeTheBase(t *testing.T) {
	r, _ := diffRepo(t, diffPolicy)
	installFake(t, r.dir, "fake-1.0.0")
	code, _, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", r.dir)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "is the head's own checkout")

	other := filepath.Join(t.TempDir(), "other")
	r.git("worktree", "add", "-q", "--detach", other, "bump")
	installFake(t, other, "fake-1.0.0")
	code, _, errOut = referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", other)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "not the merge base")

	notGit := t.TempDir()
	code, _, errOut = referee(t, nil, "diff", "--dir", r.dir, "--base", "main", "--base-dir", notGit)
	require.Equal(t, exitError, code)
	assert.Contains(t, errOut, "is not a git checkout")
}

// TestDiffNoToolsExitsZero: without tools: on the base ref there is nothing
// to compare, and no --base-dir is needed.
func TestDiffNoToolsExitsZero(t *testing.T) {
	for _, policy := range []string{"", "human: [ci/**]\n"} {
		r, _ := diffRepo(t, policy)
		code, out, errOut := referee(t, nil, "diff", "--dir", r.dir, "--base", "main")
		require.Equal(t, exitOK, code, "policy %q: stdout=%s stderr=%s", policy, out, errOut)
		assert.Contains(t, out, "diff: no tools: configured")
	}
}

// TestDiffPolicyComesFromTheBase: tools: added on the head is not used.
func TestDiffPolicyComesFromTheBase(t *testing.T) {
	r, _ := diffRepo(t, "")
	r.commit("add tools on the head", map[string]string{".referee.yml": diffPolicy})
	code, out, _ := referee(t, nil, "diff", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code)
	assert.Contains(t, out, "no tools: configured (policy: none (.referee.yml not on the base ref))")
}

func TestNormalizeOutput(t *testing.T) {
	raw := "\x1b[32mfake\x1b[0m v2.3.4-beta.1 ready\r\n" +
		"building 10%\rbuilding 100%\n" +
		"at 2026-11-20T10:11:12.345Z (took 1.25s, 300ms) id 0123456789abcdef0123\n" +
		"cache /tmp/fake-abc/x.json and /home/u/repo/src\n\n\n"
	got := normalizeOutput([]byte(raw), []pathPlaceholder{{"/home/u/repo", "<checkout>"}})
	assert.Equal(t, []string{
		"fake <version> ready",
		"building 100%",
		"at <time> (took <duration>, <duration>) id <hex>",
		"cache <tmp> and <checkout>/src",
	}, got)
}

func TestLineDiffAndPrompts(t *testing.T) {
	added, removed := lineDiff([]string{"a", "b", "b", "c"}, []string{"c", "b", "a", "d"})
	assert.Equal(t, []string{"d"}, added)
	assert.Equal(t, []string{"b"}, removed)

	for _, l := range []string{"Continue? [y/N]", "Overwrite (yes/no)", "? Are you sure you want to deploy", "Do you want to continue", "Press Enter to open the browser"} {
		assert.True(t, isPrompt(l), l)
	}
	for _, l := range []string{"  --yes  skip the confirmation", "Usage: fake deploy", "Did you know?"} {
		assert.False(t, isPrompt(l), l)
	}
	assert.Equal(t, []string{"Continue? [y/N]"}, newPrompts([]string{"? Pick one"}, []string{"? Pick one", "Continue? [y/N]"}))
}

func adaptersProbe(text string) adapters.Probe {
	return adapters.Probe{Text: text, Argv: strings.Fields(text)}
}
