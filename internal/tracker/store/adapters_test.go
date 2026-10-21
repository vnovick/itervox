package store_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// readsAfterColdPull wraps an adapter, makes the reads a tick and the
// dashboard make, and returns the requests the adapter sent before and after
// the cold pull, plus the store's answers.
func readsAfterColdPull(t *testing.T, up tracker.Tracker, requests *atomic.Int64, cfg store.Config) (cold, after int64, candidates, board []string) {
	t.Helper()
	tr, _, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	ctx := t.Context()
	_, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	cold = requests.Load()
	for range 5 {
		cands, err := tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		all, err := tr.FetchIssuesByStates(ctx, cfg.Views[0])
		require.NoError(t, err)
		candidates, board = identifiers(cands), identifiers(all)
	}
	return cold, requests.Load() - cold, candidates, board
}

// TestStoreLinearAdapter: the Linear client works through the store, and
// after the cold pull answers from it without a request.
func TestStoreLinearAdapter(t *testing.T) {
	type node = map[string]any
	issues := []struct{ id, ident, state string }{
		{"a", "ENG-1", "Todo"}, {"b", "ENG-2", "Backlog"}, {"c", "ENG-3", "Done"},
	}
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Variables struct {
				StateNames []string `json:"stateNames"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		nodes := []any{}
		for _, i := range issues {
			if slices.Contains(body.Variables.StateNames, i.state) {
				nodes = append(nodes, node{
					"id": i.id, "identifier": i.ident, "title": "Issue " + i.ident,
					"state":            node{"name": i.state},
					"labels":           node{"nodes": []any{}},
					"inverseRelations": node{"nodes": []any{}},
				})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(node{"data": node{"issues": node{
			"nodes": nodes, "pageInfo": node{"hasNextPage": false, "endCursor": nil},
		}}})
	}))
	defer srv.Close()

	up := linear.NewClient(linear.ClientConfig{
		APIKey: "lin_test", Endpoint: srv.URL, ActiveStates: active, TerminalStates: terminal,
	})
	cfg := storeConfig(t, t.TempDir())
	cold, after, cands, board := readsAfterColdPull(t, up, &requests, cfg)
	assert.Positive(t, cold)
	assert.Zero(t, after, "no Linear request after the cold pull")
	assert.Equal(t, []string{"ENG-1"}, cands)
	assert.ElementsMatch(t, []string{"ENG-1", "ENG-2", "ENG-3"}, board)
}

// TestStoreGitHubAdapter: the GitHub client works through the store, and
// after the cold pull answers from it without a request.
func TestStoreGitHubAdapter(t *testing.T) {
	type issue struct {
		number      int
		label, open string
	}
	issues := []issue{{1, "todo", "open"}, {2, "backlog", "open"}, {3, "done", "closed"}}
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		out := []any{}
		if strings.HasSuffix(r.URL.Path, "/repos/owner/repo/issues") {
			for _, i := range issues {
				if i.open == q.Get("state") && (q.Get("labels") == "" || q.Get("labels") == i.label) {
					out = append(out, map[string]any{
						"number": i.number, "title": fmt.Sprintf("Issue %d", i.number), "state": i.open,
						"labels":   []any{map[string]any{"name": i.label}},
						"html_url": fmt.Sprintf("https://github.com/owner/repo/issues/%d", i.number),
						"body":     "",
					})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	up := ghclient.NewClient(ghclient.ClientConfig{
		APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: srv.URL,
		ActiveStates: []string{"todo"}, TerminalStates: []string{"done"}, BacklogStates: []string{"backlog"},
	})
	cfg := storeConfig(t, t.TempDir())
	cfg.ActiveStates = []string{"todo"}
	cfg.Views = [][]string{{"backlog", "todo", "closed"}}
	cold, after, cands, board := readsAfterColdPull(t, up, &requests, cfg)
	assert.Positive(t, cold)
	assert.Zero(t, after, "no GitHub request after the cold pull")
	assert.Equal(t, []string{"#1"}, cands)
	assert.ElementsMatch(t, []string{"#1", "#2", "#3"}, board)
}
