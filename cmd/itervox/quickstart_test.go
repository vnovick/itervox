package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// stubLookPath makes exactly the named CLIs "installed".
func stubLookPath(t *testing.T, installed ...string) {
	t.Helper()
	orig := quickstartLookPath
	quickstartLookPath = func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/usr/local/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { quickstartLookPath = orig })
}

func repoWithRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	if remote != "" {
		out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", remote).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return dir
}

// TestDetectQuickstartTrackerAndRunner (#74): the tracker comes from
// --tracker, then a real LINEAR_API_KEY, then a github.com origin; the agent
// from --runner, then claude, then codex on PATH.
func TestDetectQuickstartTrackerAndRunner(t *testing.T) {
	ghSSH := repoWithRemote(t, "git@github.com:acme/widgets.git")
	ghHTTPS := repoWithRemote(t, "https://github.com/acme/widgets")
	gitlab := repoWithRemote(t, "git@gitlab.com:acme/widgets.git")
	noRemote := repoWithRemote(t, "")

	cases := []struct {
		name, dir, trackerFlag, runnerFlag, linearKey string
		installed                                     []string
		wantTracker, wantRunner, wantErr              string
	}{
		{name: "github ssh remote, claude", dir: ghSSH, installed: []string{"claude", "codex"}, wantTracker: "github", wantRunner: "claude"},
		{name: "github https remote, only codex", dir: ghHTTPS, installed: []string{"codex"}, wantTracker: "github", wantRunner: "codex"},
		{name: "linear key wins over a github remote", dir: ghSSH, linearKey: "lin_api_realkey123", installed: []string{"claude"}, wantTracker: "linear", wantRunner: "claude"},
		{name: "placeholder linear key is ignored", dir: ghSSH, linearKey: "lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", installed: []string{"claude"}, wantTracker: "github", wantRunner: "claude"},
		{name: "--tracker overrides detection", dir: gitlab, trackerFlag: "github", installed: []string{"claude"}, wantTracker: "github", wantRunner: "claude"},
		{name: "--runner overrides detection", dir: ghSSH, runnerFlag: "codex", installed: []string{"claude", "codex"}, wantTracker: "github", wantRunner: "codex"},
		{name: "non-github remote without a key", dir: gitlab, installed: []string{"claude"}, wantErr: "origin git@gitlab.com:acme/widgets.git is not on github.com"},
		{name: "no remote without a key", dir: noRemote, installed: []string{"claude"}, wantErr: "no origin remote and LINEAR_API_KEY is not set"},
		{name: "no agent CLI", dir: ghSSH, wantErr: "neither claude nor codex is on PATH"},
		{name: "--runner not installed", dir: ghSSH, runnerFlag: "codex", installed: []string{"claude"}, wantErr: "--runner codex: codex is not on PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LINEAR_API_KEY", tc.linearKey)
			stubLookPath(t, tc.installed...)
			det, err := detectQuickstart(tc.dir, tc.trackerFlag, tc.runnerFlag)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTracker, det.Tracker)
			assert.Equal(t, tc.wantRunner, det.Runner)
			assert.NotEmpty(t, det.TrackerReason)
			assert.NotEmpty(t, det.RunnerReason)
		})
	}
}

func TestGitRemoteHost(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:acme/widgets.git":       "github.com",
		"https://github.com/acme/widgets":       "github.com",
		"ssh://git@GitHub.com/acme/widgets.git": "github.com",
		"https://user:pw@github.com:443/acme/w": "github.com",
		"git@gitlab.com:acme/widgets.git":       "gitlab.com",
		"https://ghe.example.com/acme/widgets":  "ghe.example.com",
		"":                                      "",
		"/srv/git/widgets.git":                  "",
	} {
		assert.Equal(t, want, gitRemoteHost(remote), remote)
	}
}

// TestSetEnvFileVar: replaces a placeholder or empty value, appends a missing
// key, never replaces a real value, writes 0600.
func TestSetEnvFileVar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(p, []byte("# header\nGITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\nOTHER=1\n"), 0o600))
	require.NoError(t, setEnvFileVar(p, "GITHUB_TOKEN", "gho_real"))
	raw, _ := os.ReadFile(p)
	assert.Equal(t, "# header\nGITHUB_TOKEN=gho_real\nOTHER=1\n", string(raw))

	err := setEnvFileVar(p, "GITHUB_TOKEN", "gho_other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not overwriting")

	require.NoError(t, setEnvFileVar(p, "NEW", "v"))
	raw, _ = os.ReadFile(p)
	assert.True(t, strings.HasSuffix(string(raw), "OTHER=1\nNEW=v\n"))

	fresh := filepath.Join(dir, "sub", ".env")
	require.NoError(t, setEnvFileVar(fresh, "GITHUB_TOKEN", "gho_real"))
	info, err := os.Stat(fresh)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// quickstartGitHubProject is a repository with a GitHub schema-2 workflow
// pointed at a fake labels API, a stub claude on PATH, and an isolated HOME.
func quickstartGitHubProject(t *testing.T, labels []string) (dir, wf string, repo *fakeLabelRepo) {
	t.Helper()
	repo = &fakeLabelRepo{labels: labels}
	wf = writeLabelWorkflow(t, "github", repo.serve(t).URL)
	dir = filepath.Dir(wf)
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho 'stub claude 0.0.0'\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return dir, wf, repo
}

// TestQuickstartLabelStep (#74): missing GitHub state labels are created
// only after confirmation, through the same doctor --fix path, and a declined
// prompt stops quickstart with doctor's report instead of starting anything.
func TestQuickstartLabelStep(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		dir, _, repo := quickstartGitHubProject(t, []string{"todo"})
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("n\n"), &out)
		assert.Equal(t, 1, code)
		assert.Empty(t, repo.created)
		assert.Contains(t, out.String(), "Create 5 label(s) on owner/repo")
		assert.Contains(t, out.String(), "ERROR: 5 state label(s) missing")
		assert.Contains(t, out.String(), "doctor found the problems above")
	})
	t.Run("accepted", func(t *testing.T) {
		dir, _, repo := quickstartGitHubProject(t, []string{"todo"})
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\n"), &out)
		assert.Equal(t, 0, code, out.String())
		assert.Len(t, repo.created, 5)
		assert.Contains(t, out.String(), "doctor checks passed")
		assert.Contains(t, out.String(), "not starting the daemon (--no-start)")
	})
}

// TestQuickstartKeepsExistingWorkflow (#74): re-running never rewrites a
// schema-2 workflow, and an older one is migrated only after confirmation.
func TestQuickstartKeepsExistingWorkflow(t *testing.T) {
	t.Run("schema 2 is left byte-identical", func(t *testing.T) {
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		before, _ := os.ReadFile(wf)
		for range 2 {
			var out strings.Builder
			code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
			require.Equal(t, 0, code, out.String())
			assert.Contains(t, out.String(), "using the existing")
		}
		after, _ := os.ReadFile(wf)
		assert.Equal(t, string(before), string(after))
		assert.NoFileExists(t, wf+".bak")
	})

	legacy := func(t *testing.T) (string, string, []byte) {
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		raw, _ := os.ReadFile(wf)
		old := strings.Replace(string(raw), "itervox_schema_version: 2\n", "", 1)
		require.NoError(t, os.WriteFile(wf, []byte(old), 0o644))
		return dir, wf, []byte(old)
	}
	t.Run("older schema, declined", func(t *testing.T) {
		dir, wf, old := legacy(t)
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("n\n"), &out)
		assert.Equal(t, 1, code)
		after, _ := os.ReadFile(wf)
		assert.Equal(t, string(old), string(after), "declined: unchanged")
		assert.Contains(t, out.String(), "left "+wf+" unchanged")
	})
	t.Run("older schema, accepted", func(t *testing.T) {
		dir, wf, _ := legacy(t)
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\n"), &out)
		assert.Equal(t, 0, code, out.String())
		v, err := workflowSchemaVersion(wf)
		require.NoError(t, err)
		assert.Equal(t, 2, v)
		assert.FileExists(t, wf+".bak")
	})
}

// TestQuickstartWritesNewWorkflow: in a repository with a GitHub origin and
// no workflow, quickstart scaffolds a schema-2 GitHub workflow for the
// detected agent, and stops at the credential step when there is no token.
func TestQuickstartWritesNewWorkflow(t *testing.T) {
	dir := repoWithRemote(t, "git@github.com:acme/widgets.git")
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "")
	stubLookPath(t, "claude")
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })
	origGH := quickstartGHToken
	quickstartGHToken = func(context.Context) (string, error) { return "", errors.New("gh: not logged in") }
	t.Cleanup(func() { quickstartGHToken = origGH })

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "tracker github (origin is git@github.com:acme/widgets.git), agent claude")
	assert.Contains(t, out.String(), "no GitHub token. Run `gh auth login`")
	front, err := readQuickstartFrontMatter(filepath.Join(dir, "WORKFLOW.md"))
	require.NoError(t, err)
	assert.Equal(t, 2, front.Version)
	assert.Equal(t, "github", front.Tracker.Kind)
	assert.FileExists(t, filepath.Join(dir, ".itervox", ".env"))
}

// TestQuickstartUsesGHTokenAfterConfirmation: with no GITHUB_TOKEN, the gh
// CLI's token is saved to .itervox/.env only when the operator agrees.
func TestQuickstartUsesGHTokenAfterConfirmation(t *testing.T) {
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", ".env"),
			[]byte("GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"), 0o600))
		wf := filepath.Join(dir, "WORKFLOW.md")
		require.NoError(t, os.WriteFile(wf, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: $GITHUB_TOKEN\n  project_slug: o/r\n---\nPrompt.\n"), 0o644))
		t.Setenv("GITHUB_TOKEN", "")
		orig := quickstartGHToken
		quickstartGHToken = func(context.Context) (string, error) { return "gho_fromgh", nil }
		t.Cleanup(func() { quickstartGHToken = orig })
		return wf
	}
	t.Run("declined", func(t *testing.T) {
		wf := setup(t)
		var out strings.Builder
		code := quickstartCredential(wf, filepath.Join(filepath.Dir(wf), ".itervox"), false, bufioReader("n\n"), &out)
		assert.Equal(t, 1, code)
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(wf), ".itervox", ".env"))
		assert.NotContains(t, string(raw), "gho_fromgh")
	})
	t.Run("accepted", func(t *testing.T) {
		wf := setup(t)
		var out strings.Builder
		code := quickstartCredential(wf, filepath.Join(filepath.Dir(wf), ".itervox"), false, bufioReader("y\n"), &out)
		assert.Equal(t, 0, code, out.String())
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(wf), ".itervox", ".env"))
		assert.Equal(t, "GITHUB_TOKEN=gho_fromgh\n", string(raw))
		assert.Equal(t, "gho_fromgh", os.Getenv("GITHUB_TOKEN"))
		assert.True(t, trackerAPIKeyResolved(wf))
	})
}

// TestQuickstartStartsDaemonAndWaitsReady (#74): quickstart starts the real
// daemon in the background (here the test binary re-executed as itervox),
// waits for /api/v1/ready, prints the dashboard URL and the token file, and a
// second run reports the running daemon instead of starting another.
func TestQuickstartStartsDaemonAndWaitsReady(t *testing.T) {
	p := newDaemonProject(t)
	// The in-process doctor checks need the stub claude and the same HOME.
	for _, kv := range p.env {
		if k, v, ok := strings.Cut(kv, "="); ok && (k == "PATH" || k == "HOME") {
			t.Setenv(k, v)
		}
	}
	t.Setenv("ITERVOX_API_TOKEN", "")
	orig := quickstartDaemonCommand
	quickstartDaemonCommand = func(workflowPath string) (*exec.Cmd, error) {
		raw, err := json.Marshal([]string{"--workflow", workflowPath})
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(append([]string{}, p.env...), daemonChildArgsEnv+"="+string(raw))
		return cmd, nil
	}
	t.Cleanup(func() { quickstartDaemonCommand = orig })
	t.Cleanup(func() {
		if pid, _, _, err := readPIDFile(p.Workflow); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) && processAlive(pid) {
				time.Sleep(50 * time.Millisecond)
			}
			if processAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: p.Dir, ReadyTimeout: 60 * time.Second}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())

	raw, err := os.ReadFile(dashboardURLFilePath(p.Workflow))
	require.NoError(t, err)
	url := strings.TrimSpace(string(raw))
	require.NotEmpty(t, url)
	assert.Contains(t, out.String(), "Dashboard: "+url)
	tokenFile := filepath.Join(defaultLogsDir(p.Workflow), "api-token")
	assert.Contains(t, out.String(), "API token: "+tokenFile)
	assert.FileExists(t, tokenFile, "the daemon writes the generated token where quickstart says")
	assert.True(t, quickstartReady(httpClientForTest(), url), "the dashboard is reachable after quickstart returns")

	var again strings.Builder
	code = quickstart(quickstartOptions{Dir: p.Dir}, strings.NewReader(""), &again)
	require.Equal(t, 0, code, again.String())
	assert.Contains(t, again.String(), "the daemon is already running")
	assert.NotContains(t, again.String(), "started the daemon")
}

func bufioReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

func httpClientForTest() *http.Client { return &http.Client{Timeout: 2 * time.Second} }

// TestQuickstartWaitsForReadyEndpoint pins the wait loop: quickstart returns
// only once GET /api/v1/ready answers 200, gives up after the timeout, and
// reports a daemon that exits early.
func TestQuickstartWaitsForReadyEndpoint(t *testing.T) {
	fakeDaemon := func(t *testing.T, script string) {
		t.Helper()
		orig := quickstartDaemonCommand
		quickstartDaemonCommand = func(string) (*exec.Cmd, error) {
			return exec.Command("sh", "-c", script), nil
		}
		t.Cleanup(func() { quickstartDaemonCommand = orig })
	}
	readyServer := func(t *testing.T, notReadyFor int) (*httptest.Server, *atomic.Int64) {
		t.Helper()
		var calls atomic.Int64
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/ready" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if n := calls.Add(1); notReadyFor < 0 || n <= int64(notReadyFor) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)
		return ts, &calls
	}
	project := func(t *testing.T) (wf, itervoxDir string) {
		dir := t.TempDir()
		return filepath.Join(dir, "WORKFLOW.md"), filepath.Join(dir, ".itervox")
	}

	t.Run("waits for 200", func(t *testing.T) {
		ts, calls := readyServer(t, 3)
		wf, itervoxDir := project(t)
		fakeDaemon(t, "mkdir -p .itervox && echo '"+ts.URL+"/' > .itervox/dashboard_url && sleep 3")
		var out strings.Builder
		url, err := quickstartStartDaemon(wf, itervoxDir, 20*time.Second, &out)
		require.NoError(t, err)
		assert.Equal(t, ts.URL+"/", url)
		assert.GreaterOrEqual(t, calls.Load(), int64(4), "three 503s were waited out")
	})
	t.Run("times out while not ready", func(t *testing.T) {
		ts, _ := readyServer(t, -1)
		wf, itervoxDir := project(t)
		fakeDaemon(t, "mkdir -p .itervox && echo '"+ts.URL+"/' > .itervox/dashboard_url && sleep 3")
		var out strings.Builder
		_, err := quickstartStartDaemon(wf, itervoxDir, time.Second, &out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not report ready within 1s")
	})
	t.Run("daemon exits early", func(t *testing.T) {
		wf, itervoxDir := project(t)
		fakeDaemon(t, "echo 'startup failed' >&2; exit 3")
		var out strings.Builder
		_, err := quickstartStartDaemon(wf, itervoxDir, 20*time.Second, &out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the daemon exited before it was ready")
		logged, _ := os.ReadFile(filepath.Join(itervoxDir, "logs", "quickstart-daemon.log"))
		assert.Contains(t, string(logged), "startup failed", "daemon output goes to the quickstart log")
	})
}

// TestQuickstartLoadsProjectEnvFromAnotherDirectory (review of #74): run with
// --dir from elsewhere, quickstart reads the project's own .itervox/.env, so
// a token already saved there is used instead of asking again.
func TestQuickstartLoadsProjectEnvFromAnotherDirectory(t *testing.T) {
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	raw, _ := os.ReadFile(wf)
	require.NoError(t, os.WriteFile(wf, []byte(strings.Replace(string(raw), "api_key: test-token", "api_key: $GITHUB_TOKEN", 1)), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", ".env"), []byte("GITHUB_TOKEN=gho_saved_earlier\n"), 0o600))
	t.Setenv("GITHUB_TOKEN", "") // registers the restore
	require.NoError(t, os.Unsetenv("GITHUB_TOKEN"))
	orig := quickstartGHToken
	quickstartGHToken = func(context.Context) (string, error) { return "", errors.New("gh must not be needed") }
	t.Cleanup(func() { quickstartGHToken = orig })

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
	assert.Equal(t, 0, code, out.String())
	assert.Equal(t, "gho_saved_earlier", os.Getenv("GITHUB_TOKEN"))
	envRaw, _ := os.ReadFile(filepath.Join(dir, ".itervox", ".env"))
	assert.Equal(t, "GITHUB_TOKEN=gho_saved_earlier\n", string(envRaw))
}

// TestQuickstartClearsStaleRuntimeFiles (review of #74): after a daemon died
// without cleaning up, its HEARTBEAT.md and dashboard_url no longer stop
// quickstart at doctor.
func TestQuickstartClearsStaleRuntimeFiles(t *testing.T) {
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(heartbeatPath(wf), []byte("Daemon: running\n"), 0o644))
	require.NoError(t, os.WriteFile(dashboardURLFilePath(wf), []byte("http://127.0.0.1:1/\n"), 0o644))

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
	assert.Equal(t, 0, code, out.String())
	assert.NoFileExists(t, heartbeatPath(wf))
	assert.NoFileExists(t, dashboardURLFilePath(wf))
	assert.Contains(t, out.String(), "left by a daemon that is no longer running")
}
