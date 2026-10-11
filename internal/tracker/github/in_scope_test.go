package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

// TestFetchIssuesInScopeLeavesOutATransferredIssue (#131): GitHub answers a
// transferred issue with a redirect to its new repository. The issue that
// comes back is not this repository's, so it is left out rather than stored
// under its own number.
func TestFetchIssuesInScopeLeavesOutATransferredIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues/7":
			http.Redirect(w, r, "/repos/other/place/issues/3", http.StatusMovedPermanently)
		case "/repos/other/place/issues/3":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 3, "title": "foreign issue", "state": "open", "labels": []any{}})
		case "/repos/owner/repo/issues/8":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 8, "title": "ours", "state": "open", "labels": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := ghclient.NewClient(ghclient.ClientConfig{APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: srv.URL})
	got, err := c.FetchIssuesInScope(context.Background(), []string{"7", "8"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "8", got[0].ID)
}
