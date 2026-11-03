package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// gitT runs git in dir for a test fixture, through gitexec like the daemon.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitexec.Command(context.Background(), dir, append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "LANG=C", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// hotspotFixture is a repository whose main branch has a squash-merged and
// then reverted agent PR (#7), plus two unmerged run branches.
func hotspotFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeT(t, dir, "a/x.go", "package a\n")
	writeT(t, dir, "b/y.go", "package b\n")
	writeT(t, dir, "README.md", "readme\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")

	gitT(t, dir, "checkout", "-q", "-b", "feat/eng-1")
	writeT(t, dir, "a/x.go", "package a\n// run 1\n")
	writeT(t, dir, "a/z.go", "package a\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "ENG-1 work")

	gitT(t, dir, "checkout", "-q", "main")
	gitT(t, dir, "checkout", "-q", "-b", "feat/eng-2")
	writeT(t, dir, "a/x.go", "package a\n// run 2\n")
	writeT(t, dir, "b/y.go", "package b\n// run 2\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "ENG-2 work")

	// ENG-3's PR #7 was squash-merged (its branch deleted) and reverted.
	gitT(t, dir, "checkout", "-q", "main")
	writeT(t, dir, "docs/guide.md", "guide\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "Add the guide (#7)")
	gitT(t, dir, "revert", "--no-edit", "HEAD")
	return dir
}

func hotspotRuns(t0 time.Time) []orchestrator.CompletedRun {
	return []orchestrator.CompletedRun{
		{RunID: "run-1", Identifier: "ENG-1", Status: "failed", Kind: "worker", BranchName: "feat/eng-1", FinishedAt: t0},
		{RunID: "run-2", Identifier: "ENG-2", Status: "succeeded", Kind: "reviewer", ReviewVerdict: orchestrator.ReviewVerdictBlock,
			BranchName: "feat/eng-2", FinishedAt: t0.Add(time.Minute)},
		{RunID: "run-3", Identifier: "ENG-3", Status: "succeeded", Kind: "worker", BranchName: "feat/eng-3",
			PRURL: "https://github.com/acme/app/pull/7", FinishedAt: t0.Add(2 * time.Minute)},
		// Went fine: never looked up.
		{RunID: "run-4", Identifier: "ENG-4", Status: "succeeded", Kind: "worker", BranchName: "feat/eng-1", FinishedAt: t0.Add(3 * time.Minute)},
		// Asked for input on a branch this repository does not have.
		{RunID: "run-5", Identifier: "ENG-5", Status: "input_required", BranchName: "feat/elsewhere", FinishedAt: t0.Add(4 * time.Minute)},
	}
}

func rowByPath(rows []server.RepoHotspotRow, path string) server.RepoHotspotRow {
	for _, r := range rows {
		if r.Path == path {
			return r
		}
	}
	return server.RepoHotspotRow{}
}

// TestHotspotsAttributeFailuresToChangedFiles (#201): runs that went wrong
// are attributed to the files their branch changed — a failed run and a
// reviewer change request through their branch diffs, a reverted PR through
// its squash commit on the base — and ranked per file and directory.
func TestHotspotsAttributeFailuresToChangedFiles(t *testing.T) {
	repo := hotspotFixture(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	resp, cache := computeHotspots(t.Context(), gitexecHotspotGit, hotspotSource{repo: repo, base: "main"},
		hotspotRuns(t0), map[string]hotspotRunFiles{}, t0)

	assert.Equal(t, "refs/heads/main", resp.BaseRef)
	assert.Equal(t, 5, resp.RunsConsidered)
	assert.Equal(t, 4, resp.RunsWithSignals, "failed, change request, reverted, input request")
	assert.Equal(t, 3, resp.RunsAttributed)
	assert.Equal(t, 1, resp.RunsUnattributed, "ENG-5's branch is not in the repository")

	x := rowByPath(resp.Files, "a/x.go")
	assert.Equal(t, server.RepoHotspotRow{Path: "a/x.go", Failed: 1, ChangeRequests: 1, Total: 2,
		Issues: []string{"ENG-1", "ENG-2"}, Runs: x.Runs}, x)
	require.Len(t, x.Runs, 2)
	assert.Equal(t, "run-2", x.Runs[0].RunID, "newest run first")
	assert.Equal(t, []string{server.HotspotSignalChangeRequest}, x.Runs[0].Signals)
	assert.Equal(t, []string{server.HotspotSignalFailed}, x.Runs[1].Signals)
	assert.Equal(t, 1, rowByPath(resp.Files, "a/z.go").Failed)
	assert.Equal(t, 1, rowByPath(resp.Files, "b/y.go").ChangeRequests)
	guide := rowByPath(resp.Files, "docs/guide.md")
	assert.Equal(t, 1, guide.Reverted, "the reverted PR's squash commit touched the guide")
	assert.Equal(t, []string{"ENG-3"}, guide.Issues)
	assert.Equal(t, "a/x.go", resp.Files[0].Path, "the file with the most problems ranks first")
	assert.Empty(t, rowByPath(resp.Files, "README.md").Path, "no problem run changed README.md")

	a := rowByPath(resp.Directories, "a/")
	assert.Equal(t, 2, a.Total, "a run that changed two files in a/ counts once for a/")
	assert.Equal(t, 1, rowByPath(resp.Directories, "docs/").Reverted)

	assert.Equal(t, hotspotByBranch, cache["run-1"].method)
	assert.Equal(t, hotspotByPRCommit, cache["run-3"].method)
	assert.False(t, cache["run-5"].resolved)
	_, looked := cache["run-4"]
	assert.False(t, looked, "a run that went fine is never looked up")
	require.NotEmpty(t, resp.Notes)
	assert.Contains(t, strings.Join(resp.Notes, "\n"), "1 run(s) that went wrong could not be attributed")
}

// wait blocks until the in-flight refresh, if any, has finished (tests).
func (h *hotspotService) wait() {
	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// countingGit wraps the real git and records every call.
type countingGit struct {
	mu    sync.Mutex
	calls [][]string
}

func (c *countingGit) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	c.mu.Lock()
	c.calls = append(c.calls, args)
	c.mu.Unlock()
	return gitexecHotspotGit(ctx, dir, args...)
}

// take returns and forgets the calls so far.
func (c *countingGit) take() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.calls
	c.calls = nil
	return out
}

// runCalls are the calls that looked up a run's files (a diff, or the
// search for a PR's commit), as opposed to the per-refresh base and revert
// reads.
func runCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if c[0] == "diff" || slices.ContainsFunc(c, func(a string) bool { return strings.HasPrefix(a, "--grep=") }) {
			out = append(out, c)
		}
	}
	return out
}

// TestHotspotsCachePerRunID (#201): runs already attributed are never looked
// up again; a request inside the refresh interval runs no git at all, and a
// later refresh only looks up the runs it has not seen.
func TestHotspotsCachePerRunID(t *testing.T) {
	repo := hotspotFixture(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := t0
	var clockMu sync.Mutex
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	advance := func(d time.Duration) { clockMu.Lock(); clock = clock.Add(d); clockMu.Unlock() }

	git := &countingGit{}
	runs := hotspotRuns(t0)
	var histMu sync.Mutex
	hist := func() []orchestrator.CompletedRun { histMu.Lock(); defer histMu.Unlock(); return slices.Clone(runs) }
	svc := newHotspotService(git.run, now)
	svc.bind(hist, hotspotSource{repo: repo, base: "main"})

	first := svc.get(t.Context())
	assert.True(t, first.Refreshing, "the first request starts the background refresh")
	assert.Nil(t, first.GeneratedAt)
	svc.wait()
	firstCalls := runCalls(git.take())
	require.NotEmpty(t, firstCalls, "the first refresh looks the runs up")

	second := svc.get(t.Context())
	require.NotNil(t, second.GeneratedAt)
	assert.False(t, second.Refreshing)
	assert.Equal(t, 3, second.RunsAttributed)
	svc.wait()
	assert.Empty(t, git.take(), "a request inside the refresh interval runs no git")

	// Past the interval, with one new failed run: only that run is looked
	// up; ENG-5's unresolved lookup waits for its retry delay.
	advance(hotspotsRefreshEvery + time.Second)
	histMu.Lock()
	runs = append(runs, orchestrator.CompletedRun{RunID: "run-6", Identifier: "ENG-6", Status: "stalled",
		BranchName: "feat/eng-2", FinishedAt: t0.Add(time.Hour)})
	histMu.Unlock()
	assert.True(t, svc.get(t.Context()).Refreshing)
	svc.wait()
	again := runCalls(git.take())
	// run-6 alone: its origin ref misses, its local branch diffs.
	require.Len(t, again, 2, "only the new run is looked up; got %v", again)
	for _, c := range again {
		joined := strings.Join(c, " ")
		assert.Contains(t, joined, "feat/eng-2", "only the new run-6 (on feat/eng-2) is looked up; got %v", c)
	}
	third := svc.get(t.Context())
	assert.Equal(t, 4, third.RunsAttributed)
	assert.Equal(t, 1, rowByPath(third.Files, "b/y.go").Stalled)

	// A different repository drops the cache.
	svc.bind(hist, hotspotSource{repo: t.TempDir(), base: "main"})
	assert.Nil(t, svc.get(t.Context()).GeneratedAt)
	svc.wait()
}

// TestHotspotsRequestNeverWaitsForGit (#201): the endpoint answers with the
// latest result while the background job is still blocked in git.
func TestHotspotsRequestNeverWaitsForGit(t *testing.T) {
	release := make(chan struct{})
	blocked := func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		<-release
		return nil, context.Canceled
	}
	svc := newHotspotService(blocked, time.Now)
	svc.bind(func() []orchestrator.CompletedRun { return hotspotRuns(time.Now()) }, hotspotSource{repo: t.TempDir(), base: "main"})
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		AllowedHosts: []string{"example.com"},
		RepoHotspots: svc.get,
	})
	for range 2 {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/repo/hotspots", nil))
			done <- w
		}()
		select {
		case w := <-done:
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var body server.RepoHotspotsResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.True(t, body.Refreshing)
			assert.NotNil(t, body.Files, "files is always a list")
		case <-time.After(5 * time.Second):
			t.Fatal("GET /api/v1/repo/hotspots waited for git")
		}
	}
	close(release)
	svc.wait()
}

func TestRepoAnalysisEndpointsUnwiredAnswer503(t *testing.T) {
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		AllowedHosts: []string{"example.com"},
	})
	for _, path := range []string{"/api/v1/repo/evals", "/api/v1/repo/hotspots"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, path)
	}
}

func TestHotspotSourceFor(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.BaseBranch = "develop"
	src := hotspotSourceFor(cfg, "/proj/WORKFLOW.md")
	assert.Equal(t, hotspotSource{repo: "/proj", base: "develop"}, src)
	cfg.Workspace.Worktree, cfg.Workspace.Root = true, "/ws"
	assert.Equal(t, "/ws", hotspotSourceFor(cfg, "/proj/WORKFLOW.md").repo)
	cfg.Workspace.CloneURL = "git@example.com:a/b.git"
	assert.Equal(t, filepath.Join("/ws", ".bare"), hotspotSourceFor(cfg, "/proj/WORKFLOW.md").repo)
	cfg.Workspace.BaseBranch = ""
	assert.Equal(t, "main", hotspotSourceFor(cfg, "/proj/WORKFLOW.md").base)
}

func TestPRNumberAndRevertParsing(t *testing.T) {
	assert.Equal(t, 7, prNumber("https://github.com/acme/app/pull/7"))
	assert.Equal(t, 12, prNumber("https://gitlab.com/acme/app/-/merge_requests/12/diffs"))
	assert.Equal(t, 0, prNumber(""))
	assert.Equal(t, []string{"a/", "a/b/"}, parentDirs("a/b/c.go"))
	assert.Nil(t, parentDirs("README.md"))
	assert.False(t, safeRefPart("-x"))
	assert.False(t, safeRefPart("a..b"))
	assert.True(t, safeRefPart("itervox/eng-1"))
}
