package jira_test

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestAuthHeaderIsBasicEmailToken(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{}, "isLast": true})
	})

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	reqs := seen()
	require.Len(t, reqs, 1)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:tok"))
	assert.Equal(t, want, reqs[0].Header.Get("Authorization"))
	assert.Equal(t, "application/json", reqs[0].Header.Get("Accept"))
	assert.Equal(t, "application/json", reqs[0].Header.Get("Content-Type"))
	assert.Equal(t, "/rest/api/3/search/jql", reqs[0].Path, "trailing slash on Endpoint is trimmed")
}

func TestNotFoundMapsToTrackerErrNotFound(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.FetchCandidateIssues(t.Context())

	require.Error(t, err)
	assert.ErrorIs(t, err, tracker.ErrNotFound)
}

func TestAPIStatusErrorCarriesStatus(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := c.FetchCandidateIssues(t.Context())

	var apiErr *tracker.APIStatusError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.Status)
	assert.Equal(t, "jira", apiErr.Adapter)
}
