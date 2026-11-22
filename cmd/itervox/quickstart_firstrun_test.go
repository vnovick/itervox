//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// fakeAgentBin writes agent CLI stand-ins into a fresh directory. Each one
// records that it ran in <dir>/ran-<name> and fails: quickstart must find
// them by name and never run them.
func fakeAgentBin(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, n := range names {
		script := "#!/bin/sh\ntouch '" + filepath.Join(bin, "ran-"+n) + "'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, n), []byte(script), 0o755))
	}
	return bin
}

// assertNoAgentRan fails when any fake agent CLI in bin was executed.
func assertNoAgentRan(t *testing.T, bin string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(bin, "ran-*"))
	assert.Empty(t, matches, "an agent CLI was executed")
}

// realLookPath undoes a stubLookPath for tests that use a fake PATH.
func realLookPath(t *testing.T) {
	t.Helper()
	orig := quickstartLookPath
	quickstartLookPath = exec.LookPath
	t.Cleanup(func() { quickstartLookPath = orig })
}

// noModelDiscovery keeps scaffolding off the providers' model APIs.
func noModelDiscovery(t *testing.T) {
	t.Helper()
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })
}

// noDaemonStart fails the test if quickstart tries to start a daemon.
func noDaemonStart(t *testing.T) {
	t.Helper()
	orig := quickstartDaemonCommand
	quickstartDaemonCommand = func(string) (*exec.Cmd, error) {
		t.Error("quickstart started a daemon")
		return nil, errors.New("no daemon in this test")
	}
	t.Cleanup(func() { quickstartDaemonCommand = orig })
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// TestQuickstartDetectsAgentCLIsOnAFakePATH: the four agent CLIs are found
// by name on PATH, absent ones are reported as not found, and none is run.
func TestQuickstartDetectsAgentCLIsOnAFakePATH(t *testing.T) {
	realLookPath(t)
	bin := fakeAgentBin(t, "claude", "opencode")
	t.Setenv("PATH", bin)
	f := detectQuickstartFindings(t.TempDir(), quickstartDetection{Tracker: "local", Runner: "claude"}, quickstartOptions{NoLocalModel: true})
	assert.Equal(t, []foundCLI{
		{Name: "claude", Path: filepath.Join(bin, "claude")},
		{Name: "codex"},
		{Name: "opencode", Path: filepath.Join(bin, "opencode")},
		{Name: "cursor-agent"},
	}, f.AgentCLIs)
	assert.Equal(t, "not checked (--no-local-model)", f.ModelServerNote)
	assertNoAgentRan(t, bin)

	t.Setenv("PATH", fakeAgentBin(t, "codex", "cursor-agent"))
	det, err := detectQuickstart(t.TempDir(), "", "")
	require.NoError(t, err)
	assert.Equal(t, "codex", det.Runner, "codex is the default agent without claude")
}

// TestDetectCI: each CI system by its configuration; an empty
// .github/workflows is not GitHub Actions.
func TestDetectCI(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  []string
	}{
		"github actions": {files: map[string]string{".github/workflows/ci.yml": "on: push\n"}, want: []string{"GitHub Actions (.github/workflows)"}},
		"gitlab":         {files: map[string]string{".gitlab-ci.yml": "test:\n"}, want: []string{"GitLab CI (.gitlab-ci.yml)"}},
		"circleci":       {files: map[string]string{".circleci/config.yml": "version: 2.1\n"}, want: []string{"CircleCI (.circleci)"}},
		"buildkite dir":  {files: map[string]string{".buildkite/pipeline.yml": "steps: []\n"}, want: []string{"Buildkite (.buildkite)"}},
		"buildkite file": {files: map[string]string{"buildkite.yml": "steps: []\n"}, want: []string{"Buildkite (buildkite.yml)"}},
		"several":        {files: map[string]string{".github/workflows/a.yaml": "x", ".gitlab-ci.yml": "x"}, want: []string{"GitHub Actions (.github/workflows)", "GitLab CI (.gitlab-ci.yml)"}},
		"no workflows":   {files: map[string]string{".github/workflows/README.md": "x", ".github/CODEOWNERS": "x"}},
		"nothing":        {},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tc.files)
			assert.Equal(t, tc.want, detectCI(dir))
		})
	}
}

// TestDetectProjectChecks: Makefile targets, then package.json scripts
// (with the lockfile's package manager), then go.mod; a name once.
func TestDetectProjectChecks(t *testing.T) {
	t.Run("makefile, pnpm scripts and go.mod", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"Makefile":       "GO := go\ntest: build\n\tgo test ./...\nlint:\n\tgolangci-lint run\nTESTFLAGS := -race\n",
			"package.json":   `{"scripts":{"test":"vitest","lint":"eslint .","typecheck":"tsc --noEmit","dev":"vite"}}`,
			"pnpm-lock.yaml": "lockfileVersion: 9\n",
			"go.mod":         "module example.com/x\n",
		})
		assert.Equal(t, []proposedCheck{
			{Name: "test", Run: "make test", From: "Makefile"},
			{Name: "lint", Run: "make lint", From: "Makefile"},
			{Name: "typecheck", Run: "pnpm run typecheck", From: "package.json scripts"},
			{Name: "vet", Run: "go vet ./...", From: "go.mod"},
		}, detectProjectChecks(dir))
	})
	t.Run("package.json without a lockfile uses npm", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"package.json": `{"scripts":{"test":"jest","build":"tsc"}}`})
		assert.Equal(t, []proposedCheck{{Name: "test", Run: "npm run test", From: "package.json scripts"}}, detectProjectChecks(dir))
	})
	t.Run("yarn", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"package.json": `{"scripts":{"lint":"eslint"}}`, "yarn.lock": ""})
		assert.Equal(t, []proposedCheck{{Name: "lint", Run: "yarn run lint", From: "package.json scripts"}}, detectProjectChecks(dir))
	})
	t.Run("go.mod only", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"go.mod": "module x\n"})
		assert.Equal(t, []proposedCheck{
			{Name: "test", Run: "go test ./...", From: "go.mod"},
			{Name: "vet", Run: "go vet ./...", From: "go.mod"},
		}, detectProjectChecks(dir))
	})
	t.Run("nothing", func(t *testing.T) {
		assert.Empty(t, detectProjectChecks(t.TempDir()))
	})
}

// TestQuickstartPlanText: the plan's lines for a repository with CI, a
// deploy tool, test commands and a local tracker.
func TestQuickstartPlanText(t *testing.T) {
	stubLookPath(t, "claude", "cursor-agent")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".github/workflows/ci.yml": "on: push\n",
		"package.json":             `{"dependencies":{"convex":"^1.0.0"},"scripts":{"test":"vitest","lint":"eslint ."}}`,
	})
	det := quickstartDetection{Tracker: "local", TrackerReason: "no origin remote and LINEAR_API_KEY is not set", Runner: "claude", RunnerReason: "/usr/local/bin/claude"}
	f := detectQuickstartFindings(dir, det, quickstartOptions{})
	wf := filepath.Join(dir, "WORKFLOW.md")
	var out strings.Builder
	renderQuickstartPlan(&out, f, wf)
	lines := strings.Split(out.String(), "\n")
	for _, want := range []string{
		"Here's what I found:",
		"  tracker        local — no origin remote and LINEAR_API_KEY is not set",
		"  agent CLI      claude: /usr/local/bin/claude (the default agent)",
		"  agent CLI      codex: not found",
		"  agent CLI      opencode: not found",
		"  agent CLI      cursor-agent: /usr/local/bin/cursor-agent",
		"  model server   none answering on the usual local ports",
		"  CI             GitHub Actions (.github/workflows) — `itervox referee` can hold agent changes to CI for a human: https://itervox.dev/guides/referee/",
		"  deploy tools   convex — `itervox referee envcheck` checks the deploy-target variables they read: https://itervox.dev/guides/referee/#referee-envcheck",
		"  test/lint      test: npm run test (package.json scripts)",
		"  test/lint      lint: npm run lint (package.json scripts)",
		"Here's what I'll write:",
		"  " + wf + " — schema 2, tracker local, agent claude, the prompt and the profiles",
		"  " + filepath.Join(dir, ".itervox", "settings.jsonc") + " — tracker, agent, workspace and server settings",
		"  " + filepath.Join(dir, ".itervox", "issues", "DEMO-1.md") + ` — a demo task (Todo): "Add a README section on how to run the tests"`,
		`        - { name: test, run: "npm run test" }`,
		`        - { name: lint, run: "npm run lint" }`,
	} {
		assert.Contains(t, lines, want)
	}

	f = detectQuickstartFindings(t.TempDir(), quickstartDetection{Tracker: "github", Runner: "claude"}, quickstartOptions{NoDemo: true})
	out.Reset()
	renderQuickstartPlan(&out, f, wf)
	assert.Contains(t, out.String(), "  CI             none found\n")
	assert.Contains(t, out.String(), "  test/lint      none found")
	assert.NotContains(t, out.String(), "DEMO-1", "no demo issue for a service tracker")
	assert.NotContains(t, out.String(), "ITX-1")
	assert.NotContains(t, out.String(), "Suggested verify checks")
}

// newLocalRepo is a git repository with no remote, a fake claude first on
// PATH, a private HOME and no tracker credentials in the environment.
func newLocalRepo(t *testing.T) (dir, bin string) {
	t.Helper()
	realLookPath(t)
	noModelDiscovery(t)
	dir = repoWithRemote(t, "")
	bin = fakeAgentBin(t, "claude")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("ITERVOX_API_TOKEN", "")
	return dir, bin
}

// TestQuickstartYesScaffoldsLoadableLocalSetupWithDemo: --yes writes a
// workflow config.Load accepts (layered settings in .itervox/settings.jsonc),
// a demo issue the local tracker parses in an active state, prints the
// exact command that runs it, and starts no daemon.
func TestQuickstartYesScaffoldsLoadableLocalSetupWithDemo(t *testing.T) {
	dir, bin := newLocalRepo(t)
	writeFiles(t, dir, map[string]string{"Makefile": "test:\n\ttrue\n"})
	noDaemonStart(t)

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, Yes: true, NoLocalModel: true, NonInteractive: true}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	t.Log(out.String())
	wf := filepath.Join(dir, "WORKFLOW.md")

	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, "local", cfg.Tracker.Kind)
	assert.FileExists(t, filepath.Join(dir, ".itervox", "settings.jsonc"))
	front, err := readQuickstartFrontMatter(wf)
	require.NoError(t, err)
	assert.Equal(t, 2, front.Version)

	tr := local.New(local.Config{Dir: filepath.Join(dir, ".itervox", "issues"), ActiveStates: cfg.Tracker.ActiveStates, TerminalStates: cfg.Tracker.TerminalStates})
	issue, err := tr.FetchIssueByIdentifier(context.Background(), "DEMO-1")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, demoIssueTitle, issue.Title)
	assert.Contains(t, cfg.Tracker.ActiveStates, issue.State, "`itervox run` runs only an active issue")
	require.NotNil(t, issue.Description)
	assert.Contains(t, *issue.Description, "`make test`", "the demo names the detected test command")
	assert.Contains(t, *issue.Description, "Change nothing else")

	s := out.String()
	assert.Contains(t, s, "Write this setup? [Y/n] y (preset)")
	assert.Contains(t, s, "itervox quickstart: doctor checks passed")
	assert.Contains(t, s, "  itervox run --issue DEMO-1 --worktree --workflow "+wf+"\n")
	assert.Contains(t, s, "  itervox open --workflow "+wf)
	assert.NotContains(t, s, "itervox doctor\n==============", "doctor's full report is not printed")
	assertNoAgentRan(t, bin)

	// Re-running keeps the workflow and the demo issue as they are.
	before, _ := os.ReadFile(filepath.Join(dir, ".itervox", "issues", "DEMO-1.md"))
	var again strings.Builder
	code = quickstart(quickstartOptions{Dir: dir, Yes: true, NoLocalModel: true, NoStart: true}, strings.NewReader(""), &again)
	require.Equal(t, 0, code, again.String())
	assert.Contains(t, again.String(), "using the existing")
	after, _ := os.ReadFile(filepath.Join(dir, ".itervox", "issues", "DEMO-1.md"))
	assert.Equal(t, string(before), string(after))
}

// TestQuickstartNoDemoKeepsTheOldFlow: --no-demo writes no DEMO-1, and
// --no-start then stops after the checks as before.
func TestQuickstartNoDemoKeepsTheOldFlow(t *testing.T) {
	dir, _ := newLocalRepo(t)
	noDaemonStart(t)
	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, Yes: true, NoLocalModel: true, NoDemo: true, NoStart: true}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.NoFileExists(t, filepath.Join(dir, ".itervox", "issues", "DEMO-1.md"))
	assert.FileExists(t, filepath.Join(dir, ".itervox", "issues", "ITX-1.md"))
	assert.Contains(t, out.String(), "not starting the daemon (--no-start)")
	assert.NotContains(t, out.String(), "itervox run --issue DEMO-1")
}

// TestQuickstartWithoutATerminalPrintsThePlanAndWritesNothing: no TTY and
// no --yes: the plan and the way on, exit 1, no file written.
func TestQuickstartWithoutATerminalPrintsThePlanAndWritesNothing(t *testing.T) {
	dir, bin := newLocalRepo(t)
	noDaemonStart(t)
	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoLocalModel: true, NonInteractive: true}, strings.NewReader("y\n"), &out)
	assert.Equal(t, 1, code)
	s := out.String()
	assert.Contains(t, s, "Here's what I found:")
	assert.Contains(t, s, "Here's what I'll write:")
	assert.Contains(t, s, "nothing was written: there is no terminal to confirm the plan on.")
	assert.Contains(t, s, "with --yes to write it as shown")
	assert.NotContains(t, s, "[Y/n]", "nothing is asked")
	assert.NoFileExists(t, filepath.Join(dir, "WORKFLOW.md"))
	assert.NoDirExists(t, filepath.Join(dir, ".itervox"))
	assertNoAgentRan(t, bin)
}

// TestQuickstartDeclinedPlanWritesNothing: "n" (and EOF without an
// answer) writes nothing; Enter is yes.
func TestQuickstartDeclinedPlanWritesNothing(t *testing.T) {
	for name, input := range map[string]string{"n": "n\n", "eof": ""} {
		t.Run(name, func(t *testing.T) {
			dir, _ := newLocalRepo(t)
			noDaemonStart(t)
			var out strings.Builder
			code := quickstart(quickstartOptions{Dir: dir, NoLocalModel: true}, strings.NewReader(input), &out)
			assert.Equal(t, 1, code)
			assert.Contains(t, out.String(), "Write this setup? [Y/n] ")
			assert.Contains(t, out.String(), "nothing was written")
			assert.NoFileExists(t, filepath.Join(dir, "WORKFLOW.md"))
		})
	}
}

// TestQuickstartNeverPrintsSecretValues: tracker credentials, and a token
// embedded in the origin URL, are reported by presence only — in the plan,
// the scaffold output, the doctor step and the next steps.
func TestQuickstartNeverPrintsSecretValues(t *testing.T) {
	const (
		linearSentinel = "lin_api_SENTINELlinear0123456789"
		githubSentinel = "ghp_SENTINELgithub0123456789abcdef"
		remoteSentinel = "SENTINELremote0123456789"
	)
	sentinels := []string{linearSentinel, githubSentinel, remoteSentinel}
	assertClean := func(t *testing.T, s string) {
		t.Helper()
		for _, v := range sentinels {
			assert.NotContains(t, s, v)
		}
	}

	t.Run("linear, full run", func(t *testing.T) {
		dir, _ := newLocalRepo(t)
		noDaemonStart(t)
		t.Setenv("LINEAR_API_KEY", linearSentinel)
		t.Setenv("GITHUB_TOKEN", githubSentinel)
		var out strings.Builder
		_ = quickstart(quickstartOptions{Dir: dir, Yes: true, NoLocalModel: true, NoStart: true}, strings.NewReader(""), &out)
		assert.Contains(t, out.String(), "  tracker        linear — LINEAR_API_KEY is set")
		assertClean(t, out.String())
	})
	t.Run("github remote with an embedded token", func(t *testing.T) {
		stubLookPath(t, "claude", "gh")
		noModelDiscovery(t)
		t.Setenv("LINEAR_API_KEY", "")
		t.Setenv("GITHUB_TOKEN", githubSentinel)
		dir := repoWithRemote(t, "https://x-access-token:"+remoteSentinel+"@github.com/acme/widgets.git")
		det, err := detectQuickstart(dir, "", "")
		require.NoError(t, err)
		assert.Equal(t, "github", det.Tracker)
		assert.Contains(t, det.TrackerReason, "origin is https://github.com/acme/widgets.git and GITHUB_TOKEN is set")
		var out strings.Builder
		renderQuickstartPlan(&out, detectQuickstartFindings(dir, det, quickstartOptions{NoLocalModel: true}), filepath.Join(dir, "WORKFLOW.md"))
		info := scanRepo(dir)
		require.NoError(t, scaffoldWorkflow(filepath.Join(dir, "WORKFLOW.md"), dir, det.Tracker, det.Runner, initOptions{}, &out))
		assert.Contains(t, info.RemoteURL, remoteSentinel, "the fixture does embed the token")
		assertClean(t, out.String())
	})
}

// TestDoctorFailureLines: only failing lines and their indented fixes are
// kept; passing rows, warnings and notes are not.
func TestDoctorFailureLines(t *testing.T) {
	rendered := strings.Join([]string{
		"itervox doctor",
		"==============",
		"workflow: /w/WORKFLOW.md",
		"schema: FAILED — config: tracker.kind is required",
		"running binary: /usr/bin/itervox",
		"WARNING: agent CLI codex not found on PATH",
		"ERROR: 2 state label(s) missing on o/r — create them with `itervox doctor --fix`, or:",
		`  gh label create "todo" --repo o/r`,
		`  gh label create "done" --repo o/r`,
		"github labels: not checked",
		"WARNING: port 8090 is held by itervox (pid 7)",
		"repo readiness:",
		"  [pass] schema: schema marker",
		"  [fail] bare-clone: bare clone fetch — failing",
		"         fix: run git fetch in the bare clone",
		"  [warn] claude-md: CLAUDE.md — missing",
		"         fix: add one",
		"",
	}, "\n")
	got := doctorFailureLines(DoctorReport{PortInUseWarning: "port 8090 is held by itervox (pid 7)"}, rendered)
	assert.Equal(t, []string{
		"schema: FAILED — config: tracker.kind is required",
		"ERROR: 2 state label(s) missing on o/r — create them with `itervox doctor --fix`, or:",
		`  gh label create "todo" --repo o/r`,
		`  gh label create "done" --repo o/r`,
		"WARNING: port 8090 is held by itervox (pid 7)",
		"[fail] bare-clone: bare clone fetch — failing",
		"  fix: run git fetch in the bare clone",
	}, got)
}

// TestQuickstartTrackerNextSteps: with GitHub nothing is created on the
// service; quickstart prints how to label an issue and run it.
func TestQuickstartTrackerNextSteps(t *testing.T) {
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.Contains(t, out.String(), `give a small issue the "todo" label`)
	assert.Contains(t, out.String(), "  gh issue edit 42 --add-label todo\n")
	assert.Contains(t, out.String(), "  itervox run --issue 42 --worktree --workflow "+wf+"\n")

	out.Reset()
	printTrackerNextSteps(&out, wf, true)
	assert.Contains(t, out.String(), "The daemon picks it up on its next poll")
	assert.NotContains(t, out.String(), "itervox run --issue")
}

// TestQuickstartDemoIssueRunsWithItervoxRun: the printed command works on
// what quickstart wrote: `itervox run --issue DEMO-1 --worktree` (a fake
// agent here) runs the demo issue to success in a repository with no
// remote, leaves the checkout on its branch and moves DEMO-1 to
// completion_state.
func TestQuickstartDemoIssueRunsWithItervoxRun(t *testing.T) {
	dir, _ := newLocalRepo(t)
	noDaemonStart(t)
	rigGit(t, dir, "config", "user.name", "Itervox Test")
	rigGit(t, dir, "config", "user.email", "test@example.invalid")
	rigGit(t, dir, "config", "commit.gpgsign", "false")
	writeFiles(t, dir, map[string]string{"README.md": "# widgets\n"})
	rigGit(t, dir, "add", "-A")
	rigGit(t, dir, "commit", "--quiet", "-m", "initial")
	branch := rigGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	var out strings.Builder
	require.Equal(t, 0, quickstart(quickstartOptions{Dir: dir, Yes: true, NoLocalModel: true}, strings.NewReader(""), &out), out.String())
	require.Contains(t, out.String(), "itervox run --issue DEMO-1 --worktree")

	var prompt string
	runner := &fakeRunRunner{fn: func(rt runTurn) (agent.TurnResult, error) {
		prompt = rt.prompt
		if err := os.WriteFile(filepath.Join(rt.ws, "README.md"), []byte("# widgets\n\n## Running the tests\n\n    make test\n"), 0o644); err != nil {
			return agent.TurnResult{Failed: true, FailureText: err.Error()}, err
		}
		for _, args := range [][]string{{"add", "README.md"}, {"commit", "--quiet", "-m", "docs: running the tests"}} {
			if b, err := gitexec.Command(context.Background(), rt.ws, args...).CombinedOutput(); err != nil {
				return agent.TurnResult{Failed: true, FailureText: string(b)}, err
			}
		}
		return agent.TurnResult{ResultText: "Added the section.", AllTextBlocks: []string{"Added the section."}, SessionID: "sess-demo"}, nil
	}}
	prev := newIssueRunDeps
	newIssueRunDeps = func() issueRunDeps {
		deps := defaultIssueRunDeps()
		deps.runner = func(*config.Config) agent.Runner { return runner }
		deps.validateAgent = false
		deps.prCreate = func(context.Context, string, string, string, string, string) (string, error) {
			return "", errors.New("no forge in this test")
		}
		return deps
	}
	t.Cleanup(func() { newIssueRunDeps = prev })

	var stdout, stderr strings.Builder
	code := runIssueCommand(context.Background(), []string{"--workflow", filepath.Join(dir, "WORKFLOW.md"), "--issue", "DEMO-1", "--worktree"}, &stdout, &stderr)
	t.Log(stdout.String())
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	assert.Equal(t, 1, int(runner.calls.Load()), "one agent turn")
	assert.Contains(t, prompt, "DEMO-1")
	assert.Equal(t, branch, rigGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"), "the checkout stays on its branch")
	assert.Contains(t, rigGit(t, dir, "show", "itervox/demo-1:README.md"), "## Running the tests", "the agent's commit is on the issue branch")

	cfg, err := config.Load(filepath.Join(dir, "WORKFLOW.md"))
	require.NoError(t, err)
	tr := local.New(local.Config{Dir: filepath.Join(dir, ".itervox", "issues"), ActiveStates: cfg.Tracker.ActiveStates, TerminalStates: cfg.Tracker.TerminalStates})
	issue, err := tr.FetchIssueByIdentifier(context.Background(), "DEMO-1")
	require.NoError(t, err)
	assert.Equal(t, cfg.Tracker.CompletionState, issue.State)
}
