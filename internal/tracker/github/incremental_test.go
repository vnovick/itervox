package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

// TestGHFetchIssuesUpdatedSince (#114): one paginated issues listing in any
// state since the cursor. A closed issue comes back in its terminal state, an
// issue with a backlog or completion label in that state, an open issue with
// no tracked label as gone, and pull requests not at all.
func TestGHFetchIssuesUpdatedSince(t *testing.T) {
	since := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	pr := ghIssue(9, "A pull request", "open", []string{"todo"})
	pr["pull_request"] = map[string]any{"url": "x"}
	pages := [][]any{
		{ghIssue(1, "Edited", "open", []string{"todo"}), ghIssue(2, "Closed", "closed", nil), pr},
		{ghIssue(3, "Parked", "open", []string{"backlog"}), ghIssue(4, "Untracked now", "open", []string{"question"}),
			ghIssue(5, "In review", "open", []string{"in review"})},
	}
	var calls atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		assert.Equal(t, "/repos/owner/repo/issues", r.URL.Path)
		q := r.URL.Query()
		if n == 1 {
			assert.Equal(t, "all", q.Get("state"))
			assert.Equal(t, "2026-10-01T09:30:00Z", q.Get("since"))
			assert.Equal(t, "updated", q.Get("sort"))
			assert.Equal(t, "desc", q.Get("direction"))
			w.Header().Set("Link", `<`+srv.URL+`/repos/owner/repo/issues?page=2>; rel="next"`)
		}
		require.NoError(t, json.NewEncoder(w).Encode(pages[n-1]))
	}))
	defer srv.Close()
	cfg := defaultConfig(srv.URL)
	cfg.BacklogStates = []string{"backlog"}
	cfg.CompletionState = "in review"
	client := ghclient.NewClient(cfg)

	changed, gone, err := client.FetchIssuesUpdatedSince(t.Context(), since)
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(), "one listing, two pages")
	states := map[string]string{}
	for _, iss := range changed {
		states[iss.Identifier] = iss.State
	}
	assert.Equal(t, map[string]string{"#1": "todo", "#2": "closed", "#3": "backlog", "#5": "in review"}, states)
	assert.Equal(t, []string{"4"}, gone, "an open issue without a tracked label left the scope")
}

// TestGHIncrementalClosedIssueIsGoneUnlessClosedIsTracked: the full pull
// lists closed issues only when "closed" is a terminal state; otherwise it
// lists open issues by label. The incremental pull must agree, or a closed
// issue would show on the board until the next full pull.
func TestGHIncrementalClosedIssueIsGoneUnlessClosedIsTracked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode([]any{
			ghIssue(1, "Done, still open", "open", []string{"done"}),
			ghIssue(2, "Closed as done", "closed", []string{"done"}),
		}))
	}))
	defer srv.Close()
	cfg := defaultConfig(srv.URL)
	cfg.TerminalStates = []string{"done", "cancelled"}
	changed, gone, err := ghclient.NewClient(cfg).FetchIssuesUpdatedSince(t.Context(), time.Now())
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, "#1", changed[0].Identifier)
	assert.Equal(t, "done", changed[0].State)
	assert.Equal(t, []string{"2"}, gone)
}
