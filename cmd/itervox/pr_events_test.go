package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/prwatch"
	"github.com/vnovick/itervox/internal/tracker"
)

const e2ePRURL = "https://github.com/acme/app/pull/42"

// prEventPromptRunner records every prompt it is given.
type prEventPromptRunner struct {
	agent.Runner
	prompts chan string
}

func (r *prEventPromptRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int, mode agent.PermissionMode) (agent.TurnResult, error) {
	select {
	case r.prompts <- prompt:
	default:
	}
	return r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs, mode)
}

// prEventsE2E runs a real orchestrator whose pr_events.json already records
// e2ePRURL as ENG-1's managed PR, with a pr_check_completed rule whose
// instructions print every trigger variable. It returns the orchestrator,
// its tracker, the config and the channel of prompts its runs received.
func prEventsE2E(t *testing.T, webhookSecret string) (*orchestrator.Orchestrator, tracker.Tracker, *config.Config, chan string) {
	t.Helper()
	cfg := &config.Config{
		Polling: config.PollingConfig{IntervalMs: 50},
		Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}},
		Agent: config.AgentConfig{
			Command: "claude", MaxConcurrentAgents: 2, MaxTurns: 1, TurnTimeoutMs: 60000, ReadTimeoutMs: 30000,
			Profiles: map[string]config.AgentProfile{"fixer": {Command: "claude"}},
		},
		PREvents: config.PREventsConfig{WebhookSecret: webhookSecret},
		Automations: []config.AutomationConfig{{
			ID: "fix-ci", Enabled: true, Profile: "fixer",
			Instructions: "VARS check={{ trigger.check_name }} conclusion={{ trigger.check_conclusion }} " +
				"url={{ trigger.pr_url }} number={{ trigger.pr_number }} branch={{ trigger.pr_branch }} " +
				"sha={{ trigger.head_sha }} mergeable={{ trigger.mergeable_state }} type={{ trigger.type }}",
			Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerPRCheckCompleted},
			Filter:  config.AutomationFilterConfig{CheckNames: []string{"ci"}, Conclusions: []string{"failure"}},
		}},
	}
	tr := tracker.NewMemoryTracker([]domain.Issue{{ID: "id-1", Identifier: "ENG-1", Title: "Ship it", State: "In Review"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	path := filepath.Join(t.TempDir(), "pr_events.json")
	ledger, err := json.Marshal(map[string]any{"managed_prs": map[string]any{
		e2ePRURL: map[string]any{"pr_url": e2ePRURL, "issue_id": "id-1", "identifier": "ENG-1",
			"since": time.Now().Add(-time.Hour), "recorded_at": time.Now()},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, ledger, 0o600))

	runner := &prEventPromptRunner{
		Runner:  agenttest.NewFakeRunner([]agent.StreamEvent{{Type: "system", SessionID: "s1"}, {Type: "result", SessionID: "s1"}}),
		prompts: make(chan string, 4),
	}
	orch := orchestrator.New(cfg, tr, runner, nil)
	orch.SetPREventsFile(path)
	orch.SetPREventAutomations(compileAutomations(cfg).prEvents)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runDone := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})
	require.Eventually(t, func() bool { return len(orch.ManagedPRs()) == 1 }, 3*time.Second, 10*time.Millisecond,
		"the persisted managed PR is loaded")
	return orch, tr, cfg, runner.prompts
}

func waitPrompt(t *testing.T, prompts chan string) string {
	t.Helper()
	select {
	case p := <-prompts:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no automation run started")
		return ""
	}
}

func signed(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestPRCheckCompletedWebhookDeliveryDispatches (#173): a signed GitHub
// check_run delivery for a managed PR runs the matching automation, whose
// instructions see every trigger variable; a re-delivery runs nothing more.
func TestPRCheckCompletedWebhookDeliveryDispatches(t *testing.T) {
	const secret = "0123456789abcdef-secret"
	orch, tr, cfg, prompts := prEventsE2E(t, secret)
	w := newPREventWatcher(cfg, tr, orch, nil)
	h, err := prwatch.NewHandler(secret, w)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)

	body := []byte(`{"action":"completed","check_run":{"name":"ci","head_sha":"aaa111","status":"completed",
		"conclusion":"failure","completed_at":"` + time.Now().UTC().Format(time.RFC3339) + `",
		"pull_requests":[{"number":42,"head":{"sha":"aaa111","ref":"itervox/eng-1"}}]},
		"repository":{"full_name":"acme/app","html_url":"https://github.com/acme/app"}}`)
	deliver := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pr-events", strings.NewReader(string(body)))
		req.Header.Set("X-GitHub-Event", "check_run")
		req.Header.Set("X-Hub-Signature-256", signed(secret, body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusAccepted, deliver())

	p := waitPrompt(t, prompts)
	assert.Contains(t, p, "VARS check=ci conclusion=failure url="+e2ePRURL+" number=42 branch=itervox/eng-1 sha=aaa111 mergeable= type=pr_check_completed")

	require.Equal(t, http.StatusAccepted, deliver(), "a re-delivery is accepted")
	select {
	case p := <-prompts:
		t.Fatalf("a re-delivered check ran the automation again: %q", p)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, uint64(1), orch.Snapshot().AutomationDispatchesPREventTotal)
}

// fakePRGitHub serves one open PR (#42, head aaa111) with a failed "ci" check
// run, counting requests and answering conditional requests with 304.
type fakePRGitHub struct {
	requests, notModified atomic.Int64
}

func (g *fakePRGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.requests.Add(1)
	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	etag := `"` + r.URL.Path + `"`
	if r.Header.Get("If-None-Match") == etag {
		g.notModified.Add(1)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	switch r.URL.Path {
	case "/repos/acme/app/pulls/42":
		_, _ = w.Write([]byte(`{"state":"open","number":42,"html_url":"` + e2ePRURL + `","mergeable_state":"unstable",
			"head":{"sha":"aaa111","ref":"itervox/eng-1"}}`))
	case "/repos/acme/app/commits/aaa111/check-runs":
		_, _ = w.Write([]byte(`{"check_runs":[{"id":1,"name":"ci","status":"completed","conclusion":"failure",
			"completed_at":"` + time.Now().UTC().Format(time.RFC3339) + `"},{"id":2,"name":"lint","status":"in_progress"}]}`))
	case "/repos/acme/app/commits/aaa111/statuses":
		_, _ = w.Write([]byte(`[]`))
	default:
		http.NotFound(w, r)
	}
}

// TestPRCheckCompletedPollDispatches (#173): with no webhook configured, the
// poll of the open managed PRs finds the failed check and runs the matching
// automation with every trigger variable; a second poll costs only 304s and
// runs nothing more.
func TestPRCheckCompletedPollDispatches(t *testing.T) {
	orch, tr, cfg, prompts := prEventsE2E(t, "")
	gh := &fakePRGitHub{}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	client := &prwatch.Client{HTTP: srv.Client(), Token: func(string) string { return "test-token" },
		BaseURL: func(string) string { return srv.URL }}
	w := newPREventWatcher(cfg, tr, orch, client)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w.Poll(ctx)
	p := waitPrompt(t, prompts)
	assert.Contains(t, p, "VARS check=ci conclusion=failure url="+e2ePRURL+" number=42 branch=itervox/eng-1 sha=aaa111 mergeable=unstable type=pr_check_completed")

	before := gh.requests.Load()
	w.Poll(ctx)
	assert.Equal(t, gh.requests.Load()-before, gh.notModified.Load(), "an unchanged PR is re-read with conditional requests only")
	select {
	case p := <-prompts:
		t.Fatalf("a repeated poll ran the automation again: %q", p)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, uint64(1), orch.Snapshot().AutomationDispatchesPREventTotal)
}

// fakePREventOrch records what the sink hands the orchestrator.
type fakePREventOrch struct {
	kinds map[string]bool
	got   []orchestrator.PREvent
	issue domain.Issue
}

func (f *fakePREventOrch) ManagedPRs() []orchestrator.ManagedPR { return nil }
func (f *fakePREventOrch) PREventTriggerTypes() map[string]bool { return f.kinds }
func (f *fakePREventOrch) DispatchPREventAutomations(_ context.Context, issue domain.Issue, ev orchestrator.PREvent) int {
	f.issue = issue
	f.got = append(f.got, ev)
	return 1
}

// TestPREventSinkSkipsKindsWithoutRules (#173): an event no rule can use is
// dropped before the tracker is read; a used one reaches the orchestrator
// with its issue and fields.
func TestPREventSinkSkipsKindsWithoutRules(t *testing.T) {
	tr := tracker.NewMemoryTracker([]domain.Issue{{ID: "id-1", Identifier: "ENG-1", State: "In Review"}}, nil, nil)
	f := &fakePREventOrch{kinds: map[string]bool{prwatch.ReviewSubmitted: true}}
	sink := prEventSink(tr, f)
	sink(t.Context(), prwatch.Event{Kind: prwatch.CheckCompleted, Identifier: "ENG-1", PRURL: e2ePRURL})
	assert.Empty(t, f.got)
	sink(t.Context(), prwatch.Event{Kind: prwatch.ReviewSubmitted, Identifier: "ENG-1", PRURL: e2ePRURL,
		ReviewID: "9", ReviewAuthor: "greptile-apps[bot]", ReviewState: "commented", Source: prwatch.SourcePoll})
	require.Len(t, f.got, 1)
	assert.Equal(t, "ENG-1", f.issue.Identifier)
	assert.Equal(t, config.AutomationTriggerPRReviewSubmitted, f.got[0].TriggerType)
	assert.Equal(t, "greptile-apps[bot]", f.got[0].ReviewAuthor)
	assert.Equal(t, "9", f.got[0].ReviewID)
}

// TestPREventsTokenSource (#173): the tracker's GitHub key wins, then the
// environment, then the gh CLI login; each host is resolved once.
func TestPREventsTokenSource(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	calls := 0
	orig := ghAuthToken
	ghAuthToken = func(_ context.Context, host string) string { calls++; return "gh-" + host }
	t.Cleanup(func() { ghAuthToken = orig })

	linear := &config.Config{Tracker: config.TrackerConfig{Kind: "linear", APIKey: "lin_api_x"}}
	src := prEventsTokenSource(linear)
	assert.Equal(t, "gh-github.com", src("github.com"), "a Linear key is never sent to GitHub")
	assert.Equal(t, "gh-github.com", src("github.com"))
	assert.Equal(t, 1, calls, "resolved once per host")

	t.Setenv("GITHUB_TOKEN", "env-token")
	assert.Equal(t, "env-token", prEventsTokenSource(linear)("github.com"))
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "ghe-token")
	assert.Equal(t, "ghe-token", prEventsTokenSource(linear)("ghe.example.com"))

	gh := &config.Config{Tracker: config.TrackerConfig{Kind: "github", APIKey: "tracker-key"}}
	assert.Equal(t, "tracker-key", prEventsTokenSource(gh)("github.com"))
}
