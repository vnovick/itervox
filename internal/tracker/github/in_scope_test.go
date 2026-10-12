package github_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// A transfer can land the issue on the same number in another repository;
// the redirect's answer names that repository, so the issue is out of scope
// even though its number was asked for. An issue that names this
// repository is in scope.
func TestFetchIssuesInScopeChecksTheRepositoryNotJustTheNumber(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := func(owner, repo string, n int) map[string]any {
			return map[string]any{"number": n, "title": owner + "/" + repo, "state": "open", "labels": []any{map[string]any{"name": "todo"}},
				"repository_url": srvURL + "/repos/" + owner + "/" + repo,
				"html_url":       fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, n)}
		}
		switch r.URL.Path {
		case "/repos/owner/repo/issues/1":
			http.Redirect(w, r, "/repos/other/place/issues/1", http.StatusMovedPermanently)
		case "/repos/other/place/issues/1":
			_ = json.NewEncoder(w).Encode(issue("other", "place", 1))
		case "/repos/owner/repo/issues/2":
			_ = json.NewEncoder(w).Encode(issue("Owner", "Repo", 2)) // GitHub's own casing may differ
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	c := ghclient.NewClient(ghclient.ClientConfig{APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: srv.URL,
		ActiveStates: []string{"todo"}})
	got, err := c.FetchIssuesInScope(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	require.Len(t, got, 1, "the same-number issue in another repository is left out")
	assert.Equal(t, "2", got[0].ID)
}
