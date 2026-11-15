package tracker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// #253: create_issue's labels and blocked_by_source, per adapter, against
// httptest fakes of the Linear GraphQL and GitHub REST APIs and a real local
// issues directory.

type linearCreateRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// linearCreateFake answers the create-issue operations and records them.
type linearCreateFake struct {
	mu   sync.Mutex
	reqs []linearCreateRequest
}

func (f *linearCreateFake) named(op string) []linearCreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []linearCreateRequest
	for _, r := range f.reqs {
		if strings.Contains(r.Query, op) {
			out = append(out, r)
		}
	}
	return out
}

func newLinearCreateFake(t *testing.T) (*linearCreateFake, *linear.Client) {
	t.Helper()
	f := &linearCreateFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req linearCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "ItervoxResolveCreateIssueContext"):
			data = map[string]any{
				"issue": map[string]any{
					"id": "uuid-src", "identifier": "ENG-1", "url": "https://linear.app/x/issue/ENG-1",
					"state": map[string]any{"name": "In Progress"},
					"team": map[string]any{
						"id":     "team-1",
						"states": map[string]any{"nodes": []any{map[string]any{"id": "st-todo", "name": "Todo"}}},
						"labels": map[string]any{"nodes": []any{
							map[string]any{"id": "lbl-web", "name": "repo:web"},
							map[string]any{"id": "lbl-bug", "name": "bug"},
						}},
					},
					"project": map[string]any{"id": "proj-1"},
				},
				"issueLabels": map[string]any{"nodes": []any{map[string]any{"id": "lbl-ws", "name": "cross-repo"}}},
			}
		case strings.Contains(req.Query, "ItervoxCreateIssue"):
			data = map[string]any{"issueCreate": map[string]any{"success": true, "issue": map[string]any{
				"id": "uuid-new", "identifier": "ENG-2", "title": "Web follow-up",
				"state":  map[string]any{"name": "Todo"},
				"labels": map[string]any{"nodes": []any{map[string]any{"name": "repo:web"}}},
			}}}
		case strings.Contains(req.Query, "ItervoxSyncRelationCreate"):
			data = map[string]any{"issueRelationCreate": map[string]any{"success": true}}
		default:
			t.Errorf("unexpected Linear operation: %s", req.Query)
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	t.Cleanup(srv.Close)
	return f, linear.NewClient(linear.ClientConfig{APIKey: "test-" + "key", Endpoint: srv.URL})
}

// ghCreateFake records issue-create bodies, answers each with the created
// issue (the body and labels echoed, as GitHub does) and serves it back.
type ghCreateFake struct {
	mu    sync.Mutex
	posts []map[string]any
	last  map[string]any
}

func newGHCreateFake(t *testing.T) (*ghCreateFake, *ghclient.Client) {
	t.Helper()
	f := &ghCreateFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.posts = append(f.posts, body)
			var labels []any
			if ls, ok := body["labels"].([]any); ok {
				for _, l := range ls {
					labels = append(labels, map[string]any{"name": l})
				}
			}
			f.last = map[string]any{"number": 8, "title": body["title"], "state": "open", "body": body["body"],
				"labels": labels, "html_url": "https://github.com/owner/repo/issues/8"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(f.last)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/8":
			_ = json.NewEncoder(w).Encode(f.last)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/8/comments":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "Source", "state": "open",
				"labels": []any{map[string]any{"name": "in progress"}}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, ghclient.NewClient(ghclient.ClientConfig{
		APIKey: "test-" + "token", ProjectSlug: "owner/repo", Endpoint: srv.URL,
		ActiveStates: []string{"todo", "in progress"}, TerminalStates: []string{"done"},
	})
}

// TestCreateIssueAppliesLabelsLinear (#253): labels are resolved to ids among
// the source team's labels and the workspace labels (ignoring case) and sent
// with issueCreate in the source's team and project; a name that matches no
// label fails the create before issueCreate, so a routing label is never
// silently dropped.
func TestCreateIssueAppliesLabelsLinear(t *testing.T) {
	f, client := newLinearCreateFake(t)
	is, err := client.CreateIssue(t.Context(), "uuid-src", tracker.NewIssue{
		Title: "Web follow-up", Body: "Wire the new endpoint", State: "Todo",
		Labels: []string{"Repo:Web", "cross-repo"},
	})
	require.NoError(t, err)
	assert.Equal(t, "ENG-2", is.Identifier)
	assert.Equal(t, []string{"repo:web"}, is.Labels)

	create := f.named("ItervoxCreateIssue")
	require.Len(t, create, 1)
	input := create[0].Variables["input"].(map[string]any)
	assert.Equal(t, "team-1", input["teamId"])
	assert.Equal(t, "proj-1", input["projectId"])
	assert.Equal(t, "st-todo", input["stateId"])
	assert.Equal(t, []any{"lbl-web", "lbl-ws"}, input["labelIds"])
	assert.Empty(t, f.named("ItervoxSyncRelationCreate"), "no blocker asked for")

	_, err = client.CreateIssue(t.Context(), "uuid-src", tracker.NewIssue{
		Title: "T", State: "Todo", Labels: []string{"repo:mobile"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `label "repo:mobile" not found`)
	assert.Len(t, f.named("ItervoxCreateIssue"), 1, "an unknown label creates nothing")

	_, err = client.CreateIssue(t.Context(), "uuid-src", tracker.NewIssue{Title: "T", State: "Todo"})
	require.NoError(t, err)
	create = f.named("ItervoxCreateIssue")
	require.Len(t, create, 2)
	assert.NotContains(t, create[1].Variables["input"], "labelIds", "no labels, no labelIds")
}

// TestCreateIssueAppliesLabelsGitHub (#253): the labels go beside the state
// label in the create request, the state label first and duplicates (ignoring
// case) dropped; the created issue reads them back.
func TestCreateIssueAppliesLabelsGitHub(t *testing.T) {
	f, client := newGHCreateFake(t)
	is, err := client.CreateIssue(t.Context(), "7", tracker.NewIssue{
		Title: "Web follow-up", Body: "Wire the new endpoint", State: "todo",
		Labels: []string{"repo:web", "Repo:Web", "TODO"},
	})
	require.NoError(t, err)
	require.Len(t, f.posts, 1)
	assert.Equal(t, []any{"todo", "repo:web"}, f.posts[0]["labels"])
	assert.Equal(t, "Wire the new endpoint", f.posts[0]["body"], "no blocker line without blocked_by_source")
	assert.Equal(t, "#8", is.Identifier)
	assert.ElementsMatch(t, []string{"todo", "repo:web"}, is.Labels)
	assert.Empty(t, is.BlockedBy)
}

// TestCreateIssueBlockedBySource (#253): blocked_by_source records the source
// as the new issue's blocker in the form each adapter's reader already
// reads — a Linear "blocks" relation from the source, the GitHub managed
// "Blocked by #N" body line, the local blocked_by front matter — and the
// issue the adapter returns (and, for GitHub and local, the next read)
// reports the source as its blocker.
func TestCreateIssueBlockedBySource(t *testing.T) {
	t.Run("linear", func(t *testing.T) {
		f, client := newLinearCreateFake(t)
		is, err := client.CreateIssue(t.Context(), "uuid-src", tracker.NewIssue{
			Title: "Web follow-up", State: "Todo", Labels: []string{"repo:web"}, BlockedBySource: true,
		})
		require.NoError(t, err)
		rel := f.named("ItervoxSyncRelationCreate")
		require.Len(t, rel, 1)
		// The source "blocks" the new issue: the new issue's inverseRelations
		// then carry {type: blocks, issue: source}, which normalizeIssue reads
		// as its blocker.
		assert.Equal(t, map[string]any{"issueId": "uuid-src", "relatedIssueId": "uuid-new", "type": "blocks"},
			rel[0].Variables["input"])
		require.Len(t, is.BlockedBy, 1)
		assertBlocker(t, is.BlockedBy[0], "uuid-src", "ENG-1", "In Progress")
	})

	t.Run("github", func(t *testing.T) {
		f, client := newGHCreateFake(t)
		is, err := client.CreateIssue(t.Context(), "7", tracker.NewIssue{
			Title: "Web follow-up", Body: "Wire the new endpoint", State: "todo", BlockedBySource: true,
		})
		require.NoError(t, err)
		require.Len(t, f.posts, 1)
		assert.Equal(t, "Wire the new endpoint\n\nBlocked by #7 "+syncBlockersMarker(), f.posts[0]["body"])
		require.Len(t, is.BlockedBy, 1)
		assert.Equal(t, "#7", *is.BlockedBy[0].Identifier)

		again, err := client.FetchIssueDetail(t.Context(), "8")
		require.NoError(t, err)
		require.Len(t, again.BlockedBy, 1, "the body parser reads the line back")
		assert.Equal(t, "7", *again.BlockedBy[0].ID)
		require.NotNil(t, again.Description)
		assert.Contains(t, *again.Description, "Wire the new endpoint")
	})

	t.Run("local", func(t *testing.T) {
		dir := t.TempDir()
		tr := local.New(local.Config{Dir: dir, Prefix: "ITX", ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"}})
		src, err := tr.CreateIssue(t.Context(), "", tracker.NewIssue{Title: "API change", State: "In Progress"})
		require.NoError(t, err)
		is, err := tr.CreateIssue(t.Context(), src.ID, tracker.NewIssue{
			Title: "Web follow-up", State: "Todo", Labels: []string{"repo:web", "Repo:Web"}, BlockedBySource: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "ITX-2", is.Identifier)

		raw, err := os.ReadFile(filepath.Join(dir, "ITX-2.md"))
		require.NoError(t, err)
		assert.Contains(t, string(raw), `labels: ["repo:web"]`)
		assert.Contains(t, string(raw), `blocked_by: ["ITX-1"]`)

		reread := local.New(local.Config{Dir: dir, Prefix: "ITX", ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"}})
		again, err := reread.FetchIssueDetail(t.Context(), "ITX-2")
		require.NoError(t, err)
		assert.Equal(t, []string{"repo:web"}, again.Labels)
		require.Len(t, again.BlockedBy, 1)
		assertBlocker(t, again.BlockedBy[0], "ITX-1", "ITX-1", "In Progress")
	})

	t.Run("memory", func(t *testing.T) {
		mem := tracker.NewMemoryTracker([]domain.Issue{{ID: "id-src", Identifier: "ENG-1", State: "In Progress"}},
			[]string{"Todo", "In Progress"}, []string{"Done"})
		is, err := mem.CreateIssue(t.Context(), "id-src", tracker.NewIssue{
			Title: "Web follow-up", State: "Todo", Labels: []string{"Repo:Web"}, BlockedBySource: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"repo:web"}, is.Labels)
		require.Len(t, is.BlockedBy, 1)
		assertBlocker(t, is.BlockedBy[0], "id-src", "ENG-1", "In Progress")
	})
}

func assertBlocker(t *testing.T, ref domain.BlockerRef, id, identifier, state string) {
	t.Helper()
	require.NotNil(t, ref.ID)
	assert.Equal(t, id, *ref.ID)
	require.NotNil(t, ref.Identifier)
	assert.Equal(t, identifier, *ref.Identifier)
	require.NotNil(t, ref.State)
	assert.Equal(t, state, *ref.State)
}

// syncBlockersMarker is the hidden marker ending the managed blockers line.
func syncBlockersMarker() string {
	line := tracker.SyncBlockersLine([]string{"#1"})
	return strings.TrimPrefix(line, "Blocked by #1 ")
}
