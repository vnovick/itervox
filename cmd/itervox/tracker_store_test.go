package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
	jiraclient "github.com/vnovick/itervox/internal/tracker/jira"
)

// TestWithTrackerStore (#113): the store goes in front of a GitHub or Linear
// adapter only when tracker.store.enabled is set, and the scope changes with
// the project and the active states.
func TestWithTrackerStore(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "github"
	cfg.Tracker.ProjectSlug = "owner/repo"
	cfg.Tracker.ActiveStates = []string{"todo"}
	cfg.Tracker.TerminalStates = []string{"done"}
	cfg.Tracker.Store.SyncIntervalMs = 60000
	gh := ghclient.NewClient(ghclient.ClientConfig{APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: "http://127.0.0.1:1"})
	workflow := t.TempDir() + "/WORKFLOW.md"

	off, noHooks := withTrackerStore(t.Context(), cfg, workflow, gh)
	assert.Same(t, gh, off, "off when disabled (enabled: false)")
	assert.Nil(t, noHooks)

	cfg.Tracker.Store.Enabled = true
	wrapped, hooks := withTrackerStore(t.Context(), cfg, workflow, gh)
	assert.Nil(t, hooks, "webhooks are off by default")
	assert.NotSame(t, tracker.Tracker(gh), wrapped)
	_, ok := wrapped.(tracker.IdempotentCommenter)
	assert.True(t, ok)
	cc, ok := commentCommandsTracker(wrapped).(commentCommandTracker)
	require.True(t, ok, "comment commands reach GitHub's repository-comment reads behind the store")
	// Issue writes from a command go through the store, so the next tick
	// sees `/itervox run` without waiting for a sync.
	assert.Same(t, wrapped, cc.(storeCommentCommands).Tracker)
	assert.Same(t, gh, commentCommandsTracker(gh))

	cfg.Tracker.Kind = "local"
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	local, _ := withTrackerStore(t.Context(), cfg, workflow, mem)
	assert.Same(t, mem, local, "local trackers need no store")

	cfg.Tracker.Kind = "github"
	scope := trackerStoreScope(cfg)
	cfg.Tracker.ActiveStates = []string{"TODO"}
	require.Equal(t, scope, trackerStoreScope(cfg), "case does not change the scope")
	cfg.Tracker.ActiveStates = []string{"todo", "doing"}
	assert.NotEqual(t, scope, trackerStoreScope(cfg))
}

// TestWithTrackerStoreJira: a Jira tracker gets the store like Linear and
// GitHub when tracker.store.enabled is set (the default), and the store
// keeps the adapter's optional interfaces: the scope check the orchestrator
// runs on the by-ID read of a retry and of a dispatch claim check, keyed
// comments and the state-list setter.
func TestWithTrackerStoreJira(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "jira"
	cfg.Tracker.Endpoint = "http://127.0.0.1:1"
	cfg.Tracker.ProjectSlug = "KAN"
	cfg.Tracker.ActiveStates = []string{"To Do"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Tracker.Labels = []string{"agent"}
	cfg.Tracker.Store.SyncIntervalMs = 60000
	jc := jiraclient.NewClient(jiraclient.ClientConfig{Endpoint: cfg.Tracker.Endpoint, APIKey: "me@example.com:tok", ProjectSlug: "KAN", ActiveStates: cfg.Tracker.ActiveStates, Labels: cfg.Tracker.Labels})
	workflow := t.TempDir() + "/WORKFLOW.md"

	off, _ := withTrackerStore(t.Context(), cfg, workflow, jc)
	assert.Same(t, jc, off, "off when disabled (enabled: false)")

	cfg.Tracker.Store.Enabled = true
	wrapped, hooks := withTrackerStore(t.Context(), cfg, workflow, jc)
	assert.Nil(t, hooks)
	require.NotSame(t, tracker.Tracker(jc), wrapped, "the store is in front of the Jira adapter")
	_, isStore := wrapped.(interface{ Upstream() tracker.Tracker })
	assert.True(t, isStore)
	m, ok := wrapped.(tracker.ScopeMatcher)
	require.True(t, ok, "the scope check survives the store")
	in, known := m.MatchesFilter(domain.Issue{Identifier: "KAN-1"})
	assert.True(t, known)
	assert.False(t, in, "an issue without the label is out of scope")
	_, ok = wrapped.(tracker.IdempotentCommenter)
	assert.True(t, ok)
	_, ok = wrapped.(tracker.StateListSetter)
	assert.True(t, ok)
	assert.Contains(t, trackerStoreScope(cfg), "jira|", "a Jira store file is not read for another tracker")
}

// TestWithTrackerStoreWebhooks (#131): with tracker.webhooks on, the server
// gets a receiver for the tracker, and the store's periodic pull becomes the
// safety net.
func TestWithTrackerStoreWebhooks(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "github"
	cfg.Tracker.ProjectSlug = "owner/repo"
	cfg.Tracker.ActiveStates = []string{"todo"}
	cfg.Tracker.Store = config.TrackerStoreConfig{Enabled: true, SyncIntervalMs: 60000}
	assert.Equal(t, 60000, storeSyncIntervalMs(cfg.Tracker))

	cfg.Tracker.Webhooks = config.TrackerWebhooksConfig{Enabled: true, Secret: "s3cret", SafetyNetIntervalMs: 600000}
	assert.Equal(t, 600000, storeSyncIntervalMs(cfg.Tracker), "with webhooks the periodic pull is the safety net")
	gh := ghclient.NewClient(ghclient.ClientConfig{APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: "http://127.0.0.1:1"})
	_, hooks := withTrackerStore(t.Context(), cfg, t.TempDir()+"/WORKFLOW.md", gh)
	require.NotNil(t, hooks)
	assert.Equal(t, "github", hooks.Tracker())
}

// TestWithTrackerStoreJiraCountsActiveViewIssuesAsCandidates: for a Jira
// tracker the store counts an active issue that the cold pull's first
// search (the candidates) missed and its view searches listed, as Jira's
// lagging search index can make happen, once a read by id confirms it;
// without that it would not be dispatched until the next full pull.
func TestWithTrackerStoreJiraCountsActiveViewIssuesAsCandidates(t *testing.T) {
	var searches, bulkReads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := func(id, key string) map[string]any {
			return map[string]any{"id": id, "key": key, "fields": map[string]any{"summary": key, "status": map[string]any{"name": "To Do"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/rest/api/3/issue/bulkfetch" {
			bulkReads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{issue("2", "KAN-2")}})
			return
		}
		var body struct {
			JQL string `json:"jql"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		issues := []any{}
		if strings.Contains(body.JQL, `"To Do"`) {
			issues = append(issues, issue("1", "KAN-1"))
			if searches.Add(1) > 1 { // the first search does not show KAN-2 yet
				issues = append(issues, issue("2", "KAN-2"))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": issues, "isLast": true})
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Tracker.Kind = "jira"
	cfg.Tracker.Endpoint = srv.URL
	cfg.Tracker.ProjectSlug = "KAN"
	cfg.Tracker.ActiveStates = []string{"To Do"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Tracker.Store = config.TrackerStoreConfig{Enabled: true, SyncIntervalMs: 60000}
	jc := jiraclient.NewClient(jiraclient.ClientConfig{Endpoint: srv.URL, APIKey: "me@example.com:tok", ProjectSlug: "KAN", ActiveStates: cfg.Tracker.ActiveStates})

	wrapped, _ := withTrackerStore(t.Context(), cfg, t.TempDir()+"/WORKFLOW.md", jc)
	cands, err := wrapped.FetchCandidateIssues(t.Context())

	require.NoError(t, err)
	var keys []string
	for _, is := range cands {
		keys = append(keys, is.Identifier)
	}
	assert.ElementsMatch(t, []string{"KAN-1", "KAN-2"}, keys)
	assert.Greater(t, searches.Load(), int64(1), "the views were searched after the candidates")
	assert.Equal(t, int64(1), bulkReads.Load(), "KAN-2 was read by id before it was counted")
}
