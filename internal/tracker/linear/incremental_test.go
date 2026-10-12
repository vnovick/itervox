package linear_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/linear"
)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// TestLinearFetchIssuesUpdatedSince (#114): the issues updated since the
// cursor, in any state and including archived and trashed ones, in the
// candidate poll's scope; trashed and archived issues come back as gone.
func TestLinearFetchIssuesUpdatedSince(t *testing.T) {
	node := func(id, ident, state string) map[string]any {
		return map[string]any{
			"id": id, "identifier": ident, "title": "T " + ident,
			"state": map[string]any{"name": state}, "updatedAt": "2026-10-01T10:00:00.000Z",
		}
	}
	trashed := node("id-3", "ENG-3", "Todo")
	trashed["trashed"] = true
	archived := node("id-4", "ENG-4", "Done")
	archived["archivedAt"] = "2026-10-01T10:00:00.000Z"
	pages := []map[string]any{
		{"nodes": []any{node("id-1", "ENG-1", "Todo"), trashed}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"}},
		{"nodes": []any{node("id-2", "ENG-2", "Done"), archived}, "pageInfo": map[string]any{"hasNextPage": false}},
	}
	var mu sync.Mutex
	var reqs []gqlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gqlRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		reqs = append(reqs, req)
		n := len(reqs)
		mu.Unlock()
		page := pages[(n-1)%len(pages)]
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": page}}))
	}))
	defer srv.Close()
	since := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

	client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL, ProjectSlug: "ours"})
	changed, gone, err := client.FetchIssuesUpdatedSince(t.Context(), since)
	require.NoError(t, err)
	require.Len(t, reqs, 2, "one query, two pages")
	q := reqs[0]
	assert.Contains(t, q.Query, "updatedAt: {gte: $since}")
	assert.Contains(t, q.Query, "includeArchived: true")
	assert.Contains(t, q.Query, "project: {slugId: {eq: $projectSlug}}")
	assert.NotContains(t, q.Query, "stateNames", "any state")
	assert.Equal(t, "2026-10-01T09:30:00Z", q.Variables["since"])
	assert.Equal(t, "ours", q.Variables["projectSlug"])
	assert.Equal(t, "c1", reqs[1].Variables["after"])
	var got []string
	for _, iss := range changed {
		got = append(got, iss.Identifier+"="+iss.State)
	}
	assert.Equal(t, []string{"ENG-1=Todo", "ENG-2=Done"}, got)
	assert.Equal(t, []string{"id-3", "id-4"}, gone, "trashed and archived issues are gone")

	// The runtime project filter: one query per project, the no-project
	// bucket included.
	reqs = nil
	client.SetProjectFilter([]string{"theirs", "__no_project__"})
	changed, _, err = client.FetchIssuesUpdatedSince(t.Context(), since)
	require.NoError(t, err)
	got = nil
	for _, iss := range changed {
		got = append(got, iss.Identifier)
	}
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, got, "an issue listed by two queries is returned once")
	var scopes []string
	for _, r := range reqs {
		switch {
		case strings.Contains(r.Query, "project: {null: true}"):
			scopes = append(scopes, "none")
		case r.Variables["projectSlug"] != nil:
			scopes = append(scopes, r.Variables["projectSlug"].(string))
		}
	}
	assert.Equal(t, []string{"theirs", "theirs", "none", "none"}, scopes)

	// An empty filter means every project: one query with no project clause.
	reqs = nil
	client.SetProjectFilter([]string{})
	changed, _, err = client.FetchIssuesUpdatedSince(t.Context(), since)
	require.NoError(t, err)
	require.Len(t, reqs, 2, "one query, two pages")
	assert.Contains(t, reqs[0].Query, "ItervoxLinearChangesAll")
	assert.NotContains(t, reqs[0].Query, "project:")
	assert.NotContains(t, reqs[0].Variables, "projectSlug")
	assert.Len(t, changed, 2)
}
