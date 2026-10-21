package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestMain keeps doctor's readiness section (#200) off the network for every
// test in the package: the tracker probe is stubbed unless a test swaps
// doctorReadinessEnv itself. Tests that exercise a tracker API do so through
// their own fake servers.
func TestMain(m *testing.M) {
	doctorReadinessEnv = func() repoReadinessEnv {
		return stubTrackerProbe(deploySkipped, "not probed in tests")
	}
	os.Exit(m.Run())
}

// stubTrackerProbe answers the tracker readiness probe without the network.
func stubTrackerProbe(status deployStatus, detail string) repoReadinessEnv {
	return repoReadinessEnv{
		trackerProbe: func(context.Context, *config.Config) deployCheck {
			return deployCheck{Name: "tracker API", Status: status, Detail: detail}
		},
		trackerTimeout: 2 * time.Second,
		now:            time.Now,
	}
}

// readinessFixture writes a repository whose readiness rows cover every
// status: the root .gitignore hides .itervox/ without the carve-outs (warn),
// a profile names a subagent that does not exist (warn), a Codex profile
// has no AGENTS.md (warn), and the schema marker, bare clone and
// CLAUDE.md pass.
func readinessFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	prev := doctorHomeDir
	doctorHomeDir = func() string { return home }
	t.Cleanup(func() { doctorHomeDir = prev })
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	write(".gitignore", "node_modules/\n.itervox/\n")
	write("CLAUDE.md", "# Conventions\n")
	write(".itervox/agents/impl/SOUL.md", "You are the implementer.\n")
	write(".itervox/agents/impl/INSTRUCTIONS.md", "Ask @agent-ghost-reviewer for a review.\n")
	write(".itervox/agents/qa/SOUL.md", "You are QA.\n")
	write(".itervox/agents/qa/INSTRUCTIONS.md", "Test it.\n")
	write("WORKFLOW.md", `---
itervox_schema_version: 2
tracker:
  kind: local
agent:
  command: claude
  profiles:
    impl:
      command: claude
      soul_file: .itervox/agents/impl/SOUL.md
      instructions_file: .itervox/agents/impl/INSTRUCTIONS.md
    qa:
      command: codex
      soul_file: .itervox/agents/qa/SOUL.md
      instructions_file: .itervox/agents/qa/INSTRUCTIONS.md
---

Prompt.
`)
	return filepath.Join(dir, "WORKFLOW.md")
}

type readinessKey struct{ ID, Status string }

func readinessKeys(checks []server.RepoReadinessCheck) []readinessKey {
	out := make([]readinessKey, 0, len(checks))
	for _, c := range checks {
		out = append(out, readinessKey{c.ID, c.Status})
	}
	return out
}

func getReadiness(t *testing.T, provider server.RepoReadinessFunc) server.RepoReadinessResponse {
	t.Helper()
	srv := server.New(server.Config{
		Snapshot:      func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:   make(chan struct{}, 1),
		AllowedHosts:  []string{"example.com"},
		RepoReadiness: provider,
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/repo/readiness", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body server.RepoReadinessResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

// TestRepoReadinessReusesDoctorChecks (#200): GET /api/v1/repo/readiness and
// `itervox doctor` report the same check IDs with the same statuses for the
// same repository, because both build their rows from doctor's check
// functions.
func TestRepoReadinessReusesDoctorChecks(t *testing.T) {
	wf := readinessFixture(t)
	env := stubTrackerProbe(deployFail, "linear `{ viewer }` query failed: 401")
	prev := doctorReadinessEnv
	doctorReadinessEnv = func() repoReadinessEnv { return env }
	t.Cleanup(func() { doctorReadinessEnv = prev })

	report, _ := collectDoctorReport(wf)
	require.True(t, report.SchemaPassed, report.WorkflowError)

	body := getReadiness(t, func(ctx context.Context) server.RepoReadinessResponse {
		return collectRepoReadiness(ctx, wf, env)
	})

	want := []readinessKey{
		{readinessSchemaVersion, server.RepoReadinessPass},
		{readinessGitignore, server.RepoReadinessWarn},
		{readinessInstructions, server.RepoReadinessWarn},
		{readinessBareFetch, server.RepoReadinessPass},
		{readinessProfileRefs, server.RepoReadinessWarn},
		{readinessTracker, server.RepoReadinessFail},
	}
	assert.Equal(t, want, readinessKeys(report.Readiness), "doctor")
	assert.Equal(t, want, readinessKeys(body.Checks), "endpoint")
	assert.Equal(t, report.Readiness, body.Checks, "the same rows, fix text included")

	for _, c := range body.Checks {
		if c.Status != server.RepoReadinessPass {
			assert.NotEmpty(t, c.Fix, "%s carries the fix doctor prints", c.ID)
		}
	}
	assert.Contains(t, body.Checks[1].Detail, "!.itervox/agents/**")
	assert.Contains(t, body.Checks[2].Detail, "AGENTS.md")
	assert.Contains(t, body.Checks[4].Detail, "ghost-reviewer")

	out := renderDoctorReport(report)
	assert.Contains(t, out, "repo readiness:")
	assert.Contains(t, out, "[warn] gitignore_carveouts:")
	assert.Contains(t, out, "[fail] tracker_reachable:")
	assert.Contains(t, out, "fix: run `itervox init --update --workflow "+wf+"`")
}

// TestRepoReadinessCarveOutsMatchInit (#200): a .gitignore patched by
// `itervox init --update` passes the readiness check it is judged by.
func TestRepoReadinessCarveOutsMatchInit(t *testing.T) {
	wf := readinessFixture(t)
	require.NoError(t, patchRootGitignoreForAgents(filepath.Dir(wf)))
	c := gitignoreReadiness(wf)
	assert.Equal(t, server.RepoReadinessPass, c.Status, c.Detail)
}

// TestRepoReadinessWarnsWithoutMemoryCarveOut (#202): a root .gitignore
// that hides .itervox/ and has the agents and handoff carve-outs but not
// !.itervox/memory.json is a warning naming the missing line, so the
// project-shared memory is not silently left out of commits.
func TestRepoReadinessWarnsWithoutMemoryCarveOut(t *testing.T) {
	wf := readinessFixture(t)
	lines := []string{"node_modules/", ".itervox/", "!.itervox/", "!.itervox/agents/", "!.itervox/agents/**", "!.itervox/handoff/", "!.itervox/handoff/**"}
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(wf), ".gitignore"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	c := gitignoreReadiness(wf)
	assert.Equal(t, server.RepoReadinessWarn, c.Status)
	assert.Contains(t, c.Detail, "!.itervox/memory.json")
	assert.NotContains(t, c.Detail, "!.itervox/agents/**")

	require.NoError(t, patchRootGitignoreForAgents(filepath.Dir(wf)))
	assert.Equal(t, server.RepoReadinessPass, gitignoreReadiness(wf).Status, "init --update adds it")
}

// countingTracker counts the candidate reads, one per orchestrator tick.
type countingTracker struct {
	*tracker.MemoryTracker
	ticks atomic.Int64
}

func (c *countingTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	c.ticks.Add(1)
	return c.MemoryTracker.FetchCandidateIssues(ctx)
}

// TestRepoReadinessNeverBlocksEventLoop (#200): a tracker probe that hangs
// (and ignores its context) costs the readiness request its timeout and
// nothing more — the request answers with a failed tracker row — while the
// orchestrator keeps ticking throughout, because the checks run on the
// request goroutine, never on the event loop.
func TestRepoReadinessNeverBlocksEventLoop(t *testing.T) {
	wf := readinessFixture(t)

	cfg := pollTestConfig()
	cfg.Polling.IntervalMs = 20
	tr := &countingTracker{MemoryTracker: tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	runPollOrchestrator(t, cfg, tr)
	require.Eventually(t, func() bool { return tr.ticks.Load() > 0 }, 3*time.Second, 5*time.Millisecond)

	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	const timeout = 400 * time.Millisecond
	env := repoReadinessEnv{
		trackerProbe: func(context.Context, *config.Config) deployCheck {
			<-hung // never answers, never looks at its context
			return deployCheck{Status: deployOK}
		},
		trackerTimeout: timeout,
		now:            time.Now,
	}

	before := tr.ticks.Load()
	start := time.Now()
	body := getReadiness(t, func(ctx context.Context) server.RepoReadinessResponse {
		return collectRepoReadiness(ctx, wf, env)
	})
	elapsed := time.Since(start)
	during := tr.ticks.Load() - before

	assert.GreaterOrEqual(t, elapsed, timeout, "the request waited for the probe's timeout")
	assert.Less(t, elapsed, timeout+3*time.Second, "and no longer")
	assert.GreaterOrEqual(t, during, int64(3), "the event loop kept ticking while the probe hung (%d ticks in %s)", during, elapsed)

	last := body.Checks[len(body.Checks)-1]
	assert.Equal(t, readinessTracker, last.ID)
	assert.Equal(t, server.RepoReadinessFail, last.Status)
	assert.True(t, strings.Contains(last.Detail, "did not answer within"), last.Detail)
}

// TestRepoReadinessUnwiredAnswers503 (#200): a server built without the
// provider answers 503, never a fabricated report.
func TestRepoReadinessUnwiredAnswers503(t *testing.T) {
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		AllowedHosts: []string{"example.com"},
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/repo/readiness", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
