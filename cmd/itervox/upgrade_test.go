package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
)

// Upgrade from the previous release (#138).
//
// testdata/upgrade/v0.2.1/project is a project as v0.2.1 left it: WORKFLOW.md,
// .gitignore and .itervox/agents/ from v0.2.1's `itervox init --tracker
// github`, and the ledgers its daemon writes (logs/: paused issues with a
// pause reason, an open backend breaker, a pending review, a question
// awaiting an answer, run history; .itervox/outbox.json: an undelivered
// state move and comment), written by v0.2.1's own persistence code (see
// gen_ledgers_test.go.txt). The test runs this build's `itervox init
// --update` on it, then starts the real daemon and reads every ledger back.

const v021FixtureDir = "testdata/upgrade/v0.2.1/project"

// fakeGitHub answers the daemon's GitHub reads offline: every list is empty,
// and every single-issue read fails (500), so the daemon keeps each ledger
// entry ("retry next tick") rather than resolving it, and its reads show
// which issues it loaded. Writes fail too, so the outbox keeps its entries.
type fakeGitHub struct {
	mu    sync.Mutex
	reads map[string]bool // issue number -> read
}

var issuePath = regexp.MustCompile(`^/repos/owner/repo/issues/(\d+)`)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m := issuePath.FindStringSubmatch(r.URL.Path); m != nil {
		f.mu.Lock()
		f.reads[m[1]] = true
		f.mu.Unlock()
		http.Error(w, "fixture: unavailable", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "fixture: read-only", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("[]"))
}

func (f *fakeGitHub) read(n string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[n]
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
	require.NoError(t, err)
}

// upgradeState is the part of GET /api/v1/state the test reads.
type upgradeState struct {
	Paused        []string          `json:"paused"`
	PauseReasons  map[string]string `json:"pauseReasons"`
	BackendHealth []struct {
		Backend   string `json:"backend"`
		Status    string `json:"status"`
		Kind      string `json:"kind"`
		LimitType string `json:"limitType"`
	} `json:"backendHealth"`
	InputRequired []struct {
		Identifier string `json:"identifier"`
		SessionID  string `json:"sessionId"`
		Context    string `json:"context"`
	} `json:"inputRequired"`
	History []struct {
		Identifier string `json:"identifier"`
		Status     string `json:"status"`
	} `json:"history"`
	DispatchPressure *struct {
		ObservedTicks int64 `json:"observedTicks"`
	} `json:"dispatchPressure"`
	OutboxEntries []struct {
		Kind        string `json:"kind"`
		Identifier  string `json:"identifier"`
		TargetState string `json:"targetState"`
	} `json:"outboxEntries"`
}

func getUpgradeState(t *testing.T, p *daemonProject, token string) upgradeState {
	t.Helper()
	base := readTrim(t, dashboardURLFilePath(p.Workflow))
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(base, "/")+"/api/v1/state", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var s upgradeState
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
	return s
}

// TestUpgradeFromV021: this build's `init --update` accepts a v0.2.1
// project, the daemon starts with its config, and every v0.2.1 ledger is
// read back intact.
func TestUpgradeFromV021(t *testing.T) {
	p := newDaemonProject(t) // the child environment: stub claude, HOME, dry run
	dir := filepath.Join(t.TempDir(), "project")
	copyTree(t, v021FixtureDir, dir)
	p.Dir, p.Workflow, p.LogsDir = dir, filepath.Join(dir, "WORKFLOW.md"), filepath.Join(dir, "logs")
	original, err := os.ReadFile(p.Workflow)
	require.NoError(t, err)

	// 1. `itervox init --update`, through the real main().
	raw, err := json.Marshal([]string{"init", "--update", "--workflow", p.Workflow})
	require.NoError(t, err)
	update := exec.Command(os.Args[0])
	update.Dir = dir
	update.Env = append(append([]string{}, p.env...), daemonChildArgsEnv+"="+string(raw))
	out, err := update.CombinedOutput()
	require.NoError(t, err, "init --update failed:\n%s", out)
	assert.Contains(t, string(out), "already uses schema 2")
	migrated, err := os.ReadFile(p.Workflow)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(migrated), "a v0.2.1 workflow needs no rewrite")
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	for _, carve := range []string{"!.itervox/agents/**", "!.itervox/handoff/**"} {
		assert.Contains(t, string(gitignore), carve)
	}

	// 2. The migrated config loads, profiles and all.
	t.Setenv("GITHUB_TOKEN", "fixture-token") // tracker.api_key: $GITHUB_TOKEN
	cfg, err := config.Load(p.Workflow)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	assert.Equal(t, "github", cfg.Tracker.Kind)
	for _, name := range []string{"implementer", "reviewer", "input-responder", "deps-analyzer"} {
		assert.Contains(t, cfg.Agent.Profiles, name)
	}
	assert.Equal(t, "reviewer", cfg.Agent.ReviewerProfile)

	// 3. The daemon starts on it. Only the tracker endpoint is pointed at
	// the fake GitHub API, so the test runs offline.
	gh := &fakeGitHub{reads: map[string]bool{}}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	require.NoError(t, os.WriteFile(p.Workflow, []byte(strings.Replace(string(migrated),
		"  kind: github\n", "  kind: github\n  endpoint: "+srv.URL+"\n", 1)), 0o644))
	p.env = append(p.env, "GITHUB_TOKEN=fixture-token")
	p.startHeadless(t, nil)
	tokenFile := filepath.Join(p.LogsDir, apiTokenFileName)
	waitFor(t, 60*time.Second, "the upgraded daemon serves its dashboard", func() bool {
		_, e1 := os.Stat(dashboardURLFilePath(p.Workflow))
		_, e2 := os.Stat(tokenFile)
		return e1 == nil && e2 == nil
	})
	token := readTrim(t, tokenFile)

	// 4. Every ledger is read back.
	var s upgradeState
	// A snapshot from after the first tick: the ledgers are loaded before
	// the event loop's first tick, so every one is in it.
	waitFor(t, 30*time.Second, "a snapshot published after the first tick", func() bool {
		s = getUpgradeState(t, p, token)
		return s.DispatchPressure != nil && s.DispatchPressure.ObservedTicks > 0
	})
	waitFor(t, 30*time.Second, "the daemon reads the pending review's issue", func() bool { return gh.read("11") })
	assert.Contains(t, s.Paused, "#7", "paused.json")
	assert.Equal(t, "retries_exhausted", s.PauseReasons["#7"], "paused.json.reasons.json")
	require.Len(t, s.BackendHealth, 1, "backend_health.json")
	assert.Equal(t, "claude", s.BackendHealth[0].Backend)
	assert.Equal(t, "limited", s.BackendHealth[0].Status)
	assert.Equal(t, "quota", s.BackendHealth[0].Kind)
	assert.Equal(t, "five_hour", s.BackendHealth[0].LimitType)
	require.Len(t, s.InputRequired, 1, "input_required.json")
	assert.Equal(t, "#5", s.InputRequired[0].Identifier)
	assert.Equal(t, "sess-5", s.InputRequired[0].SessionID)
	assert.Equal(t, "Which database should I use?", s.InputRequired[0].Context)
	require.Len(t, s.History, 1, "history.json")
	assert.Equal(t, "#3", s.History[0].Identifier)
	assert.Equal(t, "succeeded", s.History[0].Status)
	require.Len(t, s.OutboxEntries, 2, "outbox.json")
	kinds := map[string]string{}
	for _, e := range s.OutboxEntries {
		assert.Equal(t, "#8", e.Identifier)
		kinds[e.Kind] = e.TargetState
	}
	assert.Equal(t, map[string]string{"update_state": "in-review", "create_comment": ""}, kinds)
	assert.True(t, gh.read("11"), "the daemon loaded the pending review and went to resume it")

	// 5. After a clean shutdown the files still hold the same records, in a
	// form this build reads.
	p.stopDaemonByPIDFile(t)
	assert.Contains(t, orchestrator.LoadPendingReviewIdentifiers(filepath.Join(p.LogsDir, "pending_reviews.json")), "#11")
	var pending map[string]orchestrator.PendingReview
	data, err := os.ReadFile(filepath.Join(p.LogsDir, "pending_reviews.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &pending))
	assert.Equal(t, orchestrator.PendingReview{
		IssueID: "11", Identifier: "#11", Profile: "reviewer",
		QueuedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), IssueState: "in-review",
	}, pending["#11"])
	ob, err := outbox.New(filepath.Join(dir, ".itervox", "outbox.json"))
	require.NoError(t, err)
	entries := ob.Snapshot()
	require.Len(t, entries, 2)
	for _, e := range entries {
		if e.Kind == outbox.KindCreateComment {
			assert.Equal(t, "Opened a pull request.", e.Body)
			assert.Equal(t, "4b1d2c3e-5f60-4718-8a9b-0c1d2e3f4a5b", e.CommentKey)
		} else {
			assert.Equal(t, "in-progress", e.FromState)
		}
	}
}
